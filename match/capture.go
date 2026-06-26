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
