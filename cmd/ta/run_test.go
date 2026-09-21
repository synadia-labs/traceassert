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
// returns its spec results keyed by spec name.
func runThrowawaySuite(t *testing.T) map[string]TestResult {
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

	c := &runCmd{}
	byName := map[string]TestResult{}
	for _, tr := range c.specResults(run.reports[0]) {
		byName[tr.Name] = tr
	}
	return byName
}

func TestSpecResultsLabelsAndSkipReason(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a Ginkgo suite")
	}

	results := runThrowawaySuite(t)
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

	// The JSON keys are omitted, not emitted empty, when there is nothing to say.
	keys := func(name string) map[string]any {
		raw, err := json.Marshal(results[name])
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		err = json.Unmarshal(raw, &m)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	passing := keys("ingest records the request")
	_, ok := passing["skip_reason"]
	if ok {
		t.Errorf("passing spec has a skip_reason key: %v", passing)
	}
	_, ok = passing["labels"]
	if !ok {
		t.Errorf("labeled spec has no labels key: %v", passing)
	}
	silent := keys("ingest skips without a reason")
	_, ok = silent["skip_reason"]
	if ok {
		t.Errorf("skip without a message has a skip_reason key: %v", silent)
	}
	unlabeled := keys("has no labels")
	_, ok = unlabeled["labels"]
	if ok {
		t.Errorf("unlabeled spec has a labels key: %v", unlabeled)
	}
	skipped := keys("ingest needs fast ingest")
	if skipped["skip_reason"] != "capability-absent: no fast ingest" {
		t.Errorf("skip_reason = %v", skipped["skip_reason"])
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
