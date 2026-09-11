package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	life "github.com/e601201/life-api"
	"github.com/e601201/life-api/db"
	entries "github.com/e601201/life-api/gen/entries"
	health "github.com/e601201/life-api/gen/health"
	users "github.com/e601201/life-api/gen/users"
	"goa.design/clue/debug"
	"goa.design/clue/log"
)

func main() {
	// Define command line flags, add any other flag required to configure the
	// service.
	var (
		hostF     = flag.String("host", "localhost", "Server host (valid values: localhost, container)")
		domainF   = flag.String("domain", "", "Host domain name (overrides host domain specified in service design)")
		httpPortF = flag.String("http-port", "", "HTTP port (overrides host HTTP port specified in service design)")
		secureF   = flag.Bool("secure", false, "Use secure scheme (https or grpcs)")
		dbgF      = flag.Bool("debug", false, "Log request and response bodies")
		dbURLF    = flag.String("db-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection URL (default $DATABASE_URL)")
	)
	flag.Parse()

	// Setup logger. Replace logger with your own log package of choice.
	format := log.FormatJSON
	if log.IsTerminal() {
		format = log.FormatTerminal
	}
	ctx := log.Context(context.Background(), log.WithFormat(format))
	if *dbgF {
		ctx = log.Context(ctx, log.WithDebug())
		log.Debugf(ctx, "debug logs enabled")
	}
	log.Print(ctx, log.KV{K: "http-port", V: *httpPortF})

	// Connect to the database.
	//
	// スキーマの適用はこのプロセスではやらない（cmd/life-migrate に分けてある）。
	// ここで繋ぐのは、接続情報が間違っていれば最初のリクエストではなく起動時点で
	// 落としたいため。
	if *dbURLF == "" {
		log.Fatal(ctx, fmt.Errorf("database URL is empty: set -db-url or $DATABASE_URL"))
	}
	pool, err := db.Connect(ctx, *dbURLF)
	if err != nil {
		log.Fatalf(ctx, err, "failed to connect to database")
	}
	defer pool.Close()
	log.Print(ctx, log.KV{K: "msg", V: "database connected"})

	// JWT の署名鍵。DATABASE_URL と違ってフラグにはしない。コマンドラインに載せると
	// ps やシェルの履歴に残るため、環境変数だけで受け取る。ローカルは .env（compose）
	// か export、ECS では SSM から secrets として注入する（terraform/ssm.tf）。
	auth, err := life.NewAuth(os.Getenv("JWT_SECRET"))
	if err != nil {
		log.Fatalf(ctx, err, "invalid $JWT_SECRET")
	}

	// Initialize the services.
	var (
		healthSvc  health.Service
		usersSvc   users.Service
		entriesSvc entries.Service
	)
	{
		healthSvc = life.NewHealth(pool)
		usersSvc = life.NewUsers(pool, auth)
		entriesSvc = life.NewEntries(pool, auth)
	}

	// Wrap the services in endpoints that can be invoked from other services
	// potentially running in different processes.
	var (
		healthEndpoints  *health.Endpoints
		usersEndpoints   *users.Endpoints
		entriesEndpoints *entries.Endpoints
	)
	{
		healthEndpoints = health.NewEndpoints(healthSvc)
		healthEndpoints.Use(debug.LogPayloads())
		healthEndpoints.Use(log.Endpoint)
		// debug.LogPayloads は -debug のときにペイロードを丸ごとログに出す。
		// users には password が入るので、この層は付けない。
		usersEndpoints = users.NewEndpoints(usersSvc)
		usersEndpoints.Use(log.Endpoint)
		entriesEndpoints = entries.NewEndpoints(entriesSvc)
		entriesEndpoints.Use(debug.LogPayloads())
		entriesEndpoints.Use(log.Endpoint)
	}

	// Create channel used by both the signal handler and server goroutines
	// to notify the main goroutine when to stop the server.
	errc := make(chan error)

	// Setup interrupt handler. This optional step configures the process so
	// that SIGINT and SIGTERM signals cause the services to stop gracefully.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)

	// Start the servers and send errors (if any) to the error channel.
	switch *hostF {
	case "localhost":
		{
			addr := "http://localhost:8080"
			u, err := url.Parse(addr)
			if err != nil {
				log.Fatalf(ctx, err, "invalid URL %#v\n", addr)
			}
			if *secureF {
				u.Scheme = "https"
			}
			if *domainF != "" {
				u.Host = *domainF
			}
			if *httpPortF != "" {
				h, _, err := net.SplitHostPort(u.Host)
				if err != nil {
					log.Fatalf(ctx, err, "invalid URL %#v\n", u.Host)
				}
				u.Host = net.JoinHostPort(h, *httpPortF)
			} else if u.Port() == "" {
				u.Host = net.JoinHostPort(u.Host, "80")
			}
			handleHTTPServer(ctx, u, healthEndpoints, usersEndpoints, entriesEndpoints, &wg, errc, *dbgF)
		}

	case "container":
		{
			addr := "http://0.0.0.0:8080"
			u, err := url.Parse(addr)
			if err != nil {
				log.Fatalf(ctx, err, "invalid URL %#v\n", addr)
			}
			if *secureF {
				u.Scheme = "https"
			}
			if *domainF != "" {
				u.Host = *domainF
			}
			if *httpPortF != "" {
				h, _, err := net.SplitHostPort(u.Host)
				if err != nil {
					log.Fatalf(ctx, err, "invalid URL %#v\n", u.Host)
				}
				u.Host = net.JoinHostPort(h, *httpPortF)
			} else if u.Port() == "" {
				u.Host = net.JoinHostPort(u.Host, "80")
			}
			handleHTTPServer(ctx, u, healthEndpoints, usersEndpoints, entriesEndpoints, &wg, errc, *dbgF)
		}

	default:
		log.Fatal(ctx, fmt.Errorf("invalid host argument: %q (valid hosts: localhost|container)", *hostF))
	}

	// Wait for signal.
	log.Printf(ctx, "exiting (%v)", <-errc)

	// Send cancellation signal to the goroutines.
	cancel()

	wg.Wait()
	log.Printf(ctx, "exited")
}
