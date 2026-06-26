package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/choria-io/fisk"
)

// runCmd backs `ta run`: compile and run a Ginkgo/Gomega trace-conformance suite
// against a directory of traces, then emit a report.
type runCmd struct {
	suite       string
	traces      string
	report      string
	description string
	focus       string
	timeout     time.Duration
	json        bool
	verbose     bool
	allowEmpty  bool
	allowFocus  bool
}

func registerRun(app *fisk.Application) {
	c := &runCmd{}

	run := app.Command("run", "Run a trace-conformance test suite against a directory of traces").Action(c.action)
	run.HelpLong(`Compiles and runs the Go test suite in --suite using "go test", once per test
package, and writes a JSON report of the result.

The suite locates its fixtures through the TRACE_DIR environment variable, which ta
sets to the absolute --traces path; suites should read os.Getenv("TRACE_DIR").

SECURITY: this compiles and runs the code in --suite. Only point it at suites you
trust.

Exit codes: 0 all specs passed; 1 one or more specs failed (or committed focus); 2 the
suite could not be run (build failure, not a Ginkgo suite, zero specs, bad arguments).`)

	run.Flag("suite", "Directory holding the test suite").Short('s').Required().PlaceHolder("DIR").ExistingDirVar(&c.suite)
	run.Flag("traces", "Directory of traces, exported to the suite as TRACE_DIR").Short('t').Required().PlaceHolder("DIR").ExistingDirVar(&c.traces)
	run.Flag("report", "Write the JSON report to this file (- for stdout)").Short('r').PlaceHolder("FILE").StringVar(&c.report)
	run.Flag("description", "Human-readable description recorded in the report").Short('d').PlaceHolder("TEXT").StringVar(&c.description)
	run.Flag("focus", "Only run specs whose text matches this regular expression").PlaceHolder("REGEXP").StringVar(&c.focus)
	run.Flag("timeout", "Per-package go test timeout").Default("10m").DurationVar(&c.timeout)
	run.Flag("json", "Also write the JSON report to stdout").UnNegatableBoolVar(&c.json)
	run.Flag("verbose", "Include captured output for passing specs too").Short('v').UnNegatableBoolVar(&c.verbose)
	run.Flag("allow-empty", "Succeed even when the suite runs zero specs").UnNegatableBoolVar(&c.allowEmpty)
	run.Flag("allow-focus", "Succeed even when the suite has committed focused specs (FIt/FDescribe)").UnNegatableBoolVar(&c.allowFocus)
}

// action runs the command and translates its outcome into a process exit code. All
// temporary state is cleaned up inside execute (via defers) before os.Exit is reached.
func (c *runCmd) action(_ *fisk.ParseContext) error {
	os.Exit(c.execute())
	return nil
}

