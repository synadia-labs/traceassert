package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jedib0t/go-pretty/v6/text"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/synadia-labs/traceassert/ctrf"
)

// spec builds an It spec report in the given state.
func spec(name, state string) ginkgoSpecReport {
	return ginkgoSpecReport{LeafNodeType: "It", LeafNodeText: name, State: state}
}

// setup builds a suite setup node report (e.g. BeforeSuite) in the given state.
func setup(nodeType, state string) ginkgoSpecReport {
	return ginkgoSpecReport{LeafNodeType: nodeType, State: state}
}

func report(succeeded, focus bool, specs ...ginkgoSpecReport) ginkgoReport {
	return ginkgoReport{
		SuiteDescription:          "Suite",
		SuiteSucceeded:            succeeded,
		SuiteHasProgrammaticFocus: focus,
		SpecReports:               specs,
	}
}

func TestBuildSuiteResult(t *testing.T) {
	cases := []struct {
		name       string
		run        ginkgoRun
		allowFocus bool
		wantKind   string
		wantOK     bool
		wantFocus  bool
	}{
		{
			name:     "clean pass",
			run:      ginkgoRun{exitCode: 0, reports: []ginkgoReport{report(true, false, spec("a", "passed"))}},
			wantKind: kindOK, wantOK: true,
		},
		{
			name:     "spec failure",
			run:      ginkgoRun{exitCode: 1, reports: []ginkgoReport{report(false, false, spec("a", "failed"))}},
			wantKind: kindFailed, wantOK: false,
		},
		{
			// A committed FIt leaves SuiteSucceeded true but go test exits non-zero.
			// Relying on SuiteSucceeded alone would wrongly pass it.
			name:     "programmatic focus rejected",
			run:      ginkgoRun{exitCode: 1, reports: []ginkgoReport{report(true, true, spec("a", "passed"))}},
			wantKind: kindFailed, wantOK: false, wantFocus: true,
		},
		{
			name:       "programmatic focus allowed",
			run:        ginkgoRun{exitCode: 1, reports: []ginkgoReport{report(true, true, spec("a", "passed"))}},
			allowFocus: true,
			wantKind:   kindOK, wantOK: true, wantFocus: true,
		},
		{
			// Non-zero exit with a "succeeded" report (e.g. an extra failing plain
			// TestXxx in the package) must not pass.
			name:     "exit code disagrees with report",
			run:      ginkgoRun{exitCode: 1, reports: []ginkgoReport{report(true, false, spec("a", "passed"))}},
			wantKind: kindFailed, wantOK: false,
		},
		{
			name:     "no report is a tooling failure",
			run:      ginkgoRun{exitCode: 2, output: "build failed"},
			wantKind: kindTooling, wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &runCmd{allowFocus: tc.allowFocus}
			sr, _, kind, focus := c.buildSuiteResult(tc.run)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if sr.Succeeded != tc.wantOK {
				t.Errorf("succeeded = %v, want %v", sr.Succeeded, tc.wantOK)
			}
			if focus != tc.wantFocus {
				t.Errorf("focus = %v, want %v", focus, tc.wantFocus)
			}
			if tc.wantKind == kindTooling && sr.Error == "" {
				t.Error("tooling failure must record an error")
			}
		})
	}
}

func TestTallySpecs(t *testing.T) {
	r := report(false, false,
		spec("p1", "passed"),
		spec("p2", "passed"),
		spec("s1", "skipped"),
		spec("pend", "pending"),
		spec("f1", "failed"),
		spec("panic", "panicked"),
		setup("BeforeSuite", "failed"), // a setup failure: a Failed, but not a spec
	)

	var got Totals
	tallySpecs(r, &got)

	want := Totals{Specs: 6, Passed: 2, Failed: 3, Skipped: 1, Pending: 1}
	if got != want {
		t.Errorf("tally = %+v, want %+v", got, want)
	}
}

