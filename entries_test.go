package life

// entries の CRUD は実際の PostgreSQL に対して流す。検証したいものが SQL 側
// （date へのキャスト、RETURNING、pgx.ErrNoRows、インデックスに合わせた並び）に
// 寄っているため、DB をモックすると確かめたい部分が残らない。
//
// 接続先は TEST_DATABASE_URL で渡す。未設定なら skip するので、DB の無い環境でも
// go test ./... は通る。DATABASE_URL ではなく専用の変数を見るのは、テストが
// entries テーブルを空にするため。開発用の DB を取り違えて消さないようにしている。
//
//	docker compose exec db psql -U life -d postgres -c 'CREATE DATABASE life_test'
//	TEST_DATABASE_URL='postgres://life:life@localhost:5432/life_test?sslmode=disable' go test ./...

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/e601201/life-api/db"
	entries "github.com/e601201/life-api/gen/entries"
	"github.com/jackc/pgx/v5/pgxpool"
	goa "goa.design/goa/v3/pkg"
)

// testPool は TEST_DATABASE_URL が指す DB への接続。未設定なら nil のままで、
// 各テストは newService で skip する。
var testPool *pgxpool.Pool

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

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// newService はテスト用のサービスを返す。テーブルを空にし、id の採番も 1 に戻すので、
// テストごとに同じ前提から始められる。
func newService(t *testing.T) (entries.Service, context.Context) {
	t.Helper()
	if testPool == nil {
		t.Skip("TEST_DATABASE_URL が未設定のためスキップ")
	}
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, "TRUNCATE entries RESTART IDENTITY"); err != nil {
		t.Fatalf("truncate entries: %v", err)
	}
	return NewEntries(testPool), ctx
}

// mustCreate は前提となる 1 件を作る。作成そのものの検証は TestCreate でやるので、
// ここで失敗したらテストを続ける意味がない。
func mustCreate(t *testing.T, ctx context.Context, svc entries.Service, date, title string) *entries.CreateResult {
	t.Helper()
	res, err := svc.Create(ctx, &entries.EntryRequest{EntryDate: date, Kind: "til", Title: title})
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

// requireNotFound は design で宣言した not_found が返っていることを確かめる。
// ここが素の error だと、HTTP では 404 ではなく 500 になる。
func requireNotFound(t *testing.T, err error) {
	t.Helper()
	var serr *goa.ServiceError
	if !errors.As(err, &serr) {
		t.Fatalf("goa のエラーではない: %v", err)
	}
	if serr.Name != "not_found" {
		t.Fatalf("error name = %q, want %q", serr.Name, "not_found")
	}
}

func TestCreate(t *testing.T) {
	svc, ctx := newService(t)

	body := "本文"
	res, err := svc.Create(ctx, &entries.EntryRequest{
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
	// user_id はリクエストから受け取らないため、W3 で JWT を入れるまでは NULL。
	if res.UserID != nil {
		t.Errorf("user_id = %d, want nil", *res.UserID)
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

func TestCreateWithoutBody(t *testing.T) {
	svc, ctx := newService(t)

	// body は任意。省略したら空文字ではなく NULL のまま返る。
	res, err := svc.Create(ctx, &entries.EntryRequest{
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
}

func TestListEmpty(t *testing.T) {
	svc, ctx := newService(t)

	got, err := svc.List(ctx)
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

	got, err := svc.List(ctx)
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
	requireNotFound(t, err)
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
	created, err := svc.Create(ctx, &entries.EntryRequest{
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
	requireNotFound(t, err)
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
	requireNotFound(t, err)
}

func TestDeleteNotFound(t *testing.T) {
	svc, ctx := newService(t)

	// DELETE は対象が無くてもエラーにならないため、消えた行数で判定している。
	err := svc.Delete(ctx, &entries.DeletePayload{ID: 999})
	requireNotFound(t, err)
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
