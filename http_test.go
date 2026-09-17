package life

// HTTP 経由の認証。Goa が DSL から生成した配線（Authorization ヘッダ → JWTAuth →
// 401 / ctx のユーザ ID → サービス）を、生成コードを実際に通して確かめる。
// サービスを直接呼ぶテストではこの層が抜けるため。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	entries "github.com/e601201/life-api/gen/entries"
	entriessvr "github.com/e601201/life-api/gen/http/entries/server"
	tagssvr "github.com/e601201/life-api/gen/http/tags/server"
	userssvr "github.com/e601201/life-api/gen/http/users/server"
	tags "github.com/e601201/life-api/gen/tags"
	users "github.com/e601201/life-api/gen/users"
	"goa.design/clue/log"
	goahttp "goa.design/goa/v3/http"
)

// logBuffer は RequestLog の出力先。サーバのゴルーチンから書き、テストから読むので鍵を掛ける。
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestServer は users / entries / tags を cmd/life/http.go と同じ生成コードでマウントした
// サーバを立てる。エラーの整形は Goa の既定（宣言したエラーは design のステータス）。
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newTestServerWithLog(t)
	return srv
}

// newTestServerWithLog は newTestServer に加えて、RequestLog（reqlog.go）が書いた
// JSON のログも返す。本番と同じくミドルウェアを最外周に掛けてある。
func newTestServerWithLog(t *testing.T) (*httptest.Server, *logBuffer) {
	t.Helper()
	resetTables(t)

	mux := goahttp.NewMuxer()
	eh := func(_ context.Context, _ http.ResponseWriter, err error) { t.Logf("http error: %v", err) }
	ef := func(ctx context.Context, err error) goahttp.Statuser { return goahttp.NewErrorResponse(ctx, err) }

	usersServer := userssvr.New(users.NewEndpoints(NewUsers(testPool, testAuth)), mux,
		goahttp.RequestDecoder, goahttp.ResponseEncoder, eh, ef)
	userssvr.Mount(mux, usersServer)
	entriesServer := entriessvr.New(entries.NewEndpoints(NewEntries(testPool, testAuth)), mux,
		goahttp.RequestDecoder, goahttp.ResponseEncoder, eh, ef)
	entriessvr.Mount(mux, entriesServer)
	tagsServer := tagssvr.New(tags.NewEndpoints(NewTags(testPool, testAuth)), mux,
		goahttp.RequestDecoder, goahttp.ResponseEncoder, eh, ef)
	tagssvr.Mount(mux, tagsServer)

	logs := &logBuffer{}
	logCtx := log.Context(context.Background(), log.WithOutput(logs), log.WithFormat(log.FormatJSON))
	srv := httptest.NewServer(RequestLog(logCtx)(mux))
	t.Cleanup(srv.Close)
	return srv, logs
}

// call はリクエストを 1 本投げ、レスポンスと JSON のボディを返す。ボディが
// オブジェクトでないとき（一覧の配列、204 の空）は nil を返す。
// authorization が空でなければそのまま Authorization ヘッダに載せる。
func call(t *testing.T, srv *httptest.Server, method, path, authorization string, body any) (*http.Response, map[string]any) {
	t.Helper()
	resp, parsed := callRaw(t, srv, method, path, authorization, body)
	obj, _ := parsed.(map[string]any)
	return resp, obj
}

// callList は一覧（JSON 配列）を返すエンドポイント用。
func callList(t *testing.T, srv *httptest.Server, method, path, authorization string) (*http.Response, []any) {
	t.Helper()
	resp, parsed := callRaw(t, srv, method, path, authorization, nil)
	list, _ := parsed.([]any)
	return resp, list
}

// callRaw はリクエストを投げ、ボディを any にデコードして返す（空なら nil）。
func callRaw(t *testing.T, srv *httptest.Server, method, path, authorization string, body any) (*http.Response, any) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var parsed any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s %s: body is not JSON: %s", method, path, raw)
		}
	}
	return resp, parsed
}

// requireStatus はステータスと、エラーなら name も見る。
func requireStatus(t *testing.T, resp *http.Response, body map[string]any, wantStatus int, wantName string) {
	t.Helper()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status = %d, want %d (body: %v)", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, wantStatus, body)
	}
	if wantName != "" && body["name"] != wantName {
		t.Fatalf("error name = %v, want %q", body["name"], wantName)
	}
}

