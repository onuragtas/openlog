// Command http-sql is a small instrumented web service: net/http ServeMux routes, SQLite
// through openlogsql, slog bridged to OTLP logs and an optional built-in load generator.
//
//	OPENLOG_LICENSE_KEY=dev-license-key OPENLOG_ENDPOINT=http://localhost:4318 \
//	  go run ./http-sql -load 2m
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	openlog "github.com/onuragtas/openlog/agents/go"
	"github.com/onuragtas/openlog/agents/go/openloghttp"
	"github.com/onuragtas/openlog/agents/go/openlogslog"
	"github.com/onuragtas/openlog/agents/go/openlogsql"
	_ "modernc.org/sqlite"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	load := flag.Duration("load", 0, "run a load generator against the server for this long, then exit (0 = serve until interrupted)")
	rps := flag.Int("rps", 25, "load generator requests per second")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := openlog.Start(ctx, openlog.WithServiceName("example-http-sql"), openlog.WithServiceVersion("1.0.0"))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			log.Printf("openlog shutdown: %v", err)
		}
	}()
	slog.SetDefault(slog.New(openlogslog.Wrap(slog.NewJSONHandler(os.Stdout, nil))))

	db, err := openlogsql.Open("sqlite", "file:shop?mode=memory&cache=shared")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := seed(ctx, db); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		var name string
		err := db.QueryRowContext(r.Context(), "SELECT name FROM users WHERE id = ?", r.PathValue("id")).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			slog.WarnContext(r.Context(), "user not found", "id", r.PathValue("id"))
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"name":%q}`, r.PathValue("id"), name)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		total := rand.IntN(10000)
		res, err := tx.ExecContext(r.Context(), "INSERT INTO orders (user_id, total_cents, note) VALUES (?, ?, 'web order')", 1+rand.IntN(3), total)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "UPDATE users SET orders = orders + 1 WHERE id = 1")
		}
		if err != nil || tx.Commit() != nil {
			http.Error(w, "order failed", http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		slog.InfoContext(r.Context(), "order placed", "order_id", id, "total_cents", total)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(100+rand.IntN(200)) * time.Millisecond)
		_, _ = io.WriteString(w, "slow")
	})
	mux.HandleFunc("GET /error", func(w http.ResponseWriter, r *http.Request) {
		_, err := db.ExecContext(r.Context(), "SELECT * FROM missing_table")
		slog.ErrorContext(r.Context(), "query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	})

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: openloghttp.Middleware(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	slog.Info("listening", "addr", lis.Addr().String())

	if *load > 0 {
		n := generate(ctx, "http://"+lis.Addr().String(), *load, *rps)
		slog.Info("load finished", "requests", n)
	} else {
		<-ctx.Done()
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func seed(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT, orders INTEGER DEFAULT 0)",
		"CREATE TABLE IF NOT EXISTS orders (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, total_cents INTEGER, note TEXT)",
		"INSERT OR IGNORE INTO users (id, name) VALUES (1, 'ada'), (2, 'grace'), (3, 'linus')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// generate sends a mix of requests through an instrumented client (client spans + context
// propagation) until d elapsed.
func generate(ctx context.Context, base string, d time.Duration, rps int) int64 {
	client := openloghttp.Client()
	client.Timeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	tick := time.NewTicker(time.Second / time.Duration(max(rps, 1)))
	defer tick.Stop()
	var sent atomic.Int64
	for {
		select {
		case <-ctx.Done():
			return sent.Load()
		case <-tick.C:
		}
		go func() {
			var req *http.Request
			switch p := rand.IntN(100); {
			case p < 55:
				req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/users/"+strconv.Itoa(1+rand.IntN(4)), nil)
			case p < 85:
				req, _ = http.NewRequestWithContext(ctx, http.MethodPost, base+"/orders", nil)
			case p < 95:
				req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/slow", nil)
			default:
				req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/error", nil)
			}
			if resp, err := client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			sent.Add(1)
		}()
	}
}