// buildReport folds package runs into a Report the way execute does, minus the exit
// code: one SuiteResult per run, the totals merged, focus and success derived.
func buildReport(c *runCmd, runs ...ginkgoRun) *Report {
	rep := &Report{
		Description: c.description,
		SuiteDir:    "/suite",
		TracesDir:   "/traces",
		StartedAt:   time.UnixMilli(1_700_000_000_000),
		FinishedAt:  time.UnixMilli(1_700_000_001_500),
		Duration:    1500 * time.Millisecond,
		Success:     true,
	}
	for _, run := range runs {
		sr, delta, _, focus := c.buildSuiteResult(run)
		rep.Suites = append(rep.Suites, sr)
		mergeTotals(&rep.Totals, delta)
		if focus {
			rep.HasProgrammaticFocus = true
		}
		if !sr.Succeeded {
			rep.Success = false
		}
	}
	return rep
}

// ctrfDocument marshals the report the way writeJSON does and decodes it back into a
// generic map for assertions on the emitted keys.
func ctrfDocument(t *testing.T, rep *Report) ([]byte, map[string]any) {
	t.Helper()

	raw, err := json.MarshalIndent(rep.toCTRF(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	err = json.Unmarshal(raw, &doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// validateCTRF checks an emitted document against the committed CTRF schema. The
// schema is additionalProperties: false at every level, so a stray key fails here.
func validateCTRF(t *testing.T, raw []byte) {
	t.Helper()

	schema, err := os.ReadFile(filepath.Join("testdata", "ctrf.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	sch, err := jsonschema.CompileString("ctrf.schema.json", string(schema))
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}
	var v any
	err = json.Unmarshal(raw, &v)
	if err != nil {
		t.Fatal(err)
	}
	err = sch.Validate(v)
	if err != nil {
		t.Fatalf("document does not validate against the CTRF schema: %v\n%s", err, raw)
	}
}

// ctrfTests indexes the emitted results.tests entries by name.
func ctrfTests(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()

	results, ok := doc["results"].(map[string]any)
	if !ok {
		t.Fatalf("no results object: %v", doc)
	}
	list, ok := results["tests"].([]any)
	if !ok {
		t.Fatalf("results.tests is not an array: %v", results["tests"])
	}
	byName := map[string]map[string]any{}
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("test entry is not an object: %v", e)
		}
		byName[entry["name"].(string)] = entry
	}
	return byName
}

func TestCTRFDocument(t *testing.T) {
	located := func(name, state string, containers ...string) ginkgoSpecReport {
		s := spec(name, state)
		s.ContainerHierarchyTexts = containers
		s.LeafNodeLocation = ginkgoCodeLocation{FileName: "/suite/a_test.go", LineNumber: 42}
		s.RunTime = 1234 * time.Millisecond
		return s
	}
	failed := located("f1", "failed", "ingest")
	failed.Failure.Message = "expected 1 got 2"
	failed.CapturedStdOutErr = "line one\nline two\n"
	failed.LeafNodeLabels = []string{"ADR50-C-301"}
	skippedWithReason := located("s1", "skipped", "ingest")
	skippedWithReason.Failure.Message = "capability-absent"
	skippedNoReason := located("s2", "skipped", "ingest")
	before := setup("BeforeSuite", "failed")
	before.Failure.Message = "could not connect"

	good := ginkgoRun{exitCode: 1, reports: []ginkgoReport{report(false, false,
		located("p1", "passed", "ingest", "requests"),
		failed,
		skippedWithReason,
		skippedNoReason,
		located("pend", "pending", "ingest"),
		before,
	)}}
	good.pkg = "example.com/suite/ingest"
	broken := ginkgoRun{pkg: "example.com/suite/broken", exitCode: 2, output: "build failed: undefined: x"}

	rep := buildReport(&runCmd{description: "nightly"}, good, broken)
	raw, doc := ctrfDocument(t, rep)
	validateCTRF(t, raw)

	if doc["reportFormat"] != "CTRF" || doc["specVersion"] != ctrf.SpecVersion {
		t.Errorf("reportFormat/specVersion = %v/%v", doc["reportFormat"], doc["specVersion"])
	}
	results := doc["results"].(map[string]any)
	tool := results["tool"].(map[string]any)
	if tool["name"] != "ta" || tool["version"] != Version {
		t.Errorf("tool = %v", tool)
	}

	tests := ctrfTests(t, doc)
	summary := results["summary"].(map[string]any)
	want := map[string]float64{"tests": 6, "passed": 1, "failed": 2, "skipped": 2, "pending": 1, "other": 0,
		"start": 1_700_000_000_000, "stop": 1_700_000_001_500}
	for k, v := range want {
		if summary[k] != v {
			t.Errorf("summary.%s = %v, want %v", k, summary[k], v)
		}
	}
	if float64(len(tests)) != summary["tests"] {
		t.Errorf("summary.tests = %v but %d entries were emitted", summary["tests"], len(tests))
	}

	status := map[string][2]string{
		"ingest requests p1": {"passed", "passed"},
		"ingest f1":          {"failed", "failed"},
		"ingest s1":          {"skipped", "skipped"},
		"ingest s2":          {"skipped", "skipped"},
		"ingest pend":        {"pending", "pending"},
		"[BeforeSuite]":      {"failed", "failed"},
	}
	for name, st := range status {
		entry, ok := tests[name]
		if !ok {
			t.Errorf("no entry for %q", name)
			continue
		}
		if entry["status"] != st[0] || entry["rawStatus"] != st[1] {
			t.Errorf("%s: status/rawStatus = %v/%v, want %v", name, entry["status"], entry["rawStatus"], st)
		}
	}

	p1 := tests["ingest requests p1"]
	if got := p1["suite"]; !slices.Equal(anyStrings(got), []string{"ingest", "requests"}) {
		t.Errorf("suite = %v", got)
	}
	if p1["duration"] != float64(1234) || p1["line"] != float64(42) || p1["filePath"] != "/suite/a_test.go" {
		t.Errorf("duration/line/filePath = %v/%v/%v", p1["duration"], p1["line"], p1["filePath"])
	}
	for _, key := range []string{"message", "stdout", "tags", "extra"} {
		_, ok := p1[key]
		if ok {
			t.Errorf("passing spec carries %s: %v", key, p1[key])
		}
	}

	f1 := tests["ingest f1"]
	if f1["message"] != "expected 1 got 2" {
		t.Errorf("message = %v", f1["message"])
	}
	if got := anyStrings(f1["stdout"]); !slices.Equal(got, []string{"line one", "line two"}) {
		t.Errorf("stdout = %v", got)
	}
	if got := anyStrings(f1["tags"]); !slices.Equal(got, []string{"ADR50-C-301"}) {
		t.Errorf("tags = %v", got)
	}

	s1 := tests["ingest s1"]
	extra, ok := s1["extra"].(map[string]any)
	if !ok || extra["skip_reason"] != "capability-absent" {
		t.Errorf("skipped spec extra = %v", s1["extra"])
	}
	_, ok = s1["message"]
	if ok {
		t.Errorf("skip reason leaked into message: %v", s1["message"])
	}
	_, ok = tests["ingest s2"]["extra"]
	if ok {
		t.Errorf("skip without a reason carries extra: %v", tests["ingest s2"]["extra"])
	}

	// A setup node has no containers: it falls back to the suite description so the
	// suite array is never empty.
	bs := tests["[BeforeSuite]"]
	if got := anyStrings(bs["suite"]); !slices.Equal(got, []string{"Suite"}) {
		t.Errorf("setup node suite = %v", got)
	}
	if bs["message"] != "could not connect" {
		t.Errorf("setup node message = %v", bs["message"])
	}

	runExtra := results["extra"].(map[string]any)
	if runExtra["description"] != "nightly" || runExtra["suite_dir"] != "/suite" || runExtra["traces_dir"] != "/traces" {
		t.Errorf("results.extra = %v", runExtra)
	}
	if runExtra["success"] != false || runExtra["has_programmatic_focus"] != false {
		t.Errorf("success/focus = %v/%v", runExtra["success"], runExtra["has_programmatic_focus"])
	}
	pkgs := runExtra["packages"].([]any)
	if len(pkgs) != 2 {
		t.Fatalf("packages = %v", pkgs)
	}
	brokenPkg := pkgs[1].(map[string]any)
	if brokenPkg["package"] != "example.com/suite/broken" || brokenPkg["succeeded"] != false {
		t.Errorf("broken package = %v", brokenPkg)
	}
	if !strings.Contains(brokenPkg["error"].(string), "build failed") {
		t.Errorf("broken package error = %v", brokenPkg["error"])
	}
	goodPkg := pkgs[0].(map[string]any)
	_, ok = goodPkg["error"]
	if ok {
		t.Errorf("package that ran carries an error: %v", goodPkg["error"])
	}
}

// anyStrings converts a decoded JSON string array to []string; nil for anything else.
func anyStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

func TestCTRFEmptyRun(t *testing.T) {
	// A run where no package produced a report has no test entries: tests must still
	// be an array, not null, for the schema to accept it.
	rep := buildReport(&runCmd{}, ginkgoRun{pkg: "example.com/suite", exitCode: 2, output: "build failed"})
	raw, doc := ctrfDocument(t, rep)
	validateCTRF(t, raw)
	if len(ctrfTests(t, doc)) != 0 {
		t.Errorf("expected no test entries: %s", raw)
	}
}

func TestCTRFStatus(t *testing.T) {
	for state, want := range map[string]string{
		"passed":      "passed",
		"skipped":     "skipped",
		"pending":     "pending",
		"failed":      "failed",
		"panicked":    "failed",
		"aborted":     "failed",
		"interrupted": "failed",
		"timedout":    "failed",
	} {
		if got := ctrfStatus(state); got != want {
			t.Errorf("ctrfStatus(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestIsFailedState(t *testing.T) {
	for state, wantFailed := range map[string]bool{
		"passed":      false,
		"skipped":     false,
		"pending":     false,
		"failed":      true,
		"panicked":    true,
		"aborted":     true,
		"interrupted": true,
		"timedout":    true,
	} {
		if got := isFailedState(state); got != wantFailed {
			t.Errorf("isFailedState(%q) = %v, want %v", state, got, wantFailed)
		}
	}
}

func TestBuildTree(t *testing.T) {
	nodes := []specNode{
		{containers: []string{"A", "B"}, leaf: "it1"},
		{containers: []string{"A", "B"}, leaf: "it2"}, // shares A/B with previous
		{containers: []string{"A", "C"}, leaf: "it3"}, // diverges at depth 1
		{containers: nil, leaf: "top"},                // no containers: leaf at depth 0
	}

	want := []treeRow{
		{Depth: 0, Label: "A", Spec: -1},
		{Depth: 1, Label: "B", Spec: -1},
		{Depth: 2, Label: "it1", Spec: 0},
		{Depth: 2, Label: "it2", Spec: 1},
		{Depth: 1, Label: "C", Spec: -1},
		{Depth: 2, Label: "it3", Spec: 2},
		{Depth: 0, Label: "top", Spec: 3},
	}

	got := buildTree(nodes)
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestStateColor(t *testing.T) {
	for state, want := range map[string]text.Color{
		"passed":      text.FgGreen,
		"skipped":     text.FgYellow,
		"pending":     text.FgYellow,
		"failed":      text.FgRed,
		"panicked":    text.FgRed,
		"aborted":     text.FgRed,
		"interrupted": text.FgRed,
		"timedout":    text.FgRed,
	} {
		if got := stateColor(state); got != want {
			t.Errorf("stateColor(%q) = %v, want %v", state, got, want)
		}
	}
}

// throwawaySuiteGinkgo is the Ginkgo version the throwaway suite compiles against: the
// one the examples pin, so the JSON shape ta reads is the shape those suites write.
const throwawaySuiteGinkgo = "v2.31.0"

// throwawaySuiteSource is a Ginkgo suite with labeled and skipped specs. It exercises
// the label fields and the skip message the way a real conformance suite writes them.
const throwawaySuiteSource = `package suite_test

import (
	"testing"

	ginkgo "github.com/onsi/ginkgo/v2"
)

func TestSuite(t *testing.T) { ginkgo.RunSpecs(t, "Labels Suite") }

var _ = ginkgo.Describe("ingest", ginkgo.Label("ADR50"), func() {
	ginkgo.It("records the request", ginkgo.Label("ADR50-C-301"), func() {})
	ginkgo.It("records the reply", ginkgo.Label("ADR50-C-302", "slow"), func() {})
	ginkgo.It("repeats the container label", ginkgo.Label("ADR50", "ADR50-C-303"), func() {})
	ginkgo.It("needs fast ingest", func() { ginkgo.Skip("capability-absent: no fast ingest") })
	ginkgo.It("skips without a reason", func() { ginkgo.Skip("") })
})

var _ = ginkgo.It("has no labels", func() {})
`

// writeThrowawaySuite writes a standalone Go module holding throwawaySuiteSource into
// a temporary directory and returns that directory. The module requires only Ginkgo;
// go test resolves the rest of the graph with -mod=mod from the module cache.
func writeThrowawaySuite(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	gomod := "module example.com/suite\n\ngo 1.26\n\nrequire github.com/onsi/ginkgo/v2 " + throwawaySuiteGinkgo + "\n"
	err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "suite_test.go"), []byte(throwawaySuiteSource), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// runThrowawaySuite compiles and runs the throwaway suite through runPackage and
// returns the Report of that one-package run.
func runThrowawaySuite(t *testing.T) *Report {
	t.Helper()

	dir := writeThrowawaySuite(t)
	env := append(os.Environ(), "GOWORK=off", "GOFLAGS="+os.Getenv("GOFLAGS")+" -mod=mod")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The root module does not require Ginkgo, so a cold module cache has to fetch it
	// here. A runner that cannot, offline or with GOPROXY=off, skips rather than fails;
	// CI has the network and runs it.
	download := exec.CommandContext(ctx, "go", "mod", "download")
	download.Dir = dir
	download.Env = env
	out, err := download.CombinedOutput()
	if err != nil {
		t.Skipf("cannot fetch Ginkgo %s for the throwaway suite: %v\n%s", throwawaySuiteGinkgo, err, out)
	}

	run := runPackage(ctx, dir, "./...", filepath.Join(dir, "report.json"), env, nil, nil)
	if run.parseErr != nil {
		t.Fatalf("parsing report: %v\n%s", run.parseErr, run.output)
	}
	if len(run.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (exit %d)\n%s", len(run.reports), run.exitCode, run.output)
	}
	if !run.reports[0].SuiteSucceeded {
		t.Fatalf("suite did not succeed\n%s", run.output)
	}

	return buildReport(&runCmd{}, run)
}

func TestSpecResultsLabelsAndSkipReason(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Ginkgo suite")
	}

	rep := runThrowawaySuite(t)
	if len(rep.Suites) != 1 {
		t.Fatalf("got %d suites, want 1", len(rep.Suites))
	}
	results := map[string]TestResult{}
	for _, tr := range rep.Suites[0].Tests {
		results[tr.Name] = tr
	}
	if len(results) != 6 {
		t.Fatalf("got %d specs, want 6: %v", len(results), results)
	}

	labels := []struct {
		name string
		want []string
	}{
		{"ingest records the request", []string{"ADR50", "ADR50-C-301"}},
		{"ingest records the reply", []string{"ADR50", "ADR50-C-302", "slow"}},
		{"ingest repeats the container label", []string{"ADR50", "ADR50-C-303"}},
		{"ingest needs fast ingest", []string{"ADR50"}},
		{"has no labels", nil},
	}
	for _, tc := range labels {
		got := results[tc.name].Labels
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: labels = %v, want %v", tc.name, got, tc.want)
		}
	}

	skips := []struct {
		name  string
		state string
		want  string
	}{
		{"ingest needs fast ingest", "skipped", "capability-absent: no fast ingest"},
		{"ingest skips without a reason", "skipped", ""},
		{"ingest records the request", "passed", ""},
	}
	for _, tc := range skips {
		got := results[tc.name]
		if got.State != tc.state {
			t.Errorf("%s: state = %q, want %q", tc.name, got.State, tc.state)
		}
		if got.SkipReason != tc.want {
			t.Errorf("%s: skip reason = %q, want %q", tc.name, got.SkipReason, tc.want)
		}
		if got.Failure != "" {
			t.Errorf("%s: failure = %q, want none", tc.name, got.Failure)
		}
	}

	// The emitted document is CTRF: it validates against the committed schema, the
	// labels are tags, the Ginkgo state is rawStatus and the Skip message is
	// extra.skip_reason. Keys are omitted, not emitted empty, when there is nothing
	// to say.
	raw, doc := ctrfDocument(t, rep)
	validateCTRF(t, raw)
	tests := ctrfTests(t, doc)
	if len(tests) != 6 {
		t.Fatalf("got %d entries, want 6: %s", len(tests), raw)
	}
	summary := doc["results"].(map[string]any)["summary"].(map[string]any)
	if summary["tests"] != float64(6) || summary["passed"] != float64(4) || summary["skipped"] != float64(2) {
		t.Errorf("summary = %v", summary)
	}

	passing := tests["ingest records the request"]
	_, ok := passing["extra"]
	if ok {
		t.Errorf("passing spec has an extra key: %v", passing)
	}
	if got := anyStrings(passing["tags"]); !slices.Equal(got, []string{"ADR50", "ADR50-C-301"}) {
		t.Errorf("tags = %v", got)
	}
	if got := anyStrings(passing["suite"]); !slices.Equal(got, []string{"ingest"}) {
		t.Errorf("suite = %v", got)
	}
	if passing["rawStatus"] != "passed" || passing["status"] != "passed" {
		t.Errorf("status/rawStatus = %v/%v", passing["status"], passing["rawStatus"])
	}
	silent := tests["ingest skips without a reason"]
	_, ok = silent["extra"]
	if ok {
		t.Errorf("skip without a message has an extra key: %v", silent)
	}
	unlabeled := tests["has no labels"]
	_, ok = unlabeled["tags"]
	if ok {
		t.Errorf("unlabeled spec has a tags key: %v", unlabeled)
	}
	// A top-level It has no containers: the suite falls back to the Ginkgo suite
	// description.
	if got := anyStrings(unlabeled["suite"]); !slices.Equal(got, []string{"Labels Suite"}) {
		t.Errorf("top-level spec suite = %v", got)
	}
	skipped := tests["ingest needs fast ingest"]
	extra, ok := skipped["extra"].(map[string]any)
	if !ok || extra["skip_reason"] != "capability-absent: no fast ingest" {
		t.Errorf("skipped spec extra = %v", skipped["extra"])
	}
	if skipped["status"] != "skipped" || skipped["rawStatus"] != "skipped" {
		t.Errorf("status/rawStatus = %v/%v", skipped["status"], skipped["rawStatus"])
	}
}

func TestClampOutput(t *testing.T) {
	t.Run("short passes through", func(t *testing.T) {
		if got := clampOutput("hello"); got != "hello" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("long is truncated to the tail", func(t *testing.T) {
		in := strings.Repeat("x", maxOutputBytes+1000) + "END"
		got := clampOutput(in)
		if len(got) > maxOutputBytes+64 {
			t.Errorf("len = %d, want <= %d", len(got), maxOutputBytes+64)
		}
		if !strings.HasSuffix(got, "END") {
			t.Error("must keep the tail of the output")
		}
		if !strings.Contains(got, "truncated") {
			t.Error("must mark truncation")
		}
	})
	t.Run("invalid utf8 is sanitized", func(t *testing.T) {
		got := clampOutput("a\xffb")
		if !strings.ContainsRune(got, '�') {
			t.Errorf("invalid bytes not replaced: %q", got)
		}
	})
}
