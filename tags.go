package life

import (
	"context"
	"errors"
	"fmt"

	tags "github.com/e601201/life-api/gen/tags"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goa.design/clue/log"
)

// tags service の実装。一覧・改名・削除だけで、作成は entries の tags から暗黙に行う
// （entries.go の setTags）。スキーマは db/migrations/000004_create_tags.up.sql。
//
// entries と同じく、全メソッドが JWTAuth の載せたユーザ ID で絞る。他人の id は
// 存在しない id と同じ not_found。
type tagssrvc struct {
	*Auth
	db *pgxpool.Pool
}

// NewTags returns the tags service implementation.
func NewTags(pool *pgxpool.Pool, auth *Auth) tags.Service {
	return &tagssrvc{Auth: auth, db: pool}
}

// tagNotFound builds the not_found error declared in the design.
func tagNotFound(id int64) error {
	return tags.MakeNotFound(fmt.Errorf("tag %d not found", id))
}

// scanTag は (id, name, entry_count) の 1 行を API の Tag に詰め替える。
func scanTag(row pgx.Row) (*tags.Tag, error) {
	var t tags.Tag
	if err := row.Scan(&t.ID, &t.Name, &t.EntryCount); err != nil {
		return nil, err
	}
	return &t, nil
}

// List the authenticated user's tags with entry counts, sorted by name
func (s *tagssrvc) List(ctx context.Context, _ *tags.ListPayload) ([]*tags.Tag, error) {
	log.Printf(ctx, "tags.list")

	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}

	// 件数は LEFT JOIN で数える。紐付けの無いタグも 0 件として出す。
	// 並びは tags_user_id_name_key（user_id, name）がそのまま効く。
	const q = `SELECT t.id, t.name, count(et.entry_id)
	           FROM tags t
	           LEFT JOIN entry_tags et ON et.tag_id = t.id
	           WHERE t.user_id = $1
	           GROUP BY t.id
	           ORDER BY t.name`

	rows, err := s.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	res := []*tags.Tag{}
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
		res = append(res, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return res, nil
}

// Rename a tag (applies to every entry carrying it)
func (s *tagssrvc) Update(ctx context.Context, p *tags.UpdatePayload) (*tags.Tag, error) {
	log.Printf(ctx, "tags.update id=%d", p.ID)

	userID, err := userFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("update tag: %w", err)
	}

	// 名前を変えるだけなので紐付け（entry_tags）は触らない。記録側からは自動で新しい名前に見える。
	// 同じ名前が既にあれば UNIQUE 違反になるので 409 にする。
	const q = `UPDATE tags
	           SET name = $3
	           WHERE id = $1 AND user_id = $2
	           RETURNING id, name, (SELECT count(*) FROM entry_tags WHERE tag_id = tags.id)`

	t, err := scanTag(s.db.QueryRow(ctx, q, p.ID, userID, p.Name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tagNotFound(p.ID)
	}
	if isUniqueViolation(err) {
		return nil, tags.MakeConflict(fmt.Errorf("tag %q already exists", p.Name))
	}
	if err != nil {
		return nil, fmt.Errorf("update tag: %w", err)
	}
	return t, nil
}

// Delete a tag and detach it from every entry
func (s *tagssrvc) Delete(ctx context.Context, p *tags.DeletePayload) error {
	log.Printf(ctx, "tags.delete id=%d", p.ID)

	userID, err := userFromContext(ctx)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}

	// entry_tags の紐付けは ON DELETE CASCADE で一緒に消える。記録そのものは残る。
	const q = `DELETE FROM tags WHERE id = $1 AND user_id = $2`

	tag, err := s.db.Exec(ctx, q, p.ID, userID)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return tagNotFound(p.ID)
	}
	return nil
}
