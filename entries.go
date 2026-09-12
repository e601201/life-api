package life

import (
	"context"
	"errors"
	"fmt"
	"time"

	entries "github.com/e601201/life-api/gen/entries"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goa.design/clue/log"
)

// entries service の実装。データは PostgreSQL の entries テーブルに置く
// （スキーマは db/migrations/000001_create_entries.up.sql、000003 で user_id を NOT NULL に）。
//
// 全メソッドが JWTAuth の載せたユーザ ID で絞る。一覧は自分の記録だけ、単体の
// get / update / delete は「id が無い」と「他人の id」を区別せずどちらも not_found にする。
// 403 で分けると、その id が存在することが外から分かってしまう。
type entriessrvc struct {
	// 全メソッドが JWT を要求する（design の Security）。生成コードが
	// Auth.JWTAuth を各メソッドの手前で呼ぶので、埋め込んで満たしておく。
	*Auth
	db *pgxpool.Pool
}

// NewEntries returns the entries service implementation.
func NewEntries(pool *pgxpool.Pool, auth *Auth) entries.Service {
	return &entriessrvc{Auth: auth, db: pool}
}

// journalColumns は Journal を組み立てるのに要る列。SELECT と RETURNING で
// 使い回して、scanJournal のスキャン順と食い違わないようにしている。
const journalColumns = `id, user_id, entry_date, kind, title, body, created_at, updated_at`

// dateLayout は DSL の Format(FormatDate) に対応する表現。日時のほうは
// Format(FormatDateTime) = RFC 3339 で、小数部を持てるので time.RFC3339Nano を使う。
const dateLayout = "2006-01-02"

// notFound builds the not_found error declared in the design.
func notFound(id int64) error {
	return entries.MakeNotFound(fmt.Errorf("entry %d not found", id))
}

// userFromContext は JWTAuth が ctx に載せたユーザ ID を取り出す。無いのは認証を経ずに
// 呼ばれたときで、HTTP 経由では起きない。サービスを直接呼ぶ経路で全件が見えたり、
// user_id の無い行が入ったりしないよう、ここで止める。
func userFromContext(ctx context.Context) (int64, error) {
	id, ok := UserIDFromContext(ctx)
	if !ok {
		return 0, errors.New("no user in context")
	}
	return id, nil
}

// scanJournal は entries の 1 行を API の Journal に詰め替える。
//
// 引数の pgx.Row は Scan だけを持つインターフェースで、QueryRow の戻り値と
// Query で回した各行（pgx.Rows）の両方が満たす。おかげで単体取得と一覧で
// 同じ変換を使い回せる。
func scanJournal(row pgx.Row) (*entries.Journal, error) {
	var (
		id        int64
		userID    *int64
		entryDate time.Time
		kind      string
		title     string
		body      *string
		createdAt time.Time
		updatedAt time.Time
	)
	// user_id と body は NULL を取りうるのでポインタで受ける（NULL なら nil）。
	if err := row.Scan(&id, &userID, &entryDate, &kind, &title, &body, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	// timestamptz は接続のタイムゾーンで返るため、UTC に寄せてから文字列にする。
	//
	// 秒精度（time.RFC3339Nano）だと、作成した同じ秒のうちに更新したとき created_at と
	// updated_at が同じ値になり、更新されたことが読み取れない。DB は
	// マイクロ秒まで持っているので、落とさずに出す。
	created := createdAt.UTC().Format(time.RFC3339Nano)
	updated := updatedAt.UTC().Format(time.RFC3339Nano)

	return &entries.Journal{
		ID:        &id,
		UserID:    userID,
		EntryDate: entryDate.Format(dateLayout),
		Kind:      kind,
		Title:     title,
		Body:      body,
		CreatedAt: &created,
		UpdatedAt: &updated,
	}, nil
}

// Create a new journal entry
func (s *entriessrvc) Create(ctx context.Context, p *entries.CreatePayload) (*entries.CreateResult, error) {
	log.Printf(ctx, "entries.create")

	// user_id はリクエストではなく JWT から取る（JWTAuth が ctx に載せたもの）。
	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}

	// id は IDENTITY、created_at / updated_at は DEFAULT now() に任せ、
	// 採番された値を RETURNING で受け取る。アプリ側で時刻を作らないので、
	// タスクが複数あってもタイムスタンプの基準がぶれない。
	//
	// $2::date のキャストは、text で送った "YYYY-MM-DD" を date として
	// 解釈させるため。付けないとパラメータの型が決まらず型エラーになる。
	const q = `INSERT INTO entries (user_id, entry_date, kind, title, body)
	           VALUES ($1, $2::date, $3, $4, $5)
	           RETURNING ` + journalColumns

	j, err := scanJournal(s.db.QueryRow(ctx, q, userID, p.EntryDate, p.Kind, p.Title, p.Body))
	if err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}

	return &entries.CreateResult{
		Location:  fmt.Sprintf("/entries/%d", *j.ID),
		ID:        j.ID,
		CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt,
		UserID:    j.UserID,
		EntryDate: j.EntryDate,
		Kind:      j.Kind,
		Title:     j.Title,
		Body:      j.Body,
	}, nil
}

