package life

// HTTP 経由の認証。Goa が DSL から生成した配線（Authorization ヘッダ → JWTAuth →
// 401 / ctx のユーザ ID → サービス）を、生成コードを実際に通して確かめる。
// サービスを直接呼ぶテストではこの層が抜けるため。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	entries "github.com/e601201/life-api/gen/entries"
	entriessvr "github.com/e601201/life-api/gen/http/entries/server"
	userssvr "github.com/e601201/life-api/gen/http/users/server"
	users "github.com/e601201/life-api/gen/users"
	goahttp "goa.design/goa/v3/http"
)

// newTestServer は users と entries を cmd/life/http.go と同じ生成コードでマウントした
// サーバを立てる。エラーの整形は Goa の既定（宣言したエラーは design のステータス）。
func newTestServer(t *testing.T) *httptest.Server {
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

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// call はリクエストを 1 本投げ、レスポンスと JSON のボディを返す。ボディが
// オブジェクトでないとき（一覧の配列、204 の空）は nil を返す。
// authorization が空でなければそのまま Authorization ヘッダに載せる。
func call(t *testing.T, srv *httptest.Server, method, path, authorization string, body any) (*http.Response, map[string]any) {
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
	obj, _ := parsed.(map[string]any)
	return resp, obj
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

func TestHTTPTokenFromOtherUserDoesNotLeakIdentity(t *testing.T) {
	srv := newTestServer(t)
	aliceAuth, aliceID := login(t, srv, "alice@example.com", "correct horse")
	bobAuth, bobID := login(t, srv, "bob@example.com", "battery staple")

	// それぞれのトークンで作った entries の user_id が、それぞれの id になること。
	for _, tt := range []struct {
		authorization string
		want          float64
	}{
		{authorization: aliceAuth, want: aliceID},
		{authorization: bobAuth, want: bobID},
	} {
		resp, body := call(t, srv, http.MethodPost, "/entries", tt.authorization,
			map[string]string{"title": "誰の", "entry_date": "2026-09-01", "kind": "diary"})
		requireStatus(t, resp, body, http.StatusCreated, "")
		if body["user_id"] != tt.want {
			t.Errorf("user_id = %v, want %v", body["user_id"], tt.want)
		}
	}
}