// login は登録とログインを済ませて Authorization ヘッダの値と登録したユーザの id を返す。
func login(t *testing.T, srv *httptest.Server, email, password string) (authorization string, userID float64) {
	t.Helper()
	creds := map[string]string{"email": email, "password": password}

	resp, body := call(t, srv, http.MethodPost, "/users", "", creds)
	requireStatus(t, resp, body, http.StatusCreated, "")
	userID = body["id"].(float64)

	resp, body = call(t, srv, http.MethodPost, "/users/login", "", creds)
	requireStatus(t, resp, body, http.StatusOK, "")
	return "Bearer " + body["access_token"].(string), userID
}

func TestHTTPEntriesRequireToken(t *testing.T) {
	srv := newTestServer(t)

	// トークンが無い・壊れている場合はどれも 401 unauthorized。Goa の既定（Token を
	// Required にした場合）だとヘッダ無しは 400 になるので、そうなっていないことを見る。
	for _, tt := range []struct {
		name          string
		authorization string
	}{
		{name: "ヘッダ無し", authorization: ""},
		{name: "Bearer だが JWT ではない", authorization: "Bearer garbage"},
		{name: "スキーム無し", authorization: "garbage"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := call(t, srv, http.MethodGet, "/entries", tt.authorization, nil)
			requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")

			resp, body = call(t, srv, http.MethodPost, "/entries", tt.authorization,
				map[string]string{"title": "x", "entry_date": "2026-09-01", "kind": "til"})
			requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")
		})
	}

	// users.me も同じ。
	resp, body := call(t, srv, http.MethodGet, "/users/me", "", nil)
	requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")
}

func TestHTTPRegisterLoginAndCreate(t *testing.T) {
	srv := newTestServer(t)

	// 登録。レスポンスにパスワードに関する項目が無いこと。
	resp, body := call(t, srv, http.MethodPost, "/users", "",
		map[string]string{"email": "alice@example.com", "password": "correct horse"})
	requireStatus(t, resp, body, http.StatusCreated, "")
	for k := range body {
		if k == "password" || k == "password_hash" {
			t.Errorf("register のレスポンスに %q が入っている", k)
		}
	}
	userID := body["id"].(float64)

	// ログイン。OAuth 2.0 のトークンレスポンスの形。
	resp, body = call(t, srv, http.MethodPost, "/users/login", "",
		map[string]string{"email": "alice@example.com", "password": "correct horse"})
	requireStatus(t, resp, body, http.StatusOK, "")
	if body["token_type"] != "Bearer" {
		t.Errorf("token_type = %v, want Bearer", body["token_type"])
	}
	authorization := "Bearer " + body["access_token"].(string)

	// 自分の情報。トークンの sub が登録したユーザを指していること。
	resp, body = call(t, srv, http.MethodGet, "/users/me", authorization, nil)
	requireStatus(t, resp, body, http.StatusOK, "")
	if body["id"] != userID || body["email"] != "alice@example.com" {
		t.Errorf("me = %v/%v, want %v/%q", body["id"], body["email"], userID, "alice@example.com")
	}

	// entries の作成。user_id はボディに無くても JWT から入る。
	resp, body = call(t, srv, http.MethodPost, "/entries", authorization,
		map[string]string{"title": "JWT 経由", "entry_date": "2026-09-01", "kind": "til"})
	requireStatus(t, resp, body, http.StatusCreated, "")
	if body["user_id"] != userID {
		t.Errorf("user_id = %v, want %v", body["user_id"], userID)
	}
	if loc := resp.Header.Get("Location"); loc != "/entries/1" {
		t.Errorf("Location = %q, want /entries/1", loc)
	}

	// 一覧も同じトークンで読める。
	resp, _ = call(t, srv, http.MethodGet, "/entries", authorization, nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("list: status = %d, want 200", resp.StatusCode)
	}
}

func TestHTTPLoginRejectsWrongPassword(t *testing.T) {
	srv := newTestServer(t)
	login(t, srv, "alice@example.com", "correct horse")

	// login は登録時の MinLength を掛けないので、短いパスワードでも 400 ではなく 401。
	for _, password := range []string{"wrong password", "x"} {
		resp, body := call(t, srv, http.MethodPost, "/users/login", "",
			map[string]string{"email": "alice@example.com", "password": password})
		requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")
	}
}

