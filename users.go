package life

import (
	"context"
	"errors"
	"fmt"
	"time"

	users "github.com/e601201/life-api/gen/users"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"goa.design/clue/log"
	"golang.org/x/crypto/bcrypt"
)

// users service の実装。登録・ログイン（JWT 発行）・自分の情報。
// データは users テーブル（db/migrations/000002_create_users.up.sql）。
type userssrvc struct {
	// JWTAuth（me の認証）と Issue（login の発行）をここから使う。
	*Auth
	db *pgxpool.Pool
}

// NewUsers returns the users service implementation.
func NewUsers(pool *pgxpool.Pool, auth *Auth) users.Service {
	return &userssrvc{Auth: auth, db: pool}
}

// userColumns は User を組み立てるのに要る列。password_hash は含めない。
const userColumns = `id, email, created_at`

// bcryptCost はハッシュの計算量。DefaultCost（10）で 1 回 50〜100ms 程度。
// 上げるほど総当たりに強くなるが、login のたびにその時間がかかる。
const bcryptCost = bcrypt.DefaultCost

// dummyHash は存在しない email でログインされたときに比較する相手。
//
// ユーザが居ないときに bcrypt の比較を飛ばして即 401 を返すと、応答時間の差で
// 「その email は未登録」と外から判別できる。居ても居なくても 1 回ぶんの比較を
// 挟んで、時間を揃える。値そのものに意味は無いので起動時に作る。
var dummyHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy"), bcryptCost)
	if err != nil {
		panic(fmt.Sprintf("bcrypt: %v", err))
	}
	return h
}()

// scanUser は users の 1 行を API の User に詰め替える。
func scanUser(row pgx.Row) (*users.User, error) {
	var (
		id        int64
		email     string
		createdAt time.Time
	)
	if err := row.Scan(&id, &email, &createdAt); err != nil {
		return nil, err
	}
	return &users.User{
		ID:        id,
		Email:     email,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// isUniqueViolation は UNIQUE 制約違反（同じ email）かどうかを見る。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}

// invalidCredentials は login の失敗。email の有無で文言を変えない（design 参照）。
func invalidCredentials() error {
	return users.MakeUnauthorized(errors.New("invalid email or password"))
}

// Register a new user
func (s *userssrvc) Register(ctx context.Context, p *users.Credentials) (*users.User, error) {
	log.Printf(ctx, "users.register")

	// bcrypt は 72 バイトを超える入力を拒否する。design の MaxLength(72) は文字数を
	// 数えるので、マルチバイトの入力だけがここまで来る。
	hash, err := bcrypt.GenerateFromPassword([]byte(p.Password), bcryptCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return nil, users.MakePasswordTooLong(errors.New("password must be at most 72 bytes"))
	}
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	// email はそのまま保存する（大文字小文字の区別は users_email_lower_idx が吸収する）。
	// 重複は事前の SELECT ではなく INSERT の失敗で知る。先に確認しても、その直後に
	// 同じ email が入る隙間ができるため。
	const q = `INSERT INTO users (email, password_hash)
	           VALUES ($1, $2)
	           RETURNING ` + userColumns

	u, err := scanUser(s.db.QueryRow(ctx, q, p.Email, string(hash)))
	if isUniqueViolation(err) {
		return nil, users.MakeConflict(errors.New("email already registered"))
	}
	if err != nil {
		return nil, fmt.Errorf("register user: %w", err)
	}
	return u, nil
}

// Issue a JWT for the given credentials
func (s *userssrvc) Login(ctx context.Context, p *users.LoginPayload) (*users.TokenResponse, error) {
	log.Printf(ctx, "users.login")

	const q = `SELECT id, password_hash FROM users WHERE lower(email) = lower($1)`

	var (
		id   int64
		hash string
	)
	err := s.db.QueryRow(ctx, q, p.Email).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// 居ない場合も比較を 1 回挟んで、居る場合と応答時間を揃える（dummyHash 参照）。
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(p.Password))
		return nil, invalidCredentials()
	}
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(p.Password)); err != nil {
		return nil, invalidCredentials()
	}

	token, err := s.Issue(id)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return &users.TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int(tokenTTL.Seconds()),
	}, nil
}

// Return the authenticated user
func (s *userssrvc) Me(ctx context.Context, _ *users.MePayload) (*users.User, error) {
	log.Printf(ctx, "users.me")

	// JWTAuth が通っていれば必ず入っている。無いのは認証を経ずに呼ばれたとき。
	id, ok := UserIDFromContext(ctx)
	if !ok {
		return nil, unauthorized("missing user")
	}

	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1`

	u, err := scanUser(s.db.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		// トークンは有効だがユーザが消えている。JWT は失効させられないので、
		// 「その人としては認証できない」として 401 にする。
		return nil, unauthorized("user not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}
