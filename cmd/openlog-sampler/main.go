// Command openlog-sampler is the tail-based sampling stage between ingest and the processor (D-075):
// it consumes the raw traces topic, decides per trace and produces kept spans to the sampled traces topic.
package main

import (
	"fmt"
	"os"

	"github.com/onuragtas/openlog/internal/app"
	"github.com/onuragtas/openlog/internal/version"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("openlog-sampler %s (commit %s, built %s)\n", version.String(), version.Commit, version.Date)
		return
	}
	app.Main("openlog-sampler", app.RunSampler)
}
