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

	// Initialize the services.
	var (
		healthSvc  health.Service
		entriesSvc entries.Service
	)
	{
		healthSvc = life.NewHealth()
		// entries はまだインメモリ実装。CRUD を DB に移すときに pool を渡す。
		entriesSvc = life.NewEntries()
	}

	// Wrap the services in endpoints that can be invoked from other services
	// potentially running in different processes.
	var (
		healthEndpoints  *health.Endpoints
		entriesEndpoints *entries.Endpoints
	)
	{
		healthEndpoints = health.NewEndpoints(healthSvc)
		healthEndpoints.Use(debug.LogPayloads())
		healthEndpoints.Use(log.Endpoint)
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
			handleHTTPServer(ctx, u, healthEndpoints, entriesEndpoints, &wg, errc, *dbgF)
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
			handleHTTPServer(ctx, u, healthEndpoints, entriesEndpoints, &wg, errc, *dbgF)
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
