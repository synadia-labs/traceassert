package traceassert

import (
	"fmt"
	"os"
	"path/filepath"
)

// TraceDirEnv is the environment variable a test runner (the `ta` CLI) sets to the
// directory holding the captures a suite should assert against. CapturePath and
// LoadCapture resolve fixtures relative to it.
const TraceDirEnv = "TRACE_DIR"

// CapturePath resolves the on-disk path of an expanded capture named file:
// $TRACE_DIR/<file> (the directory the `ta` runner exports to the suite) when TRACE_DIR
// is set, else testdata/<file> (the in-repo default for a plain `go test`).
//
// When TRACE_DIR is set, the testdata fallback is deliberately not used, so a green run
// always asserts the supplied capture rather than a committed one.
func CapturePath(file string) string {
	dir := os.Getenv(TraceDirEnv)
	if dir != "" {
		return filepath.Join(dir, file)
	}
	return filepath.Join("testdata", file)
}

// LoadCapture resolves (via CapturePath) and loads an expanded-format capture. A
// capture that is missing, unreadable, or truncated is returned as an error - never a
// partial success - so a green run always means real evidence was asserted, not that the
// capture was quietly absent or cut short.
func LoadCapture(file string) (*Trace, error) {
	path := CapturePath(file)

	tr, err := LoadExpanded(path)
	if err != nil {
		return nil, err
	}
	if tr.Truncated() {
		return nil, fmt.Errorf("capture %q is truncated: the tracer cut it short (MaxSize/MaxTime) - recapture it", path)
	}
	return tr, nil
}
