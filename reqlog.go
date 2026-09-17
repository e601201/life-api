package life

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"goa.design/clue/log"
)

// リクエスト単位のログ。1 リクエストにつき、終わったときに 1 行だけ出す。
//
//	{"time":"...","level":"info","request_id":"Root=1-...","msg":"request","method":"GET","path":"/entries",
//	 "status":200,"duration_ms":4,"bytes":13,"remote_addr":"203.0.113.1","user_id":1}
//
// clue/log の log.HTTP を使わないのは、start / end の 2 行になるのと、request id を
// 自前で振る（ALB の X-Amzn-Trace-Id を使えない）ため。ここで決めた request_id は
// 同じリクエストの中で出る他の行（サービスの log.Printf、エラー）にも付く。
//
// 出さないもの: クエリ文字列（検索語が入る）、ボディ（パスワードとトークンが入る。
// -debug のときだけ debug.HTTP が別に出す）、Authorization ヘッダ。

const (
	// TraceHeader は ALB がリクエストに付けるトレース ID のヘッダ。値は
	// "Root=1-<epoch の hex>-<乱数 24 hex>" で、ALB のアクセスログにも同じ値が載る。
	TraceHeader = "X-Amzn-Trace-Id"

	// UserIDLogKey はログに載せるユーザ ID のキー。JWTAuth を通ったリクエストにだけ付く。
	UserIDLogKey = "user_id"
)

// requestScope は、ミドルウェアより内側で決まる値を終了時の 1 行に持ち上げる入れ物。
// ctx は不変で、内側で足した値は外側からは見えない。先にポインタを ctx に入れておき、
// 内側（JWTAuth → ContextWithUserID）がそこに書く。
type requestScope struct {
	userID  int64
	hasUser bool
}

type requestScopeKey struct{}

// RequestLog は HTTP ミドルウェアを返す。
//
//  1. リクエストの ctx に logCtx のロガーを入れる（以降の log.Printf がこの設定で出る）
//  2. request id を決めて ctx に載せる。ALB の X-Amzn-Trace-Id があればそれ、無ければ自前で振る
//  3. 終わったときに 1 行出す。JWTAuth が通っていれば user_id も付ける
//
// logCtx に log.Context のロガーが無ければ panic する（起動時に気づくため）。
func RequestLog(logCtx context.Context) func(http.Handler) http.Handler {
	log.MustContainLogger(logCtx)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(TraceHeader)
			if id == "" {
				// ALB を通らない経路（ローカル、テスト）。base32 の 26 文字。
				id = rand.Text()
			}
			scope := &requestScope{}
			ctx := log.WithContext(r.Context(), logCtx)
			ctx = log.With(ctx, log.KV{K: log.RequestIDKey, V: id})
			ctx = context.WithValue(ctx, requestScopeKey{}, scope)

			rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()
			completed := false
			defer func() {
				status := rw.status
				kvs := []log.Fielder{
					log.KV{K: log.MessageKey, V: "request"},
					log.KV{K: "method", V: r.Method},
					log.KV{K: "path", V: r.URL.Path},
				}
				if !completed {
					// ハンドラが panic で抜けた。net/http が接続を閉じるのでクライアントに
					// ステータスは届かないが、行を落とさないために 500 として残す。
					// スタックは net/http が stderr に出す（"http: panic serving ..."）。
					status = http.StatusInternalServerError
				}
				kvs = append(kvs,
					log.KV{K: "status", V: status},
					log.KV{K: "duration_ms", V: time.Since(started).Milliseconds()},
					log.KV{K: "bytes", V: rw.bytes},
					log.KV{K: "remote_addr", V: remoteAddr(r)},
				)
				if scope.hasUser {
					kvs = append(kvs, log.KV{K: UserIDLogKey, V: scope.userID})
				}
				if !completed {
					kvs = append(kvs, log.KV{K: "panic", V: true})
				}
				// Print はバッファを通さず即座に書く（Info はエラーが出るまで溜める）。
				log.Print(ctx, kvs...)
			}()
			next.ServeHTTP(rw, r.WithContext(ctx))
			completed = true
		})
	}
}

// setLogUserID は認証済みのユーザ ID を、このリクエストの終了時の行に載せる。
// RequestLog を通っていない ctx（サービスを直接呼ぶテスト）では何もしない。
func setLogUserID(ctx context.Context, id int64) {
	if s, ok := ctx.Value(requestScopeKey{}).(*requestScope); ok {
		s.userID, s.hasUser = id, true
	}
}

// remoteAddr はクライアントの IP を返す。
//
// ALB は X-Forwarded-For の末尾に「自分が見たクライアントの IP」を足す。先頭側は
// クライアントが好きに書けるので信用しない。api の 8080 には ALB の SG からしか届かないため、
// 末尾の値は ALB が付けたものと決めてよい。ヘッダが無ければ（ローカル）接続元をそのまま使う。
func remoteAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// statusWriter はレスポンスのステータスと書いたバイト数を記録する。
// WriteHeader を呼ばずに Write されたときは net/http と同じく 200。
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap は http.ResponseController 経由で元の ResponseWriter（Flush など）に届くようにする。
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
