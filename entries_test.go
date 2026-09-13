package life

// entries の CRUD は実際の PostgreSQL に対して流す。検証したいものが SQL 側
// （date へのキャスト、RETURNING、pgx.ErrNoRows、インデックスに合わせた並び）に
// 寄っているため、DB をモックすると確かめたい部分が残らない。
//
// 接続先は TEST_DATABASE_URL で渡す。未設定なら skip するので、DB の無い環境でも
// go test ./... は通る。DATABASE_URL ではなく専用の変数を見るのは、テストが
// entries / users / tags テーブルを空にするため。開発用の DB を取り違えて消さないようにしている。
//
//	docker compose exec db psql -U life -d postgres -c 'CREATE DATABASE life_test'
//	TEST_DATABASE_URL='postgres://life:life@localhost:5432/life_test?sslmode=disable' go test ./...

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"slices"

	"github.com/e601201/life-api/db"
	entries "github.com/e601201/life-api/gen/entries"
	"github.com/jackc/pgx/v5/pgxpool"
	goa "goa.design/goa/v3/pkg"
)

// testPool は TEST_DATABASE_URL が指す DB への接続。未設定なら nil のままで、
// 各テストは requireDB で skip する。testDBURL は、別の接続を張りたいテスト
// （health の異常系）が使う。
var (
	testPool  *pgxpool.Pool
	testDBURL string
)

// testSecret はテスト用の署名鍵。本物の鍵ではなく、minSecretLen を満たす固定値。
const testSecret = "test-secret-for-life-api-unit-tests-only"

// testAuth はテスト全体で共有する Auth。時刻を動かしたいテストは newTestAuth で別に作る。
var testAuth = mustNewAuth()

func mustNewAuth() *Auth {
	a, err := NewAuth(testSecret)
	if err != nil {
		panic(err)
	}
	return a
}

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		os.Exit(m.Run())
	}

	// スキーマは本番と同じ経路（golang-migrate）で作る。テストの中に手書きの
	// CREATE TABLE を置くと、マイグレーションとの食い違いがテストをすり抜ける。
	if err := db.Up(url); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	testPool = pool
	testDBURL = url

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// requireDB は DB を使うテストの入口。接続先が渡されていなければ skip する。
func requireDB(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("TEST_DATABASE_URL が未設定のためスキップ")
	}
}

