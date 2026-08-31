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

// copyJournal returns a shallow copy so that callers cannot mutate the store.
func copyJournal(j *entries.Journal) *entries.Journal {
	c := *j
	return &c
}

// Create a new journal entry
func (s *entriessrvc) Create(ctx context.Context, p *entries.Journal) (*entries.Journal, error) {
	log.Printf(ctx, "entries.create")

	s.mu.Lock()
	defer s.mu.Unlock()

	j := copyJournal(p)
	id := s.nextID
	s.nextID++
	ts := now()
	// id は自動採番、タイムスタンプはサーバー側で付ける（クライアントの値は無視）
	j.ID = &id
	j.CreatedAt = &ts
	j.UpdatedAt = &ts

	s.store[id] = j
	return copyJournal(j), nil
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
func (s *entriessrvc) Get(ctx context.Context, id int64) (*entries.Journal, error) {
	log.Printf(ctx, "entries.get id=%d", id)

	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.store[id]
	if !ok {
		return nil, notFound(id)
	}
	return copyJournal(j), nil
}

// Update a journal entry by ID
func (s *entriessrvc) Update(ctx context.Context, p *entries.Journal) (*entries.Journal, error) {
	log.Printf(ctx, "entries.update")

	s.mu.Lock()
	defer s.mu.Unlock()

	// id は PUT /entries/{id} のパスパラメータから必ず入る
	id := *p.ID
	cur, ok := s.store[id]
	if !ok {
		return nil, notFound(id)
	}

	j := copyJournal(p)
	ts := now()
	// created_at は作成時の値を維持し、updated_at だけ進める
	j.CreatedAt = cur.CreatedAt
	j.UpdatedAt = &ts

	s.store[id] = j
	return copyJournal(j), nil
}

// Delete a journal entry by ID
func (s *entriessrvc) Delete(ctx context.Context, id int64) error {
	log.Printf(ctx, "entries.delete id=%d", id)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.store[id]; !ok {
		return notFound(id)
	}
	delete(s.store, id)
	return nil
}