func TestHTTPRegisterConflict(t *testing.T) {
	srv := newTestServer(t)
	login(t, srv, "alice@example.com", "correct horse")

	resp, body := call(t, srv, http.MethodPost, "/users", "",
		map[string]string{"email": "alice@example.com", "password": "another one"})
	requireStatus(t, resp, body, http.StatusConflict, "conflict")
}

func TestHTTPRegisterValidation(t *testing.T) {
	srv := newTestServer(t)

	// DSL の MinLength(8) / Format(FormatEmail) は Goa のデコード段階で 400 になる。
	for _, tt := range []struct {
		name  string
		creds map[string]string
	}{
		{name: "短いパスワード", creds: map[string]string{"email": "alice@example.com", "password": "short"}},
		{name: "email の形をしていない", creds: map[string]string{"email": "not-an-email", "password": "correct horse"}},
		{name: "password 無し", creds: map[string]string{"email": "alice@example.com"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := call(t, srv, http.MethodPost, "/users", "", tt.creds)
			requireStatus(t, resp, body, http.StatusBadRequest, "")
		})
	}
}

func TestHTTPEntriesScopedToUser(t *testing.T) {
	srv := newTestServer(t)
	aliceAuth, aliceID := login(t, srv, "alice@example.com", "correct horse")
	bobAuth, bobID := login(t, srv, "bob@example.com", "battery staple")

	// それぞれのトークンで作った entries の user_id が、それぞれの id になること。
	ids := map[string]string{}
	for _, tt := range []struct {
		name          string
		authorization string
		want          float64
	}{
		{name: "alice", authorization: aliceAuth, want: aliceID},
		{name: "bob", authorization: bobAuth, want: bobID},
	} {
		resp, body := call(t, srv, http.MethodPost, "/entries", tt.authorization,
			map[string]string{"title": tt.name + " の記録", "entry_date": "2026-09-01", "kind": "diary"})
		requireStatus(t, resp, body, http.StatusCreated, "")
		if body["user_id"] != tt.want {
			t.Errorf("user_id = %v, want %v", body["user_id"], tt.want)
		}
		ids[tt.name] = resp.Header.Get("Location")
	}

	// alice の一覧には alice の分だけ。
	resp, list := callList(t, srv, http.MethodGet, "/entries", aliceAuth)
	if resp.StatusCode != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["user_id"] != aliceID {
		t.Errorf("alice の一覧 = %v (status %d), alice の 1 件だけであること", list, resp.StatusCode)
	}

	// alice から bob の id は 404（存在しない id と同じ）。
	bobPath := ids["bob"]
	resp, body := call(t, srv, http.MethodGet, bobPath, aliceAuth, nil)
	requireStatus(t, resp, body, http.StatusNotFound, "not_found")
	resp, body = call(t, srv, http.MethodPut, bobPath, aliceAuth,
		map[string]string{"title": "乗っ取り", "entry_date": "2026-09-01", "kind": "diary"})
	requireStatus(t, resp, body, http.StatusNotFound, "not_found")
	resp, body = call(t, srv, http.MethodDelete, bobPath, aliceAuth, nil)
	requireStatus(t, resp, body, http.StatusNotFound, "not_found")

	// bob 自身からは読める。
	resp, body = call(t, srv, http.MethodGet, bobPath, bobAuth, nil)
	requireStatus(t, resp, body, http.StatusOK, "")
	if body["title"] != "bob の記録" {
		t.Errorf("title = %v, want %q", body["title"], "bob の記録")
	}
}