// List journal entries
func (s *entriessrvc) List(ctx context.Context, p *entries.ListPayload) ([]*entries.Journal, error) {
	log.Printf(ctx, "entries.list limit=%d offset=%d", p.Limit, p.Offset)

	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}

	// 自分の記録を、記録日の新しい順。entries_user_id_entry_date_idx が
	// (user_id, entry_date DESC, id DESC) なので、user_id で絞ったあともソートを挟まずに読める。
	//
	// limit / offset の既定値と取りうる範囲は DSL 側（Default / Minimum / Maximum）で
	// 決めている。ここで補正すると同じ規則が 2 箇所に散るので、受け取った値をそのまま渡す。
	const q = `SELECT ` + journalColumns + `
	           FROM entries
	           WHERE user_id = $1
	           ORDER BY entry_date DESC, id DESC
	           LIMIT $2 OFFSET $3`

	rows, err := s.db.Query(ctx, q, userID, p.Limit, p.Offset)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer rows.Close()

	// nil スライスだと JSON が null になるので、空でも [] を返せるよう初期化する。
	res := []*entries.Journal{}
	for rows.Next() {
		j, err := scanJournal(rows)
		if err != nil {
			return nil, fmt.Errorf("list entries: %w", err)
		}
		res = append(res, j)
	}
	// 行の読み出し中に落ちた場合、エラーは Next ではなくここに出る。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	return res, nil
}

// Get a journal entry by ID
func (s *entriessrvc) Get(ctx context.Context, p *entries.GetPayload) (*entries.Journal, error) {
	log.Printf(ctx, "entries.get id=%d", p.ID)

	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("get entry: %w", err)
	}

	// 他人の id は WHERE で落ちて ErrNoRows になり、存在しない id と同じ not_found になる。
	const q = `SELECT ` + journalColumns + ` FROM entries WHERE id = $1 AND user_id = $2`

	j, err := scanJournal(s.db.QueryRow(ctx, q, p.ID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound(p.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("get entry: %w", err)
	}
	return j, nil
}

// Update a journal entry by ID
func (s *entriessrvc) Update(ctx context.Context, p *entries.UpdatePayload) (*entries.Journal, error) {
	log.Printf(ctx, "entries.update id=%d", p.ID)

	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}

	// created_at と user_id は SET に含めない。作成時の値をそのまま残し、
	// updated_at だけ進める。
	// 対象が無い（存在しない、または他人のもの）なら RETURNING が 1 行も返さず、ErrNoRows になる。
	const q = `UPDATE entries
	           SET entry_date = $2::date, kind = $3, title = $4, body = $5, updated_at = now()
	           WHERE id = $1 AND user_id = $6
	           RETURNING ` + journalColumns

	j, err := scanJournal(s.db.QueryRow(ctx, q, p.ID, p.EntryDate, p.Kind, p.Title, p.Body, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound(p.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}
	return j, nil
}

// Delete a journal entry by ID
func (s *entriessrvc) Delete(ctx context.Context, p *entries.DeletePayload) error {
	log.Printf(ctx, "entries.delete id=%d", p.ID)

	userID, err := userFromContext(ctx)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}

	const q = `DELETE FROM entries WHERE id = $1 AND user_id = $2`

	// DELETE は対象が無くてもエラーにならないので、消えた行数で 404 を判定する。
	// 他人のものも 0 行で、存在しない id と同じ扱いになる。
	tag, err := s.db.Exec(ctx, q, p.ID, userID)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound(p.ID)
	}
	return nil
}
