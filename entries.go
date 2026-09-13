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

// journalSelect は Journal を組み立てる SELECT。列の順は scanJournal と揃えている。
//
// tags は相関サブクエリで名前の配列にして同じ行に載せる。一覧でも行ごとに 1 回ずつ
// 引くことになるが、1 ページは最大 100 件なので、別クエリで引いて Go 側で突き合わせる
// より読みやすさを取った。COALESCE は「タグ無し」を NULL ではなく空配列にするため。
const journalSelect = `SELECT e.id, e.user_id, e.entry_date, e.kind, e.title, e.body, e.created_at, e.updated_at,
                              COALESCE((SELECT array_agg(t.name ORDER BY t.name)
                                        FROM entry_tags et
                                        JOIN tags t ON t.id = et.tag_id
                                        WHERE et.entry_id = e.id), '{}')
                       FROM entries e`

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
		tags      []string
	)
	// body は NULL を取りうるのでポインタで受ける（NULL なら nil）。user_id は 000003 で
	// NOT NULL にしたが、Journal 側の型がポインタのままなので合わせている。
	if err := row.Scan(&id, &userID, &entryDate, &kind, &title, &body, &createdAt, &updatedAt, &tags); err != nil {
		return nil, err
	}
	// COALESCE で '{}' にしているので nil にはならないはずだが、JSON で null を
	// 出さないよう念のため空スライスに寄せる。
	if tags == nil {
		tags = []string{}
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
		Tags:      tags,
	}, nil
}

// uniqueTags は重複を除いた名前の一覧を返す（順序は保つ）。同じ名前が 2 回あると
// setTags の ON CONFLICT DO UPDATE が同じ行を 2 度触ることになり、PostgreSQL が拒否する。
func uniqueTags(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// setTags は記録のタグを names で置き換える。無いタグはそのユーザのタグとして作る。
//
// 1 文で「名前を upsert して id を集め、その id で紐付けを入れる」までやる。
// ON CONFLICT DO NOTHING だと既存の行が RETURNING に出ないので、DO UPDATE で
// 同じ名前を書き戻して必ず id を返させる。
// 紐付けは一度全部消してから入れ直す（PUT の全置換に合わせる）。
func setTags(ctx context.Context, tx pgx.Tx, userID, entryID int64, names []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM entry_tags WHERE entry_id = $1`, entryID); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	names = uniqueTags(names)
	if len(names) == 0 {
		return nil
	}
	const q = `WITH upserted AS (
	               INSERT INTO tags (user_id, name)
	               SELECT $1, unnest($2::text[])
	               ON CONFLICT (user_id, name) DO UPDATE SET name = EXCLUDED.name
	               RETURNING id
	           )
	           INSERT INTO entry_tags (entry_id, tag_id)
	           SELECT $3, id FROM upserted`
	if _, err := tx.Exec(ctx, q, userID, names, entryID); err != nil {
		return fmt.Errorf("set tags: %w", err)
	}
	return nil
}

// Create a new journal entry
func (s *entriessrvc) Create(ctx context.Context, p *entries.CreatePayload) (*entries.CreateResult, error) {
	log.Printf(ctx, "entries.create")

	// user_id はリクエストではなく JWT から取る（JWTAuth が ctx に載せたもの）。
	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}

	// 記録の INSERT とタグの紐付けは 1 つのトランザクションでやる。途中で落ちたときに
	// タグの無い記録が残らないようにするため。Rollback は Commit 後に呼んでも
	// ErrTxClosed を返すだけなので、defer で無条件に掛けておく。
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// id は IDENTITY、created_at / updated_at は DEFAULT now() に任せる。
	// アプリ側で時刻を作らないので、タスクが複数あってもタイムスタンプの基準がぶれない。
	//
	// $2::date のキャストは、text で送った "YYYY-MM-DD" を date として
	// 解釈させるため。付けないとパラメータの型が決まらず型エラーになる。
	const q = `INSERT INTO entries (user_id, entry_date, kind, title, body)
	           VALUES ($1, $2::date, $3, $4, $5)
	           RETURNING id`

	var id int64
	if err := tx.QueryRow(ctx, q, userID, p.EntryDate, p.Kind, p.Title, p.Body).Scan(&id); err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}
	if err := setTags(ctx, tx, userID, id, p.Tags); err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}

	// タグを含めた形は journalSelect で読み直す（RETURNING の時点では紐付けがまだ無い）。
	j, err := scanJournal(tx.QueryRow(ctx, journalSelect+` WHERE e.id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("create entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
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
		Tags:      j.Tags,
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
	const q = journalSelect + `
	           WHERE e.user_id = $1
	           ORDER BY e.entry_date DESC, e.id DESC
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
	const q = journalSelect + ` WHERE e.id = $1 AND e.user_id = $2`

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

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// created_at と user_id は SET に含めない。作成時の値をそのまま残し、
	// updated_at だけ進める。
	// 対象が無い（存在しない、または他人のもの）なら RETURNING が 1 行も返さず、ErrNoRows になる。
	const q = `UPDATE entries
	           SET entry_date = $2::date, kind = $3, title = $4, body = $5, updated_at = now()
	           WHERE id = $1 AND user_id = $6
	           RETURNING id`

	var id int64
	err = tx.QueryRow(ctx, q, p.ID, p.EntryDate, p.Kind, p.Title, p.Body, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound(p.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}
	// PUT は全置換なので、tags を省いた更新はタグを全部外す（body と同じ扱い）。
	if err := setTags(ctx, tx, userID, id, p.Tags); err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}

	j, err := scanJournal(tx.QueryRow(ctx, journalSelect+` WHERE e.id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
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

	// entry_tags の紐付けは ON DELETE CASCADE で一緒に消える。タグ自体は残る。
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
