// Package ctrf holds the Common Test Report Format document `ta run` writes,
// https://ctrf.io: the types ta writes it with, and Load for reading it back.
//
// The document is ta's own dialect of CTRF. results.extra and a test's extra
// carry ta's fields, such as suite_dir and skip_reason, so a CTRF file written by
// another tool may load but lacks them.
//
// The types are ta's own so the runner takes no dependency for a document it
// only writes; the schema committed at cmd/ta/testdata/ctrf.schema.json in this
// repository pins the shape.
package ctrf

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReportFormat is the value of Report.ReportFormat for a CTRF document.
const ReportFormat = "CTRF"

// SpecVersion is the CTRF specification version the document follows. It is the
// version spec/ctrf.md in github.com/ctrf-io/ctrf states for the schema committed
// at cmd/ta/testdata/ctrf.schema.json in github.com/synadia-labs/traceassert;
// bump the two together.
const SpecVersion = "0.0.0"

// ErrReport is returned when a report cannot be read or decoded.
var ErrReport = errors.New("could not read the CTRF report")

// Statuses ta writes in Test.Status. Every Ginkgo state that is not passed,
// skipped or pending is written as StatusFailed, with the Ginkgo state kept in
// Test.RawStatus.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusPending = "pending"
)

// Ginkgo states ta writes in Test.RawStatus for a test whose Status is
// StatusFailed and that did not simply fail an assertion.
const (
	RawStatusPanicked    = "panicked"
	RawStatusAborted     = "aborted"
	RawStatusInterrupted = "interrupted"
	RawStatusTimedOut    = "timedout"
)

// Report is the whole document `ta run --report` writes.
type Report struct {
	ReportFormat string  `json:"reportFormat"`
	SpecVersion  string  `json:"specVersion"`
	Results      Results `json:"results"`
}

// Results is the body of the document: the tool that wrote it, the tally, one
// Test per spec and ta's run fields.
type Results struct {
	Tool    Tool     `json:"tool"`
	Summary Summary  `json:"summary"`
	Tests   []Test   `json:"tests"`
	Extra   RunExtra `json:"extra"`
}

// Tool names the tool that wrote the document. Version is the version ta was
// built with; ta built without the linker flag that sets it, as `go tool` builds
// it, reports 0.0.0-dev.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Summary counts the entries in Tests, so a failed setup node that is listed is
// counted in both Tests and Failed. Start and Stop are epoch milliseconds. Other
// is always zero: every Ginkgo state maps to one of the four named statuses.
type Summary struct {
	Tests   int   `json:"tests"`
	Passed  int   `json:"passed"`
	Failed  int   `json:"failed"`
	Skipped int   `json:"skipped"`
	Pending int   `json:"pending"`
	Other   int   `json:"other"`
	Start   int64 `json:"start"`
	Stop    int64 `json:"stop"`
}

// Test is one Ginkgo spec, or a setup node such as BeforeSuite that failed.
//
// Name is the container texts and the spec's own text joined with spaces, and
// Suite is the Ginkgo container texts. A test with no containers, such as a
// failed setup node, has the suite description, or the package path when the
// suite has no description, as its only Suite element. Its Name is its own text
// alone, or for a setup node, which has no text, its node type in brackets, such
// as "[BeforeSuite]". Duration is milliseconds, Tags are the Ginkgo labels, containers
// first, and RawStatus is Ginkgo's own state before it was mapped to Status.
type Test struct {
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Duration  int64      `json:"duration"`
	Suite     []string   `json:"suite"`
	FilePath  string     `json:"filePath,omitempty"`
	Line      int        `json:"line,omitempty"`
	Message   string     `json:"message,omitempty"`
	Stdout    []string   `json:"stdout,omitempty"`
	Tags      []string   `json:"tags,omitempty"`
	RawStatus string     `json:"rawStatus"`
	Extra     *TestExtra `json:"extra,omitempty"`
}

// TestExtra carries ta's own fields on a test. SkipReason is the message the
// spec passed to Skip, set only on a skipped test.
type TestExtra struct {
	SkipReason string `json:"skip_reason"`
}

// RunExtra carries the run's own fields under results.extra. Packages lists
// every test package, so a package that could not run to a verdict, and
// therefore has no tests to carry it, is still visible with its error.
type RunExtra struct {
	Description          string    `json:"description,omitempty"`
	SuiteDir             string    `json:"suite_dir"`
	TracesDir            string    `json:"traces_dir"`
	Success              bool      `json:"success"`
	HasProgrammaticFocus bool      `json:"has_programmatic_focus"`
	Packages             []Package `json:"packages"`
}

// Package is one test package of the suite. Error is set when the package could
// not be run to a verdict, such as a build failure, and such a package has no
// tests in the report.
type Package struct {
	Package     string `json:"package"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	Succeeded   bool   `json:"succeeded"`
	DurationMS  int64  `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}

// Load reads and decodes the report at path.
func Load(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReport, err)
	}

	var report Report

	err = json.Unmarshal(data, &report)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrReport, path, err)
	}

	if report.ReportFormat != ReportFormat {
		return nil, fmt.Errorf("%w: %s has report format %q rather than CTRF", ErrReport, path, report.ReportFormat)
	}

	return &report, nil
}

// SkipReason is the message the test passed to Skip, or empty when it has none.
func (t Test) SkipReason() string {
	if t.Extra == nil {
		return ""
	}

	return t.Extra.SkipReason
}

// LeafText is the spec's own text without the texts of the containers around
// it: Name with the Suite texts joined with spaces, plus one space, trimmed from
// the front. When Suite is empty, or Name does not start with that text, it
// returns Name unchanged.
//
// A test with no containers, such as a setup node, has the suite description or
// the package path as its Suite rather than an empty one. Its Name is usually
// returned whole, but when that Name happens to begin with the suite description
// (or package path) and a space, that prefix is trimmed as well.
func (t Test) LeafText() string {
	if len(t.Suite) == 0 {
		return t.Name
	}

	leaf, found := strings.CutPrefix(t.Name, strings.Join(t.Suite, " ")+" ")
	if !found {
		return t.Name
	}

	return leaf
}
