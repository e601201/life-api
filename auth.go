package life

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"goa.design/clue/log"
	goa "goa.design/goa/v3/pkg"
	"goa.design/goa/v3/security"
)

// JWT の発行と検証。design の JWTSecurity("jwt") に対応する実装で、
// users.login が Issue で発行し、守られた各メソッドの手前で JWTAuth が検証する。
//
// 署名は HS256（共有鍵）。発行も検証もこのプロセスだけなので、公開鍵を配る
// 必要がなく、鍵が 1 本で済む。鍵は環境変数 JWT_SECRET で受け取り、
// リポジトリにもイメージにも置かない（cmd/life/main.go 参照）。

const (
	// tokenTTL はトークンの有効期間。切れたら login で取り直す。
	// 失効の仕組み（ブラックリスト等）は持たないので、長くしすぎない。
	tokenTTL = 24 * time.Hour

	// tokenIssuer は iss クレームの値。検証時にも要求するので、同じ鍵を
	// 使い回した別サービスのトークンが混ざっても弾ける。
	tokenIssuer = "life-api"

	// minSecretLen は署名鍵の最短長。HS256 は鍵長がそのまま強度になるため、
	// RFC 7518 §3.2 が求める 256 bit を下限にする。
	minSecretLen = 32
)

// Auth は JWT の発行と検証をまとめたもの。users と entries の両サービスが
// 埋め込んで、Goa が生成した Auther インターフェース（JWTAuth）を満たす。
type Auth struct {
	secret []byte
	// now は時刻の取得。テストで期限切れを作るために差し替えられるようにしている。
	now func() time.Time
}

// NewAuth は署名鍵から Auth を作る。短い鍵は起動時点で拒否する。
func NewAuth(secret string) (*Auth, error) {
	if len(secret) < minSecretLen {
		return nil, fmt.Errorf("jwt secret must be at least %d bytes, got %d", minSecretLen, len(secret))
	}
	return &Auth{secret: []byte(secret), now: time.Now}, nil
}

// Issue は userID を sub に持つ JWT を発行する。
func (a *Auth) Issue(userID int64) (string, error) {
	now := a.now()
	claims := jwt.RegisteredClaims{
		Issuer:    tokenIssuer,
		Subject:   strconv.FormatInt(userID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return token, nil
}

// Verify は署名・発行者・有効期限を確かめ、sub のユーザ ID を返す。
func (a *Auth) Verify(token string) (int64, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, claims, a.key,
		// alg をトークン側に選ばせない。これが無いと "none" や、共有鍵を公開鍵と
		// 見なして RS256 → HS256 に読み替える古典的な攻撃が通る。
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		// exp の無いトークンは永久に有効になってしまうので、必須にする。
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(a.now),
	)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid subject %q: %w", claims.Subject, err)
	}
	return id, nil
}

// key は jwt.Keyfunc。alg は WithValidMethods で絞っているので、ここでは鍵を返すだけ。
func (a *Auth) key(*jwt.Token) (any, error) {
	return a.secret, nil
}

// JWTAuth は Goa が生成した Auther インターフェースの実装。生成コードがエンドポイントの
// 手前で呼び、返した ctx がサービスのメソッドに渡る。検証が通ればユーザ ID を ctx に
// 入れ、通らなければ design で宣言した unauthorized（HTTP では 401）を返す。
//
// token はデコード時に Bearer の接頭辞が剥がされた生の JWT。ヘッダが無いときは空文字
// （design の jwtToken で Required にしていないため）。
func (a *Auth) JWTAuth(ctx context.Context, token string, _ *security.JWTScheme) (context.Context, error) {
	if token == "" {
		return ctx, unauthorized("missing token")
	}
	id, err := a.Verify(token)
	if err != nil {
		// 理由（期限切れ・署名不一致・壊れている）はログにだけ残す。クライアントに
		// 返すと、どこまで正しいトークンを作れたかの手がかりになる。
		log.Printf(ctx, "auth: reject token: %v", err)
		return ctx, unauthorized("invalid token")
	}
	return ContextWithUserID(ctx, id), nil
}

// unauthorized は design で宣言した unauthorized エラーを組み立てる。
// 生成コードの MakeUnauthorized（users / entries それぞれにある）と同じ形で、
// どのサービスからでも同じ名前で返せるようにここで作る。
func unauthorized(msg string) error {
	return goa.NewServiceError(errors.New(msg), "unauthorized", false, false, false)
}

// userIDKey は ctx にユーザ ID を入れるときのキー。非公開の型にして、
// 他のパッケージが同じキーで上書きできないようにしている。
type userIDKey struct{}

// ContextWithUserID は認証済みのユーザ ID を ctx に載せる。
//
// ログにも同じ ID を付ける。この ctx から出る行（サービスの log.Printf）には user_id が
// 付き、RequestLog が最後に出す 1 行にも載る（reqlog.go）。パスワードやトークン本体は載せない。
func ContextWithUserID(ctx context.Context, id int64) context.Context {
	setLogUserID(ctx, id)
	ctx = log.With(ctx, log.KV{K: UserIDLogKey, V: id})
	return context.WithValue(ctx, userIDKey{}, id)
}

// UserIDFromContext は JWTAuth が載せたユーザ ID を取り出す。
// 認証を通っていない ctx（テストで直接呼んだ場合など）では ok が false になる。
func UserIDFromContext(ctx context.Context) (id int64, ok bool) {
	id, ok = ctx.Value(userIDKey{}).(int64)
	return id, ok
}