func TestHTTPTags(t *testing.T) {
	srv := newTestServer(t)
	auth, _ := login(t, srv, "alice@example.com", "correct horse")

	// タグ無しの記録は "tags": [] で返る（キーが消えたり null になったりしない）。
	resp, body := call(t, srv, http.MethodPost, "/entries", auth,
		map[string]any{"title": "タグなし", "entry_date": "2026-09-01", "kind": "til"})
	requireStatus(t, resp, body, http.StatusCreated, "")
	if tagsVal, ok := body["tags"].([]any); !ok || len(tagsVal) != 0 {
		t.Errorf("tags = %v (%T), want []", body["tags"], body["tags"])
	}

	// タグ付きで作る。名前順で返る。
	resp, body = call(t, srv, http.MethodPost, "/entries", auth,
		map[string]any{"title": "タグあり", "entry_date": "2026-09-02", "kind": "til", "tags": []string{"goa", "go"}})
	requireStatus(t, resp, body, http.StatusCreated, "")
	entryPath := resp.Header.Get("Location")
	if got := body["tags"]; !equalJSONStrings(got, "go", "goa") {
		t.Errorf("tags = %v, want [go goa]", got)
	}

	// 一覧に件数付きで出る。
	resp, list := callList(t, srv, http.MethodGet, "/tags", auth)
	if resp.StatusCode != http.StatusOK || len(list) != 2 {
		t.Fatalf("GET /tags: status %d, list = %v", resp.StatusCode, list)
	}
	first := list[0].(map[string]any)
	if first["name"] != "go" || first["entry_count"] != float64(1) {
		t.Errorf("tags[0] = %v, want name=go entry_count=1", first)
	}
	goID := int64(first["id"].(float64))

	// 改名すると記録側でも名前が変わる。
	resp, body = call(t, srv, http.MethodPut, fmt.Sprintf("/tags/%d", goID), auth, map[string]string{"name": "golang"})
	requireStatus(t, resp, body, http.StatusOK, "")
	resp, body = call(t, srv, http.MethodGet, entryPath, auth, nil)
	requireStatus(t, resp, body, http.StatusOK, "")
	if got := body["tags"]; !equalJSONStrings(got, "goa", "golang") {
		t.Errorf("tags after rename = %v, want [goa golang]", got)
	}

	// 既にある名前への改名は 409。
	resp, body = call(t, srv, http.MethodPut, fmt.Sprintf("/tags/%d", goID), auth, map[string]string{"name": "goa"})
	requireStatus(t, resp, body, http.StatusConflict, "conflict")

	// バリデーション: 前後の空白や空文字は 400。
	for _, name := range []string{"", " go", "go "} {
		resp, body = call(t, srv, http.MethodPut, fmt.Sprintf("/tags/%d", goID), auth, map[string]string{"name": name})
		requireStatus(t, resp, body, http.StatusBadRequest, "")
	}

	// 削除すると記録から外れ、記録は残る。
	resp, body = call(t, srv, http.MethodDelete, fmt.Sprintf("/tags/%d", goID), auth, nil)
	requireStatus(t, resp, body, http.StatusNoContent, "")
	resp, body = call(t, srv, http.MethodGet, entryPath, auth, nil)
	requireStatus(t, resp, body, http.StatusOK, "")
	if got := body["tags"]; !equalJSONStrings(got, "goa") {
		t.Errorf("tags after delete = %v, want [goa]", got)
	}

	// /tags も JWT が要る。
	resp, body = call(t, srv, http.MethodGet, "/tags", "", nil)
	requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")
}

// equalJSONStrings は JSON からデコードした []any が want と同じ文字列列かを見る。
func equalJSONStrings(got any, want ...string) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, w := range want {
		if list[i] != w {
			return false
		}
	}
	return true
}

