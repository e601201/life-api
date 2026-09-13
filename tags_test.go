package life

// tags の一覧・改名・削除。entries と同じく実際の PostgreSQL に対して流す
// （LEFT JOIN の件数、UNIQUE 違反、CASCADE が確かめたいもののため）。

import (
	"context"
	"slices"
	"testing"

	entries "github.com/e601201/life-api/gen/entries"
	tags "github.com/e601201/life-api/gen/tags"
)

// newTagsService は entries と tags のサービスと、認証済みユーザの ctx を返す。
// タグは entries 経由でしか作れないので、両方要る。
func newTagsService(t *testing.T) (tags.Service, entries.Service, context.Context) {
	t.Helper()
	esvc, ctx := newService(t)
	return NewTags(testPool, testAuth), esvc, ctx
}

// mustCreateTagged はタグ付きの記録を 1 件作る。
func mustCreateTagged(t *testing.T, ctx context.Context, svc entries.Service, title string, tagNames ...string) *entries.CreateResult {
	t.Helper()
	res, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01", Kind: "til", Title: title, Tags: tagNames,
	})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return res
}

// findTag は一覧から名前でタグを探す。
func findTag(t *testing.T, list []*tags.Tag, name string) *tags.Tag {
	t.Helper()
	for _, tg := range list {
		if tg.Name == name {
			return tg
		}
	}
	t.Fatalf("tag %q が一覧に無い: %v", name, list)
	return nil
}

func TestTagsListEmpty(t *testing.T) {
	tsvc, _, ctx := newTagsService(t)

	got, err := tsvc.List(ctx, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("list = %v, want 空スライス", got)
	}
}

func TestTagsListWithCounts(t *testing.T) {
	tsvc, esvc, ctx := newTagsService(t)

	mustCreateTagged(t, ctx, esvc, "1", "go", "aws")
	mustCreateTagged(t, ctx, esvc, "2", "go")
	created := mustCreateTagged(t, ctx, esvc, "3", "goa")
	// goa を外して、紐付けの無いタグも 0 件で出ることを見る。
	if _, err := esvc.Update(ctx, &entries.UpdatePayload{
		ID: *created.ID, EntryDate: "2026-09-01", Kind: "til", Title: "3",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := tsvc.List(ctx, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 名前順。件数は紐付いている記録の数。
	type row struct {
		name  string
		count int
	}
	var rows []row
	for _, tg := range got {
		rows = append(rows, row{tg.Name, tg.EntryCount})
	}
	want := []row{{"aws", 1}, {"go", 2}, {"goa", 0}}
	if !slices.Equal(rows, want) {
		t.Errorf("list = %v, want %v", rows, want)
	}
}

func TestTagsListScopedToUser(t *testing.T) {
	tsvc, esvc, alice := newTagsService(t)
	bob := asOtherUser(t, alice)

	mustCreateTagged(t, alice, esvc, "alice", "go")
	mustCreateTagged(t, bob, esvc, "bob", "rust")

	got, err := tsvc.List(bob, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Name != "rust" {
		t.Errorf("bob の一覧 = %v, want [rust]", got)
	}
}

func TestTagsUpdate(t *testing.T) {
	tsvc, esvc, ctx := newTagsService(t)

	e1 := mustCreateTagged(t, ctx, esvc, "1", "golang")
	e2 := mustCreateTagged(t, ctx, esvc, "2", "golang", "aws")
	list, err := tsvc.List(ctx, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	golang := findTag(t, list, "golang")

	got, err := tsvc.Update(ctx, &tags.UpdatePayload{ID: golang.ID, Name: "go"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.ID != golang.ID || got.Name != "go" || got.EntryCount != 2 {
		t.Errorf("update = %+v, want id=%d name=go entry_count=2", got, golang.ID)
	}

	// 改名は紐付いている全ての記録に効く。
	for _, id := range []int64{*e1.ID, *e2.ID} {
		j, err := esvc.Get(ctx, &entries.GetPayload{ID: id})
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		if !slices.Contains(j.Tags, "go") || slices.Contains(j.Tags, "golang") {
			t.Errorf("entry %d tags = %v, golang → go に変わっていること", id, j.Tags)
		}
	}
}

func TestTagsUpdateConflict(t *testing.T) {
	tsvc, esvc, ctx := newTagsService(t)

	mustCreateTagged(t, ctx, esvc, "1", "go", "golang")
	list, err := tsvc.List(ctx, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// 既にある名前への改名は 409。統合はしない。
	_, err = tsvc.Update(ctx, &tags.UpdatePayload{ID: findTag(t, list, "golang").ID, Name: "go"})
	requireErrorName(t, err, "conflict")
}

func TestTagsUpdateOtherUsersTagIsNotFound(t *testing.T) {
	tsvc, esvc, alice := newTagsService(t)
	bob := asOtherUser(t, alice)

	mustCreateTagged(t, alice, esvc, "alice", "go")
	list, err := tsvc.List(alice, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	id := findTag(t, list, "go").ID

	_, err = tsvc.Update(bob, &tags.UpdatePayload{ID: id, Name: "hijacked"})
	requireErrorName(t, err, "not_found")
	err = tsvc.Delete(bob, &tags.DeletePayload{ID: id})
	requireErrorName(t, err, "not_found")

	// alice のタグは何も変わっていない。
	list, err = tsvc.List(alice, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "go" {
		t.Errorf("alice の一覧 = %v, want [go]", list)
	}
}

func TestTagsDelete(t *testing.T) {
	tsvc, esvc, ctx := newTagsService(t)

	created := mustCreateTagged(t, ctx, esvc, "1", "go", "aws")
	list, err := tsvc.List(ctx, &tags.ListPayload{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if err := tsvc.Delete(ctx, &tags.DeletePayload{ID: findTag(t, list, "aws").ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 記録は残り、消したタグだけ外れる。
	j, err := esvc.Get(ctx, &entries.GetPayload{ID: *created.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if want := []string{"go"}; !slices.Equal(j.Tags, want) {
		t.Errorf("tags = %v, want %v", j.Tags, want)
	}

	// 無い id は 404。
	err = tsvc.Delete(ctx, &tags.DeletePayload{ID: 999})
	requireErrorName(t, err, "not_found")
}

func TestTagsWithoutUser(t *testing.T) {
	tsvc, _, _ := newTagsService(t)

	noUser := context.Background()
	if _, err := tsvc.List(noUser, &tags.ListPayload{}); err == nil {
		t.Error("ユーザの無い ctx で list が通っている")
	}
	if _, err := tsvc.Update(noUser, &tags.UpdatePayload{ID: 1, Name: "x"}); err == nil {
		t.Error("ユーザの無い ctx で update が通っている")
	}
	if err := tsvc.Delete(noUser, &tags.DeletePayload{ID: 1}); err == nil {
		t.Error("ユーザの無い ctx で delete が通っている")
	}
}
