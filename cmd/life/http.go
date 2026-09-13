package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	entries "github.com/e601201/life-api/gen/entries"
	health "github.com/e601201/life-api/gen/health"
	entriessvr "github.com/e601201/life-api/gen/http/entries/server"
	healthsvr "github.com/e601201/life-api/gen/http/health/server"
	tagssvr "github.com/e601201/life-api/gen/http/tags/server"
	userssvr "github.com/e601201/life-api/gen/http/users/server"
	tags "github.com/e601201/life-api/gen/tags"
	users "github.com/e601201/life-api/gen/users"
	"goa.design/clue/debug"
	"goa.design/clue/log"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

// maxRequestBody はリクエストボディの読み取り上限。日誌の本文を通すには十分な
// 大きさで、かつ 1 リクエストがメモリを食い潰さない値にしている。
const maxRequestBody = 1 << 20 // 1MiB

// handleHTTPServer starts configures and starts a HTTP server on the given
// URL. It shuts down the server if any error is received in the error channel.
func handleHTTPServer(ctx context.Context, u *url.URL, healthEndpoints *health.Endpoints, usersEndpoints *users.Endpoints, entriesEndpoints *entries.Endpoints, tagsEndpoints *tags.Endpoints, wg *sync.WaitGroup, errc chan error, dbg bool) {

	// Provide the transport specific request decoder and response encoder.
	// The goa http package has built-in support for JSON, XML and gob.
	// Other encodings can be used by providing the corresponding functions,
	// see goa.design/implement/encoding.
	var (
		dec = goahttp.RequestDecoder
		enc = goahttp.ResponseEncoder
	)

	// Build the service HTTP request multiplexer and mount debug and profiler
	// endpoints in debug mode.
	var mux goahttp.Muxer
	{
		mux = goahttp.NewMuxer()
		if dbg {
			// Mount pprof handlers for memory profiling under /debug/pprof.
			debug.MountPprofHandlers(debug.Adapt(mux))
			// Mount /debug endpoint to enable or disable debug logs at runtime.
			debug.MountDebugLogEnabler(debug.Adapt(mux))
		}
	}

	// Wrap the endpoints with the transport specific layers. The generated
	// server packages contains code generated from the design which maps
	// the service input and output data structures to HTTP requests and
	// responses.
	var (
		healthServer  *healthsvr.Server
		usersServer   *userssvr.Server
		entriesServer *entriessvr.Server
		tagsServer    *tagssvr.Server
	)
	{
		eh := errorHandler(ctx)
		ef := errorFormatter(ctx)
		healthServer = healthsvr.New(healthEndpoints, mux, dec, enc, eh, ef)
		usersServer = userssvr.New(usersEndpoints, mux, dec, enc, eh, ef)
		entriesServer = entriessvr.New(entriesEndpoints, mux, dec, enc, eh, ef)
		tagsServer = tagssvr.New(tagsEndpoints, mux, dec, enc, eh, ef)
	}

	// Configure the mux.
	healthsvr.Mount(mux, healthServer)
	userssvr.Mount(mux, usersServer)
	entriessvr.Mount(mux, entriesServer)
	tagssvr.Mount(mux, tagsServer)

	var handler http.Handler = mux
	if dbg {
		// Log query and response bodies if debug logs are enabled.
		handler = debug.HTTP()(handler)
	}
	handler = log.HTTP(ctx)(handler)
	// ボディの読み取り上限。最外周に置いて全エンドポイントに効かせる。
	// 上限を超えたリクエストはデコードの時点で失敗し、400 で返る。
	handler = http.MaxBytesHandler(handler, maxRequestBody)

	// http.Server のタイムアウトは既定でどれも無制限。放っておくと、遅い
	// （あるいは意図的に遅くした）クライアント 1 本が接続を掴んだままになる。
	// 登録とログインは認証なしで叩けるので、読み・書き・アイドルのそれぞれに上限を置く。
	srv := &http.Server{
		Addr:              u.Host,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	for _, m := range healthServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range usersServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range entriesServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range tagsServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}

	(*wg).Add(1)
	go func() {
		defer (*wg).Done()

		// Start HTTP server in a separate goroutine.
		go func() {
			log.Printf(ctx, "HTTP server listening on %q", u.Host)
			errc <- srv.ListenAndServe()
		}()

		<-ctx.Done()
		log.Printf(ctx, "shutting down HTTP server at %q", u.Host)

		// Shutdown gracefully with a 30s timeout.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		err := srv.Shutdown(shutdownCtx)
		if err != nil {
			log.Printf(shutdownCtx, "failed to shutdown: %v", err)
		}
	}()
}

// errorFormatter は goa がクライアントに返すエラー表現を組み立てる。
//
// design に宣言していないエラー（DB のエラーなど）は、goa の既定では
// err.Error() がそのままレスポンスの message に載る（goahttp.NewErrorResponse が
// goa.Fault で包むため）。PostgreSQL のメッセージには接続先ホストや DB ユーザ名が
// 入るので、外には汎用の文言だけを返し、中身はログに残す。
//
// レスポンスとログの両方に同じ ID を出しているので、問い合わせを受けたら
// その ID でログを引ける。
func errorFormatter(logCtx context.Context) func(context.Context, error) goahttp.Statuser {
	return func(ctx context.Context, err error) goahttp.Statuser {
		var serr *goa.ServiceError
		if errors.As(err, &serr) {
			// design で宣言したエラー（not_found）とバリデーション違反は
			// クライアントに見せる前提の情報なので、そのまま返す。
			return goahttp.NewErrorResponse(ctx, err)
		}
		fault := goa.Fault("internal error")
		log.Printf(logCtx, "ERROR id=%s: %s", fault.ID, err.Error())
		return goahttp.NewErrorResponse(ctx, fault)
	}
}

// errorHandler returns a function that writes and logs the given error.
// The function also writes and logs the error unique ID so that it's possible
// to correlate.
func errorHandler(logCtx context.Context) func(context.Context, http.ResponseWriter, error) {
	return func(ctx context.Context, w http.ResponseWriter, err error) {
		log.Printf(logCtx, "ERROR: %s", err.Error())
	}
}
