// Command ta is the traceassert test runner. It runs Ginkgo/Gomega trace-conformance
// suites against a directory of captured traces and reports the result.
//
// A suite is an ordinary Go test package that uses the traceassert matchers. ta drives
// it with the Go toolchain (it cannot import an arbitrary external suite into itself),
// passing the traces directory to the suite as the TRACE_DIR environment variable and
// reading back Ginkgo's structured report.
package main

import (
	"os"

	"github.com/choria-io/fisk"
)

// Version is set at build time via -ldflags.
var Version = "0.0.0-dev"

func main() {
	app := fisk.New("ta", "traceassert test runner: run trace-conformance suites and report results")
	app.Version(Version)

	registerRun(app)
	registerInfo(app)

	app.MustParseWithUsage(os.Args[1:])
}