// resetTables はテーブルを空にし、id の採番も 1 に戻す。テストごとに同じ前提から
// 始められる。FK で互いに参照しているので、まとめて 1 文で消す。
func resetTables(t *testing.T) context.Context {
	t.Helper()
	requireDB(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, "TRUNCATE entries, users, tags, entry_tags RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return ctx
}

// insertUser は users に 1 行入れて id を返す。entries のテストは「誰か」が居ないと
// 作成できない（user_id の FK）ので、users サービスを経由せずに直接入れる。
// password_hash はログインしないので何でもよい。
func insertUser(t *testing.T, ctx context.Context, email string) int64 {
	t.Helper()
	const q = `INSERT INTO users (email, password_hash) VALUES ($1, 'unused') RETURNING id`
	var id int64
	if err := testPool.QueryRow(ctx, q, email).Scan(&id); err != nil {
		t.Fatalf("insert user %q: %v", email, err)
	}
	return id
}

// newService はテスト用の entries サービスと、認証済みユーザを載せた ctx を返す。
// HTTP 経由なら JWTAuth が ctx にユーザ ID を入れるところを、ここでは直接入れる。
func newService(t *testing.T) (entries.Service, context.Context) {
	t.Helper()
	ctx := resetTables(t)
	uid := insertUser(t, ctx, "entries@example.com")
	return NewEntries(testPool, testAuth), ContextWithUserID(ctx, uid)
}

// asOtherUser は別のユーザを 1 人足して、その人として呼ぶための ctx を返す。
// スコープ（他人の記録が見えない・触れない）を確かめるテストで使う。
func asOtherUser(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	uid := insertUser(t, ctx, "other@example.com")
	return ContextWithUserID(ctx, uid)
}

// mustCreate は前提となる 1 件を作る。作成そのものの検証は TestCreate でやるので、
// ここで失敗したらテストを続ける意味がない。
func mustCreate(t *testing.T, ctx context.Context, svc entries.Service, date, title string) *entries.CreateResult {
	t.Helper()
	res, err := svc.Create(ctx, &entries.CreatePayload{EntryDate: date, Kind: "til", Title: title})
	if err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return res
}

// setCreatedAt は created_at を既知の値に置き換える。DEFAULT now() の値は選べないので、
// 「更新しても維持される」「精度が落ちない」を決定的に確かめたいときに使う。
func setCreatedAt(t *testing.T, ctx context.Context, id int64, ts string) {
	t.Helper()
	const q = `UPDATE entries SET created_at = $2::timestamptz WHERE id = $1`
	if _, err := testPool.Exec(ctx, q, id, ts); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
}

// derefString / derefInt64 は失敗メッセージ用にポインタの中身を取り出す。
// %v にポインタをそのまま渡すとアドレスが出てしまい、何が違ったのか読めない。
func derefString(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func derefInt64(p *int64) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprint(*p)
}

// requireErrorName は design で宣言したエラーが返っていることを確かめ、その
// ServiceError を返す。ここが素の error だと、HTTP では宣言したステータス
// （404 / 503）ではなく 500 になる。
func requireErrorName(t *testing.T, err error, want string) *goa.ServiceError {
	t.Helper()
	var serr *goa.ServiceError
	if !errors.As(err, &serr) {
		t.Fatalf("goa のエラーではない: %v", err)
	}
	if serr.Name != want {
		t.Fatalf("error name = %q, want %q", serr.Name, want)
	}
	return serr
}

func TestCreate(t *testing.T) {
	svc, ctx := newService(t)

	body := "本文"
	res, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01",
		Kind:      "til",
		Title:     "作成",
		Body:      &body,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// id は IDENTITY の採番。RESTART IDENTITY の直後なので 1 から始まる。
	if res.ID == nil || *res.ID != 1 {
		t.Errorf("id = %s, want 1", derefInt64(res.ID))
	}
	if res.Location != "/entries/1" {
		t.Errorf("location = %q, want %q", res.Location, "/entries/1")
	}
	// タイムスタンプは DEFAULT now() 任せなので、値ではなく入っていることを見る。
	if res.CreatedAt == nil || res.UpdatedAt == nil {
		t.Errorf("created_at = %s, updated_at = %s, どちらも入っていること",
			derefString(res.CreatedAt), derefString(res.UpdatedAt))
	}
	// user_id はリクエストではなく ctx（HTTP なら JWT の sub）から入る。
	wantUser, _ := UserIDFromContext(ctx)
	if res.UserID == nil || *res.UserID != wantUser {
		t.Errorf("user_id = %s, want %d", derefInt64(res.UserID), wantUser)
	}
	if res.EntryDate != "2026-09-01" || res.Kind != "til" || res.Title != "作成" {
		t.Errorf("entry_date/kind/title = %q/%q/%q, want %q/%q/%q",
			res.EntryDate, res.Kind, res.Title, "2026-09-01", "til", "作成")
	}
	if res.Body == nil || *res.Body != "本文" {
		t.Errorf("body = %q, want %q", derefString(res.Body), "本文")
	}

	// 戻り値だけでなく、行として残っていることを読み直して確かめる。
	got, err := svc.Get(ctx, &entries.GetPayload{ID: *res.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "作成" {
		t.Errorf("get title = %q, want %q", got.Title, "作成")
	}
}

func TestWithoutUser(t *testing.T) {
	svc, ctx := newService(t)
	created := mustCreate(t, ctx, svc, "2026-09-01", "誰かの記録")

	// 認証を経ていない ctx ではどのメソッドも動かない。HTTP では JWTAuth が先に 401 を
	// 返すのでここには来ないが、サービスを直接呼ぶ経路で全件が見えたり、
	// user_id の無い行が入ったりするのを防ぐ。
	noUser := context.Background()
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{"create", func() error {
			_, err := svc.Create(noUser, &entries.CreatePayload{EntryDate: "2026-09-01", Kind: "til", Title: "ユーザなし"})
			return err
		}},
		{"list", func() error { _, err := svc.List(noUser, &entries.ListPayload{Limit: 10}); return err }},
		{"get", func() error { _, err := svc.Get(noUser, &entries.GetPayload{ID: *created.ID}); return err }},
		{"update", func() error {
			_, err := svc.Update(noUser, &entries.UpdatePayload{ID: *created.ID, EntryDate: "2026-09-01", Kind: "til", Title: "ユーザなし"})
			return err
		}},
		{"delete", func() error { return svc.Delete(noUser, &entries.DeletePayload{ID: *created.ID}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil {
				t.Errorf("ユーザの無い ctx で %s が通っている", tt.name)
			}
		})
	}
}

func TestEntriesRequireUserID(t *testing.T) {
	_, ctx := newService(t)

	// 000003 で user_id を NOT NULL にした。API を通らない経路でも「誰のものでもない記録」が
	// 入らないこと（入ると誰からも見えない行になる）。
	const q = `INSERT INTO entries (user_id, entry_date, kind, title) VALUES (NULL, '2026-09-01', 'til', 'orphan')`
	if _, err := testPool.Exec(ctx, q); err == nil {
		t.Fatal("user_id が NULL の行を入れられている")
	}
}

func TestListScopedToUser(t *testing.T) {
	svc, alice := newService(t)
	bob := asOtherUser(t, alice)

	a1 := mustCreate(t, alice, svc, "2026-09-01", "alice 1")
	b1 := mustCreate(t, bob, svc, "2026-09-02", "bob 1")
	a2 := mustCreate(t, alice, svc, "2026-09-03", "alice 2")

	// それぞれ自分の分だけ。並びは記録日の新しい順のまま。
	for _, tt := range []struct {
		name string
		ctx  context.Context
		want []int64
	}{
		{name: "alice", ctx: alice, want: []int64{*a2.ID, *a1.ID}},
		{name: "bob", ctx: bob, want: []int64{*b1.ID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.List(tt.ctx, &entries.ListPayload{Limit: 10})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i, id := range tt.want {
				if got[i].ID == nil || *got[i].ID != id {
					t.Errorf("list[%d].id = %s, want %d", i, derefInt64(got[i].ID), id)
				}
			}
		})
	}
}

func TestOtherUsersEntryIsNotFound(t *testing.T) {
	svc, alice := newService(t)
	bob := asOtherUser(t, alice)

	created := mustCreate(t, alice, svc, "2026-09-01", "alice の記録")

	// bob からは存在しない id と同じ not_found。403 で分けると id の存在が分かってしまう。
	_, err := svc.Get(bob, &entries.GetPayload{ID: *created.ID})
	requireErrorName(t, err, "not_found")

	_, err = svc.Update(bob, &entries.UpdatePayload{
		ID: *created.ID, EntryDate: "2026-09-02", Kind: "diary", Title: "bob が書き換え",
	})
	requireErrorName(t, err, "not_found")

	err = svc.Delete(bob, &entries.DeletePayload{ID: *created.ID})
	requireErrorName(t, err, "not_found")

	// alice の記録は何も変わっていない。
	got, err := svc.Get(alice, &entries.GetPayload{ID: *created.ID})
	if err != nil {
		t.Fatalf("get as alice: %v", err)
	}
	if got.Title != "alice の記録" || got.EntryDate != "2026-09-01" {
		t.Errorf("title/entry_date = %q/%q, 他人の update が効いている", got.Title, got.EntryDate)
	}
}

func TestCreateWithoutBody(t *testing.T) {
	svc, ctx := newService(t)

	// body は任意。省略したら空文字ではなく NULL のまま返る。
	res, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01",
		Kind:      "diary",
		Title:     "本文なし",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.Body != nil {
		t.Errorf("body = %q, want nil", *res.Body)
	}
	// tags も省略できる。レスポンスでは nil（JSON の null）ではなく空スライスで返る。
	if res.Tags == nil || len(res.Tags) != 0 {
		t.Errorf("tags = %v, want 空スライス", res.Tags)
	}
}

// tagNames は失敗メッセージ用に tags テーブルの中身を (user_id:name) の一覧にする。
func tagNames(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := testPool.Query(ctx, `SELECT user_id, name FROM tags ORDER BY user_id, name`)
	if err != nil {
		t.Fatalf("select tags: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid int64
		var name string
		if err := rows.Scan(&uid, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, fmt.Sprintf("%d:%s", uid, name))
	}
	return out
}

func TestCreateWithTags(t *testing.T) {
	svc, ctx := newService(t)

	// 順不同・重複ありで渡しても、名前順・重複なしで返る。
	res, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01",
		Kind:      "til",
		Title:     "タグ付き",
		Tags:      []string{"goa", "go", "goa", "aws"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := []string{"aws", "go", "goa"}
	if !slices.Equal(res.Tags, want) {
		t.Errorf("tags = %v, want %v", res.Tags, want)
	}

	// 読み直しても同じ。tags テーブルには 3 行だけ（重複分は作られない）。
	got, err := svc.Get(ctx, &entries.GetPayload{ID: *res.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Equal(got.Tags, want) {
		t.Errorf("get tags = %v, want %v", got.Tags, want)
	}
	if names := tagNames(t, ctx); len(names) != 3 {
		t.Errorf("tags テーブル = %v, want 3 行", names)
	}
}

func TestTagsAreReusedAcrossEntries(t *testing.T) {
	svc, ctx := newService(t)

	// 同じ名前を別の記録に付けても、タグの行は増えず紐付けだけ増える。
	for _, title := range []string{"1 件目", "2 件目"} {
		if _, err := svc.Create(ctx, &entries.CreatePayload{
			EntryDate: "2026-09-01", Kind: "til", Title: title, Tags: []string{"go"},
		}); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}
	if names := tagNames(t, ctx); len(names) != 1 {
		t.Errorf("tags テーブル = %v, want 1 行", names)
	}
	var links int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM entry_tags`).Scan(&links); err != nil {
		t.Fatalf("count entry_tags: %v", err)
	}
	if links != 2 {
		t.Errorf("entry_tags = %d 行, want 2", links)
	}
}

func TestUpdateReplacesTags(t *testing.T) {
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01", Kind: "til", Title: "更新前", Tags: []string{"go", "aws"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// go を残して aws を外し、goa を足す。
	got, err := svc.Update(ctx, &entries.UpdatePayload{
		ID: *created.ID, EntryDate: "2026-09-01", Kind: "til", Title: "更新後", Tags: []string{"goa", "go"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if want := []string{"go", "goa"}; !slices.Equal(got.Tags, want) {
		t.Errorf("tags = %v, want %v", got.Tags, want)
	}

	// 外した aws のタグ自体は残る（消すのは tags.delete の仕事）。
	if names := tagNames(t, ctx); !slices.Contains(names, "1:aws") {
		t.Errorf("tags テーブル = %v, aws が消えている", names)
	}

	// PUT は全置換なので、tags を省くと全部外れる（body と同じ扱い）。
	got, err = svc.Update(ctx, &entries.UpdatePayload{
		ID: *created.ID, EntryDate: "2026-09-01", Kind: "til", Title: "タグなし",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("tags = %v, want 空スライス", got.Tags)
	}
}

func TestTagsScopedToUser(t *testing.T) {
	svc, alice := newService(t)
	bob := asOtherUser(t, alice)

	// 同じ名前でもユーザが違えば別のタグ（tags_user_id_name_key は user_id 込み）。
	for _, ctx := range []context.Context{alice, bob} {
		if _, err := svc.Create(ctx, &entries.CreatePayload{
			EntryDate: "2026-09-01", Kind: "til", Title: "同じ名前", Tags: []string{"go"},
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if names := tagNames(t, alice); !slices.Equal(names, []string{"1:go", "2:go"}) {
		t.Errorf("tags テーブル = %v, want [1:go 2:go]", names)
	}
}

func TestDeleteEntryKeepsTags(t *testing.T) {
	svc, ctx := newService(t)

	created, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01", Kind: "til", Title: "消す", Tags: []string{"go"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, &entries.DeletePayload{ID: *created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 紐付けは CASCADE で消え、タグは残る。
	var links int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM entry_tags`).Scan(&links); err != nil {
		t.Fatalf("count entry_tags: %v", err)
	}
	if links != 0 {
		t.Errorf("entry_tags = %d 行, want 0", links)
	}
	if names := tagNames(t, ctx); len(names) != 1 {
		t.Errorf("tags テーブル = %v, want 1 行", names)
	}
}

func TestListEmpty(t *testing.T) {
	svc, ctx := newService(t)

	got, err := svc.List(ctx, &entries.ListPayload{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// nil を返すと JSON が null になるため、空でもスライスであること。
	if got == nil {
		t.Fatal("list = nil, want 空スライス")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestListOrder(t *testing.T) {
	svc, ctx := newService(t)

	older := mustCreate(t, ctx, svc, "2026-08-30", "古い日付")
	sameDayFirst := mustCreate(t, ctx, svc, "2026-09-01", "同じ日付の先")
	sameDayLast := mustCreate(t, ctx, svc, "2026-09-01", "同じ日付の後")

	got, err := svc.List(ctx, &entries.ListPayload{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// entries_entry_date_idx と同じ並び（entry_date DESC, id DESC）。
	// 同じ日のエントリは、後から入れたものが先に来る。
	want := []int64{*sameDayLast.ID, *sameDayFirst.ID, *older.ID}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID == nil || *got[i].ID != id {
			t.Errorf("list[%d].id = %s, want %d", i, derefInt64(got[i].ID), id)
		}
	}
}

func TestGetNotFound(t *testing.T) {
	svc, ctx := newService(t)

	_, err := svc.Get(ctx, &entries.GetPayload{ID: 999})
	requireErrorName(t, err, "not_found")
}

func TestUpdate(t *testing.T) {
	svc, ctx := newService(t)

	created := mustCreate(t, ctx, svc, "2026-09-01", "更新前")

	// created_at を既知の値に置いてから更新し、維持されることを一致で確かめる。
	const createdAt = "2026-08-31T01:02:03.456789Z"
	setCreatedAt(t, ctx, *created.ID, createdAt)

	body := "追記"
	got, err := svc.Update(ctx, &entries.UpdatePayload{
		ID:        *created.ID,
		EntryDate: "2026-09-02",
		Kind:      "diary",
		Title:     "更新後",
		Body:      &body,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if got.EntryDate != "2026-09-02" || got.Kind != "diary" || got.Title != "更新後" {
		t.Errorf("entry_date/kind/title = %q/%q/%q, want %q/%q/%q",
			got.EntryDate, got.Kind, got.Title, "2026-09-02", "diary", "更新後")
	}
	if derefString(got.CreatedAt) != createdAt {
		t.Errorf("created_at = %q, want %q（作成時の値を維持する）", derefString(got.CreatedAt), createdAt)
	}
	if got.UpdatedAt == nil || *got.UpdatedAt == createdAt {
		t.Errorf("updated_at = %q, 更新時刻に進んでいない", derefString(got.UpdatedAt))
	}
}

func TestUpdateClearsBody(t *testing.T) {
	svc, ctx := newService(t)

	body := "消される本文"
	created, err := svc.Create(ctx, &entries.CreatePayload{
		EntryDate: "2026-09-01",
		Kind:      "til",
		Title:     "本文あり",
		Body:      &body,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// PUT は全置換なので、body を省いた更新は NULL に戻す（部分更新ではない）。
	got, err := svc.Update(ctx, &entries.UpdatePayload{
		ID:        *created.ID,
		EntryDate: "2026-09-01",
		Kind:      "til",
		Title:     "本文あり",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Body != nil {
		t.Errorf("body = %q, want nil", *got.Body)
	}
}

func TestUpdateNotFound(t *testing.T) {
	svc, ctx := newService(t)

	// UPDATE は対象が無いと RETURNING が 1 行も返さず、ErrNoRows になる。
	_, err := svc.Update(ctx, &entries.UpdatePayload{
		ID:        999,
		EntryDate: "2026-09-01",
		Kind:      "til",
		Title:     "無い",
	})
	requireErrorName(t, err, "not_found")
}

func TestDelete(t *testing.T) {
	svc, ctx := newService(t)

	created := mustCreate(t, ctx, svc, "2026-09-01", "削除")
	if err := svc.Delete(ctx, &entries.DeletePayload{ID: *created.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := svc.Get(ctx, &entries.GetPayload{ID: *created.ID})
	if err == nil {
		t.Fatal("削除したのに取得できている")
	}
	requireErrorName(t, err, "not_found")
}

func TestDeleteNotFound(t *testing.T) {
	svc, ctx := newService(t)

	// DELETE は対象が無くてもエラーにならないため、消えた行数で判定している。
	err := svc.Delete(ctx, &entries.DeletePayload{ID: 999})
	requireErrorName(t, err, "not_found")
}

func TestTimestampPrecision(t *testing.T) {
	svc, ctx := newService(t)

	created := mustCreate(t, ctx, svc, "2026-09-01", "精度")

	// now() の小数部がたまたま 0 になる可能性を避けるため、既知の値を入れて読み直す。
	// 秒精度（time.RFC3339）で組み立てていると、ここでマイクロ秒が落ちる。
	const want = "2026-09-01T10:00:00.123456Z"
	setCreatedAt(t, ctx, *created.ID, want)

	got, err := svc.Get(ctx, &entries.GetPayload{ID: *created.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if derefString(got.CreatedAt) != want {
		t.Errorf("created_at = %q, want %q", derefString(got.CreatedAt), want)
	}
}

func TestListPagination(t *testing.T) {
	svc, ctx := newService(t)

	// 12 件。entry_date を 1 日ずつずらして、並びを日付だけで決まるようにする。
	ids := make([]int64, 0, 12)
	for i := 1; i <= 12; i++ {
		res := mustCreate(t, ctx, svc, fmt.Sprintf("2026-09-%02d", i), fmt.Sprintf("%d 件目", i))
		ids = append(ids, *res.ID)
	}

	// 並びは entry_date の降順なので、最後に入れた 09-12 が先頭に来る。
	newestFirst := make([]int64, len(ids))
	for i, id := range ids {
		newestFirst[len(ids)-1-i] = id
	}

	tests := []struct {
		name   string
		limit  int
		offset int
		want   []int64
	}{
		// 既定値の 10 は DSL の Default で入る。ここでは同じ値を明示して、
		// 「10 件で切れる」ことだけを見る。
		{name: "1 ページ目", limit: 10, offset: 0, want: newestFirst[:10]},
		{name: "2 ページ目は残りだけ", limit: 10, offset: 10, want: newestFirst[10:]},
		{name: "途中から少しだけ", limit: 3, offset: 2, want: newestFirst[2:5]},
		{name: "範囲を越えたら空", limit: 10, offset: 100, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.List(ctx, &entries.ListPayload{Limit: tt.limit, Offset: tt.offset})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got == nil {
				t.Fatal("list = nil, want 空スライス")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i, id := range tt.want {
				if got[i].ID == nil || *got[i].ID != id {
					t.Errorf("list[%d].id = %s, want %d", i, derefInt64(got[i].ID), id)
				}
			}
		})
	}
}

func TestHealthCheck(t *testing.T) {
	requireDB(t)

	got, err := NewHealth(testPool).Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if got != "server OK!" {
		t.Errorf("check = %q, want %q", got, "server OK!")
	}
}

func TestHealthCheckDatabaseDown(t *testing.T) {
	requireDB(t)

	// 閉じたプールは Ping が必ず失敗する。DB そのものを落とさずに
	// 「繋がらない状態」を作れるので、他のテストに影響しない。
	ctx := context.Background()
	pool, err := db.Connect(ctx, testDBURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	pool.Close()

	_, err = NewHealth(pool).Check(ctx)
	if err == nil {
		t.Fatal("DB に繋がらないのに OK を返している")
	}
	serr := requireErrorName(t, err, "service_unavailable")

	// 接続先やユーザ名が混ざっていないこと。ここはそのままクライアントに返る。
	if serr.Message != "database unavailable" {
		t.Errorf("message = %q, want %q（詳細はログにだけ残す）", serr.Message, "database unavailable")
	}
}
