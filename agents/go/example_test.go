package openlog_test

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

	openlog "github.com/onuragtas/openlog/agents/go"
	"github.com/onuragtas/openlog/agents/go/openloghttp"
	"github.com/onuragtas/openlog/agents/go/openlogslog"
	"github.com/onuragtas/openlog/agents/go/openlogsql"
)

func ExampleStart() {
	// OPENLOG_LICENSE_KEY and OPENLOG_ENDPOINT normally come from the environment.
	shutdown, err := openlog.Start(context.Background(),
		openlog.WithServiceName("checkout"),
		openlog.WithEnvironment("production"),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	slog.SetDefault(slog.New(openlogslog.Wrap(slog.NewJSONHandler(os.Stdout, nil))))

	db, err := openlogsql.Open("postgres", "postgres://app@db:5432/shop")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		var total float64
		_ = db.QueryRowContext(r.Context(), "SELECT total FROM orders WHERE id = $1", r.PathValue("id")).Scan(&total)
		slog.InfoContext(r.Context(), "order loaded", "id", r.PathValue("id"))
	})
	_ = http.ListenAndServe(":8080", openloghttp.Middleware(mux))
}
