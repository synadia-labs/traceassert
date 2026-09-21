package match

import (
	"github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
)

// MustLoadCapture loads an expanded-format capture via traceassert.LoadCapture and fails
// the current spec - a clean Gomega/Ginkgo failure, never a panic - if the capture is
// missing, unreadable, or truncated. It is the one-line fixture loader for trace
// conformance suites: the path comes from $TRACE_DIR/<file> (the directory the `ta`
// runner exports to the suite) or testdata/<file> for a plain `go test`.
//
// Like every Gomega assertion it requires a registered fail handler - RegisterFailHandler(Fail)
// in the suite's Test function. The failure is reported at the caller, not here.
func MustLoadCapture(file string) *traceassert.Trace {
	tr, err := traceassert.LoadCapture(file)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"capture %q could not be loaded; set %s or place it under testdata/", file, traceassert.TraceDirEnv)
	return tr
}

// MustLoadSession loads every capture of a scenario via traceassert.LoadSession - the
// <name>.expanded.json and <name>-<n>.expanded.json files of the capture directory, one
// per connection the client made - and fails the current spec, a clean Gomega/Ginkgo
// failure and never a panic, when none is present or any is unreadable or truncated.
// The directory comes from $TRACE_DIR (the directory the `ta` runner exports to the
// suite) or testdata/ for a plain `go test`.
//
// Like every Gomega assertion it requires a registered fail handler - RegisterFailHandler(Fail)
// in the suite's Test function. The failure is reported at the caller, not here.
func MustLoadSession(name string) *traceassert.Session {
	s, err := traceassert.LoadSession(name)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(),
		"session %q could not be loaded; set %s or place its captures under testdata/", name, traceassert.TraceDirEnv)
	return s
}