func TestHTTPSearch(t *testing.T) {
	srv := newTestServer(t)
	auth, _ := login(t, srv, "alice@example.com", "correct horse")

	for _, e := range []map[string]any{
		{"title": "Goa 入門", "entry_date": "2026-09-01", "kind": "til", "tags": []string{"go", "goa"}},
		{"title": "AWS の設定", "entry_date": "2026-09-02", "kind": "til", "tags": []string{"aws"}},
		{"title": "goa で JWT", "entry_date": "2026-09-04", "kind": "til", "tags": []string{"go", "goa", "aws"}},
	} {
		resp, body := call(t, srv, http.MethodPost, "/entries", auth, e)
		requireStatus(t, resp, body, http.StatusCreated, "")
	}

	// クエリ文字列で tag（繰り返し）/ q / from / to を渡す。全て AND。
	for _, tt := range []struct {
		name  string
		query string
		want  []string
	}{
		{name: "tag 複数", query: "?tag=go&tag=aws", want: []string{"goa で JWT"}},
		{name: "q（大文字小文字を区別しない）", query: "?q=goa", want: []string{"goa で JWT", "Goa 入門"}},
		{name: "期間", query: "?from=2026-09-02&to=2026-09-03", want: []string{"AWS の設定"}},
		{name: "組み合わせ", query: "?tag=goa&q=JWT&from=2026-09-01", want: []string{"goa で JWT"}},
		{name: "日本語の q", query: "?q=" + url.QueryEscape("入門"), want: []string{"Goa 入門"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, list := callList(t, srv, http.MethodGet, "/entries"+tt.query, auth)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			var titles []string
			for _, j := range list {
				titles = append(titles, j.(map[string]any)["title"].(string))
			}
			if !slices.Equal(titles, tt.want) {
				t.Errorf("titles = %v, want %v", titles, tt.want)
			}
		})
	}

	// 値が空のパラメータ（?q=）は Goa が「未指定」として扱うので、絞り込み無しの 200。
	resp, list := callList(t, srv, http.MethodGet, "/entries?q=", auth)
	if resp.StatusCode != http.StatusOK || len(list) != 3 {
		t.Errorf("?q=: status = %d, len = %d, want 200 で全件", resp.StatusCode, len(list))
	}

	// DSL の制約は Goa のデコード段階で 400 になる。
	for _, tt := range []struct{ name, query string }{
		{name: "from が日付ではない", query: "?from=banana"},
		{name: "q が長すぎる", query: "?q=" + strings.Repeat("a", 101)},
		{name: "tag が空文字", query: "?tag="},
		{name: "tag の前後に空白", query: "?tag=" + url.QueryEscape(" go")},
		{name: "limit が範囲外", query: "?limit=0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := call(t, srv, http.MethodGet, "/entries"+tt.query, auth, nil)
			requireStatus(t, resp, body, http.StatusBadRequest, "")
		})
	}
}

func TestHTTPRequestLogUserID(t *testing.T) {
	srv, logs := newTestServerWithLog(t)
	authorization, userID := login(t, srv, "alice@example.com", "correct horse")

	// 認証済みのリクエストには JWT の sub が user_id として付く。
	resp, body := call(t, srv, http.MethodGet, "/users/me", authorization, nil)
	requireStatus(t, resp, body, http.StatusOK, "")
	// 401 には付かない。
	resp, body = call(t, srv, http.MethodGet, "/entries", "", nil)
	requireStatus(t, resp, body, http.StatusUnauthorized, "unauthorized")

	out := logs.String()
	lines := requestLines(t, out)
	// 登録・ログイン・me・entries の 4 本、それぞれ 1 行。
	if len(lines) != 4 {
		t.Fatalf("request の行数 = %d, want 4:\n%s", len(lines), out)
	}
	for i, want := range []struct {
		path   string
		status float64
		user   any // nil なら user_id が無いこと
	}{
		{path: "/users", status: 201},
		{path: "/users/login", status: 200}, // ログインは JWTAuth を通らないので user_id は無い
		{path: "/users/me", status: 200, user: userID},
		{path: "/entries", status: 401},
	} {
		l := lines[i]
		if l["path"] != want.path || l["status"] != want.status {
			t.Errorf("%d 本目: path / status = %v / %v, want %s / %v", i+1, l["path"], l["status"], want.path, want.status)
		}
		if got, ok := l["user_id"]; ok != (want.user != nil) || (ok && got != want.user) {
			t.Errorf("%d 本目（%s）: user_id = %v (ok=%v), want %v", i+1, want.path, got, ok, want.user)
		}
	}

	// サービスの行（users.me）にも同じ request_id と user_id が付く。
	me := lines[2]
	var found bool
	for _, l := range parseLogLines(t, out) {
		if l["msg"] == "users.me" {
			found = true
			if l["request_id"] != me["request_id"] || l["user_id"] != userID {
				t.Errorf("users.me の行に request_id / user_id が引き継がれていない: %v", l)
			}
		}
	}
	if !found {
		t.Errorf("users.me の行が無い:\n%s", out)
	}

	// パスワードとトークン本体はどの行にも出ない。
	token := strings.TrimPrefix(authorization, "Bearer ")
	for _, secret := range []string{"correct horse", token} {
		if strings.Contains(out, secret) {
			t.Errorf("ログに秘密が出ている（%.8s...）:\n%s", secret, out)
		}
	}
}
