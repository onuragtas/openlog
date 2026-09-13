// Package openlog is the openlog Go APM agent: a thin distribution of the OpenTelemetry
// Go SDK that exports traces, metrics and logs to openlog over OTLP.
//
// One call configures everything:
//
//	func main() {
//		shutdown, err := openlog.Start(context.Background(),
//			openlog.WithServiceName("checkout"),
//		)
//		if err != nil {
//			log.Fatal(err)
//		}
//		defer shutdown(context.Background())
//		// ...
//	}
//
// Start reads OPENLOG_LICENSE_KEY, OPENLOG_ENDPOINT, OPENLOG_SERVICE_NAME and the other
// variables documented in README.md; options take precedence over the environment.
// The resource carries host.id resolved exactly like the openlog infra agent, so services
// are linked to the host they run on.
//
// Instrumentation helpers live in sub-packages: openloghttp (net/http server and client),
// openlogsql (database/sql), openlogslog (log/slog bridge). gRPC, chi, gin and echo helpers
// are separate modules under instrumentation/ so their dependencies stay optional.
package openlog
