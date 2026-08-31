package life

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	entries "github.com/e601201/life-api/gen/entries"
	"goa.design/clue/log"
)

// entries service のインメモリ実装。DB 導入までのつなぎで、
// プロセスを再起動するとデータは消える。
type entriessrvc struct {
	mu     sync.Mutex
	nextID int64
	store  map[int64]*entries.Journal
}

// NewEntries returns the entries service implementation.
func NewEntries() entries.Service {
	return &entriessrvc{
		nextID: 1,
		store:  make(map[int64]*entries.Journal),
	}
}

// now returns the current UTC time in RFC 3339 format.
func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// notFound builds the not_found error declared in the design.
func notFound(id int64) error {
	return entries.MakeNotFound(fmt.Errorf("entry %d not found", id))
}

// copyJournal returns a shallow copy of a stored journal.
// ポインタフィールドは共有されたままだが、現状の呼び出し側は読み取りのみ。
// DB 実装（#38）で entries.go ごと置き換える。
func copyJournal(j *entries.Journal) *entries.Journal {
	c := *j
	return &c
}

// journalFromRequest builds a Journal from the client-writable fields.
// payload とポインタを共有しないよう、値をコピーして詰める。
func journalFromRequest(entryDate, kind, title string, body *string) *entries.Journal {
	j := &entries.Journal{
		EntryDate: entryDate,
		Kind:      kind,
		Title:     title,
	}
	if body != nil {
		b := *body
		j.Body = &b
	}
	return j
}

// Create a new journal entry
func (s *entriessrvc) Create(ctx context.Context, p *entries.EntryRequest) (*entries.CreateResult, error) {
	log.Printf(ctx, "entries.create")

	s.mu.Lock()
	defer s.mu.Unlock()

	j := journalFromRequest(p.EntryDate, p.Kind, p.Title, p.Body)
	id := s.nextID
	s.nextID++
	created := now()
	updated := created
	// id は自動採番、タイムスタンプはサーバー側で付ける
	j.ID = &id
	j.CreatedAt = &created
	j.UpdatedAt = &updated

	s.store[id] = j
	return &entries.CreateResult{
		Location:  fmt.Sprintf("/entries/%d", id),
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

// List all journal entries
func (s *entriessrvc) List(ctx context.Context) ([]*entries.Journal, error) {
	log.Printf(ctx, "entries.list")

	s.mu.Lock()
	defer s.mu.Unlock()

	res := make([]*entries.Journal, 0, len(s.store))
	for _, j := range s.store {
		res = append(res, copyJournal(j))
	}
	sort.Slice(res, func(i, k int) bool { return *res[i].ID < *res[k].ID })
	return res, nil
}

// Get a journal entry by ID
func (s *entriessrvc) Get(ctx context.Context, p *entries.GetPayload) (*entries.Journal, error) {
	log.Printf(ctx, "entries.get id=%d", p.ID)

	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.store[p.ID]
	if !ok {
		return nil, notFound(p.ID)
	}
	return copyJournal(j), nil
}

// Update a journal entry by ID
func (s *entriessrvc) Update(ctx context.Context, p *entries.UpdatePayload) (*entries.Journal, error) {
	log.Printf(ctx, "entries.update id=%d", p.ID)

	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.store[p.ID]
	if !ok {
		return nil, notFound(p.ID)
	}

	j := journalFromRequest(p.EntryDate, p.Kind, p.Title, p.Body)
	id := p.ID
	ts := now()
	j.ID = &id
	// created_at と user_id は作成時の値を維持し、updated_at だけ進める
	j.CreatedAt = cur.CreatedAt
	j.UserID = cur.UserID
	j.UpdatedAt = &ts

	s.store[p.ID] = j
	return copyJournal(j), nil
}

// Delete a journal entry by ID
func (s *entriessrvc) Delete(ctx context.Context, p *entries.DeletePayload) error {
	log.Printf(ctx, "entries.delete id=%d", p.ID)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.store[p.ID]; !ok {
		return notFound(p.ID)
	}
	delete(s.store, p.ID)
	return nil
}
