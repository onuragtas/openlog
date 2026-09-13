package version

import "net/http"

// Header carries the product version on every HTTP response of ingest, api and the admin server
// (docs/contracts/releases-updates.md §5). gRPC responses carry it as header metadata GRPCHeader.
const (
	Header     = "X-Openlog-Version"
	GRPCHeader = "x-openlog-version"
)

// Middleware sets Header on every response before next runs.
func Middleware(next http.Handler) http.Handler {
	v := String()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(Header, v)
		next.ServeHTTP(w, r)
	})
}