func (c *runCmd) execute() int {
	started := time.Now()

	suiteAbs, err := filepath.Abs(c.suite)
	if err != nil {
		return c.fatal(err)
	}
	tracesAbs, err := filepath.Abs(c.traces)
	if err != nil {
		return c.fatal(err)
	}

	if !c.json {
		warnIfNoTraceFiles(tracesAbs)
	}

	pkgs, err := listTestPackages(suiteAbs)
	if err != nil {
		return c.fatal(err)
	}
	sort.Strings(pkgs)

	tmpDir, err := os.MkdirTemp("", "ta-run-")
	if err != nil {
		return c.fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	env := append(os.Environ(), "TRACE_DIR="+tracesAbs)

	var ginkgoArgs []string
	if c.focus != "" {
		ginkgoArgs = append(ginkgoArgs, "-ginkgo.focus="+c.focus)
	}
	goTestArgs := []string{"-timeout=" + c.timeout.String()}

	rep := &Report{
		Description: c.description,
		SuiteDir:    suiteAbs,
		TracesDir:   tracesAbs,
		StartedAt:   started,
	}

	tooling := false
	allSucceeded := true

	if !c.json {
		fmt.Fprintf(os.Stderr, "running %d test package(s) in %s ...\n", len(pkgs), suiteAbs)
	}

	for i, pkg := range pkgs {
		reportPath := filepath.Join(tmpDir, fmt.Sprintf("report-%d.json", i))
		runCtx, cancel := context.WithTimeout(context.Background(), c.timeout+time.Minute)
		run := runPackage(runCtx, suiteAbs, pkg, reportPath, env, goTestArgs, ginkgoArgs)
		cancel()

		sr, delta, kind, focus := c.buildSuiteResult(run)
		rep.Suites = append(rep.Suites, sr)
		mergeTotals(&rep.Totals, delta)
		if focus {
			rep.HasProgrammaticFocus = true
		}
		if kind == kindTooling {
			tooling = true
		}
		if !sr.Succeeded {
			allSucceeded = false
		}
	}

	rep.FinishedAt = time.Now()
	rep.DurationSeconds = rep.FinishedAt.Sub(rep.StartedAt).Seconds()

	empty := rep.Totals.Specs == 0 && !c.allowEmpty
	rep.Success = !tooling && !empty && allSucceeded

	err = c.emit(rep)
	if err != nil {
		return c.fatal(err)
	}

	switch {
	case tooling:
		return 2
	case empty:
		if !c.json {
			fmt.Fprintln(os.Stderr, "\nerror: the suite ran 0 specs (wrong --suite, a build problem, or over-filtering?); use --allow-empty to permit this")
		}
		return 2
	case !rep.Success:
		return 1
	default:
		return 0
	}
}

// run-classification kinds.
const (
	kindOK      = "ok"
	kindFailed  = "failed"
	kindTooling = "tooling"
)

// buildSuiteResult turns one package run into a SuiteResult, classifying it as a clean
// pass, a spec failure, or a tooling problem (could not be run to a verdict). Success
// is authoritative: it requires a clean go test exit AND Ginkgo's own SuiteSucceeded
// AND a parseable report - never a tally of spec states, which would miss committed
// focus, panics, or a non-Ginkgo package that writes no report at all.
func (c *runCmd) buildSuiteResult(run ginkgoRun) (SuiteResult, Totals, string, bool) {
	sr := SuiteResult{Package: run.pkg}
	var totals Totals

	// No report: build failure, not a Ginkgo suite, crash before write, or timeout.
	if len(run.reports) == 0 {
		msg := run.output
		if run.parseErr != nil {
			msg = run.parseErr.Error() + "\n" + run.output
		}
		if msg == "" {
			msg = "no Ginkgo JSON report was produced - is this a Ginkgo suite?"
		}
		sr.Error = clampOutput(msg)
		return sr, totals, kindTooling, false
	}

	suiteOK := true
	focus := false
	for _, r := range run.reports {
		if !r.SuiteSucceeded {
			suiteOK = false
		}
		if r.SuiteHasProgrammaticFocus {
			focus = true
		}
		if sr.Description == "" {
			sr.Description = r.SuiteDescription
		}
		if sr.Path == "" {
			sr.Path = r.SuitePath
		}
		sr.DurationSeconds += r.RunTime.Seconds()
		sr.Tests = append(sr.Tests, c.specResults(r)...)
		tallySpecs(r, &totals)
	}

	// A clean exit and Ginkgo's verdict must agree. Committed focus makes go test exit
	// non-zero while SuiteSucceeded stays true; honor that as failure unless allowed.
	succeeded := suiteOK && run.exitCode == 0
	if !succeeded && suiteOK && focus && c.allowFocus {
		succeeded = true
	}
	sr.Succeeded = succeeded

	if succeeded {
		return sr, totals, kindOK, focus
	}
	return sr, totals, kindFailed, focus
}

// tallySpecs folds one suite's spec states into a Totals. Only It specs count toward
// the spec tally; a setup node (BeforeSuite, ...) that failed increments Failed but is
// not a spec.
func tallySpecs(r ginkgoReport, totals *Totals) {
	for i := range r.SpecReports {
		s := &r.SpecReports[i]
		if !s.isSpec() {
			if isFailedState(s.State) {
				totals.Failed++
			}
			continue
		}
		totals.Specs++
		switch s.State {
		case "passed":
			totals.Passed++
		case "skipped":
			totals.Skipped++
		case "pending":
			totals.Pending++
		default:
			totals.Failed++
		}
	}
}

// mergeTotals adds delta into totals.
func mergeTotals(totals *Totals, delta Totals) {
	totals.Specs += delta.Specs
	totals.Passed += delta.Passed
	totals.Failed += delta.Failed
	totals.Skipped += delta.Skipped
	totals.Pending += delta.Pending
}

// specResults converts the spec reports of one suite into TestResults. It lists every
// It spec, plus any setup node (BeforeSuite, ...) that failed, so a setup failure is
// never invisible. Captured output is attached to non-passing specs (all specs with
// --verbose).
func (c *runCmd) specResults(r ginkgoReport) []TestResult {
	var out []TestResult
	for i := range r.SpecReports {
		s := &r.SpecReports[i]
		if !s.isSpec() && !isFailedState(s.State) {
			continue
		}

		t := TestResult{
			Name:            s.fullText(),
			State:           s.State,
			DurationSeconds: s.RunTime.Seconds(),
			File:            s.LeafNodeLocation.FileName,
			Line:            s.LeafNodeLocation.LineNumber,
			containers:      s.ContainerHierarchyTexts,
			leaf:            s.LeafNodeText,
		}
		// A setup node (BeforeSuite, ...) has no leaf text; label it by its node type
		// so it still renders as a single top-level row.
		if t.leaf == "" {
			t.leaf = s.fullText()
		}
		// Ginkgo also stores a skip reason in Failure.Message, so only surface it as a
		// failure for states that actually failed.
		if isFailedState(s.State) {
			t.Failure = s.Failure.Message
			t.Output = clampOutput(s.combinedOutput())
		} else if c.verbose {
			t.Output = clampOutput(s.combinedOutput())
		}
		out = append(out, t)
	}
	return out
}

// emit writes the report to its destinations and prints the human summary. When JSON
// goes to stdout, the human summary goes to stderr so stdout stays pure JSON.
func (c *runCmd) emit(rep *Report) error {
	jsonToStdout := c.json || c.report == "-"

	err := rep.writeJSON(c.report, jsonToStdout)
	if err != nil {
		return err
	}

	// When the JSON report goes to stdout (--json or --report -) that is the whole
	// requested output, so the human summary is suppressed. Otherwise it is shown on
	// stdout - for a plain run, or alongside a --report <file>.
	if jsonToStdout {
		return nil
	}
	rep.renderSummary(os.Stdout, c.report)
	return nil
}

// fatal reports a tooling/usage error and returns the exit code 2. Under --json the
// error is emitted as structured JSON to stderr, mirroring the house style.
func (c *runCmd) fatal(err error) int {
	if c.json {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	return 2
}

// warnIfNoTraceFiles notes when the traces directory has no .json files: the contract
// is just "a directory", so this is a warning rather than a hard error, but an empty
// directory is almost always a mistake worth flagging.
func warnIfNoTraceFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "warning: --traces %s contains no .json files\n", dir)
}
