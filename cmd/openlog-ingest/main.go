// Command openlog-ingest receives OTLP over HTTP and gRPC and produces it to Kafka.
package main

import (
	"fmt"
	"os"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-ingest %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-ingest", app.RunIngest)
}
