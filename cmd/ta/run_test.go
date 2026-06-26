package main

import (
	"strings"
	"testing"

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
