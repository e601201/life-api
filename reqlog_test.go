package life

// RequestLog（1 リクエスト 1 行）の単体テスト。DB は使わない。
// JWTAuth を実際に通した user_id の確認は http_test.go（TestHTTPRequestLogUserID）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goa.design/clue/log"
)

// newLogContext は RequestLog に渡す logCtx を作る。出力は JSON で w に書く。
func newLogContext(w io.Writer) context.Context {
	return log.Context(context.Background(), log.WithOutput(w), log.WithFormat(log.FormatJSON))
}

// parseLogLines は JSON 行を 1 行ずつ map にする。JSON でない行があれば失敗。
func parseLogLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("ログが JSON ではない: %s: %v", l, err)
		}
		lines = append(lines, m)
	}
	return lines
}

// requestLines は msg=request の行（RequestLog が最後に出す 1 行）だけ返す。
func requestLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range parseLogLines(t, s) {
		if m["msg"] == "request" {
			out = append(out, m)
		}
	}
	return out
}

func TestRequestLogOneLinePerRequest(t *testing.T) {
	var buf bytes.Buffer
	h := RequestLog(newLogContext(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// サービスの log.Printf に相当。request_id が引き継がれること。
		log.Printf(r.Context(), "inner")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("hello")) //nolint:errcheck
	}))

	const traceID = "Root=1-6aaa84c0-0123456789abcdef01234567"
	req := httptest.NewRequest(http.MethodPost, "/entries?q=secret", nil)
	req.Header.Set(TraceHeader, traceID)
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	lines := parseLogLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2（inner と request）:\n%s", len(lines), buf.String())
	}
	inner, reqLine := lines[0], lines[1]

	want := map[string]any{
		"msg":         "request",
		"method":      "POST",
		"path":        "/entries",
		"status":      201.0,
		"bytes":       5.0,
		"remote_addr": "203.0.113.9", // X-Forwarded-For の末尾（ALB が足した側）
		"request_id":  traceID,
	}
	for k, v := range want {
		if reqLine[k] != v {
			t.Errorf("%s = %v, want %v", k, reqLine[k], v)
		}
	}
	if _, ok := reqLine["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms が無い: %v", reqLine)
	}
	if _, ok := reqLine["user_id"]; ok {
		t.Errorf("未認証なのに user_id がある: %v", reqLine)
	}
	if inner["msg"] != "inner" || inner["request_id"] != traceID {
		t.Errorf("内側の行に request_id が引き継がれていない: %v", inner)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("クエリ文字列がログに出ている:\n%s", buf.String())
	}
}

func TestRequestLogGeneratesIDWithoutTraceHeader(t *testing.T) {
	var buf bytes.Buffer
	h := RequestLog(newLogContext(&buf))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		// WriteHeader も Write も呼ばない。net/http と同じく 200 として記録されること。
	}))
	for range 2 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	}

	lines := requestLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("request の行数 = %d, want 2:\n%s", len(lines), buf.String())
	}
	id1, _ := lines[0]["request_id"].(string)
	id2, _ := lines[1]["request_id"].(string)
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Errorf("自前の request_id が振られていない: %q, %q", id1, id2)
	}
	if lines[0]["status"] != 200.0 || lines[0]["bytes"] != 0.0 {
		t.Errorf("status / bytes = %v / %v, want 200 / 0", lines[0]["status"], lines[0]["bytes"])
	}
	// httptest.NewRequest の RemoteAddr は 192.0.2.1:1234。ポートは落とす。
	if lines[0]["remote_addr"] != "192.0.2.1" {
		t.Errorf("remote_addr = %v, want 192.0.2.1", lines[0]["remote_addr"])
	}
}

func TestRequestLogUserID(t *testing.T) {
	var buf bytes.Buffer
	h := RequestLog(newLogContext(&buf))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// JWTAuth が検証後にやることと同じ。
		ctx := ContextWithUserID(r.Context(), 42)
		log.Printf(ctx, "inner")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/me", nil))

	lines := parseLogLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2:\n%s", len(lines), buf.String())
	}
	for _, l := range lines {
		if l["user_id"] != 42.0 {
			t.Errorf("%v の行に user_id が無い: %v", l["msg"], l)
		}
	}
}

func TestRequestLogPanic(t *testing.T) {
	var buf bytes.Buffer
	h := RequestLog(newLogContext(&buf))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic が net/http まで伝わっていない")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/entries", nil))
	}()

	lines := requestLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("request の行数 = %d, want 1:\n%s", len(lines), buf.String())
	}
	if lines[0]["status"] != 500.0 || lines[0]["panic"] != true {
		t.Errorf("panic の行が status=500 / panic=true でない: %v", lines[0])
	}
}

func TestRemoteAddr(t *testing.T) {
	for _, tt := range []struct {
		name string
		xff  string
		want string
	}{
		{name: "ヘッダ無しは接続元", xff: "", want: "192.0.2.1"},
		{name: "1 つならそれ", xff: "203.0.113.9", want: "203.0.113.9"},
		{name: "複数なら末尾（先頭はクライアントが書ける）", xff: "10.0.0.1, 198.51.100.7 ,203.0.113.9", want: "203.0.113.9"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := remoteAddr(r); got != tt.want {
				t.Errorf("remoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}
