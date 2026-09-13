// Command openlog-processor consumes OTLP from Kafka and writes ClickHouse rows.
package main

import (
	"fmt"
	"os"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-processor %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-processor", app.RunProcessor)
}
