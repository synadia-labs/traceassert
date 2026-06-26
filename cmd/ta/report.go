package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/nats-io/natscli/columns"
)

// maxOutputBytes bounds the captured output ta embeds per spec, so a chatty suite
// (trace assertions print large diffs) cannot balloon the report into hundreds of
// megabytes. The tail is kept: a failure's useful context is at the end.
const maxOutputBytes = 8192

// Report is ta's run report: the persisted, machine-readable record of a `ta run`.
// It is deliberately small and stable - time, what ran, against which traces, and a
// pass/fail/output line per spec - so report consumers do not depend on Ginkgo's
// internal report shape.
type Report struct {
	Description          string        `json:"description,omitempty"`
	SuiteDir             string        `json:"suite_dir"`
	TracesDir            string        `json:"traces_dir"`
	StartedAt            time.Time     `json:"started_at"`
	FinishedAt           time.Time     `json:"finished_at"`
	DurationSeconds      float64       `json:"duration_seconds"`
	Success              bool          `json:"success"`
	HasProgrammaticFocus bool          `json:"has_programmatic_focus"`
	Totals               Totals        `json:"totals"`
	Suites               []SuiteResult `json:"suites"`
}

// Totals is the spec tally across every package in the run.
type Totals struct {
	Specs   int `json:"specs"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Pending int `json:"pending"`
}

// SuiteResult is the outcome of one test package (one compiled Ginkgo suite).
type SuiteResult struct {
	Package         string  `json:"package"`
	Description     string  `json:"description,omitempty"`
	Path            string  `json:"path,omitempty"`
	Succeeded       bool    `json:"succeeded"`
	DurationSeconds float64 `json:"duration_seconds"`
	// Error is set when the package could not be run to a verdict (build failure,
	// not a Ginkgo suite, timeout) - distinct from a suite whose specs simply failed.
	Error string       `json:"error,omitempty"`
	Tests []TestResult `json:"tests"`
}

// TestResult is one spec's outcome.
type TestResult struct {
	Name            string  `json:"name"`
	State           string  `json:"state"`
	DurationSeconds float64 `json:"duration_seconds"`
	File            string  `json:"file,omitempty"`
	Line            int     `json:"line,omitempty"`
	Failure         string  `json:"failure,omitempty"`
	Output          string  `json:"output,omitempty"`

	// containers and leaf hold the spec's hierarchy split into its container
	// (Describe/Context) texts and the leaf (It) text, used to render the indented
	// tree in the summary. They are not serialized; Name carries the full text.
	containers []string
	leaf       string
}

// failed reports whether a spec state counts as a failure. Anything that is not a
// pass, a skip, or a pending counts: failed, panicked, aborted, interrupted, timedout.
func isFailedState(state string) bool {
	switch state {
	case "passed", "skipped", "pending":
		return false
	default:
		return true
	}
}

// clampOutput sanitizes captured output to valid UTF-8 (trace bodies can carry raw
// bytes) and keeps only the trailing maxOutputBytes.
func clampOutput(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxOutputBytes {
		return s
	}
	tail := strings.ToValidUTF8(s[len(s)-maxOutputBytes:], "")
	return fmt.Sprintf("...[truncated, showing last %d bytes]...\n%s", maxOutputBytes, tail)
}

// writeJSON renders the report as indented JSON to a file and/or stdout. An empty or
// "-" path skips the file; toStdout additionally prints it to stdout.
func (r *Report) writeJSON(path string, toStdout bool) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if path != "" && path != "-" {
		err := os.WriteFile(path, append(out, '\n'), 0o644)
		if err != nil {
			return fmt.Errorf("writing report %s: %w", path, err)
		}
	}
	if toStdout {
		fmt.Println(string(out))
	}
	return nil
}

// renderSummary writes the run summary in the ntf-server admin style: a columns block
// of run metadata, a rounded table of specs per suite, and a final detail section for
// failures (so a developer never has to open the JSON to see why a run failed). It is
// written after every run.
func (r *Report) renderSummary(w io.Writer, reportPath string) {
	col := columns.New("Trace Conformance Run")

	// go-pretty's text colors key on the environment (NO_COLOR, TERM), not on whether
	// this particular writer is a terminal. Suppress them for a non-terminal target (a
	// pipe, a file, or the stderr fallback under --json) so escape codes never leak into
	// captured output.
	if !col.IsTerminal(w) {
		text.DisableColors()
	}

	result, resultColor := "PASS", text.FgGreen
	if !r.Success {
		result, resultColor = "FAIL", text.FgRed
	}
	col.AddRow("Result", resultColor.Sprint(result))
	col.AddRowIfNotEmpty("Description", r.Description)
	col.AddRow("Suite", r.SuiteDir)
	col.AddRow("Traces", r.TracesDir)
	col.AddRow("Specs", r.Totals.Specs)
	col.AddRow("Passed", r.Totals.Passed)
	col.AddRowIf("Failed", r.Totals.Failed, r.Totals.Failed > 0)
	col.AddRowIf("Skipped", r.Totals.Skipped, r.Totals.Skipped > 0)
	col.AddRowIf("Pending", r.Totals.Pending, r.Totals.Pending > 0)
	col.AddRowf("Duration", "%.2fs", r.DurationSeconds)
	col.AddRowIf("Report", reportPath, reportPath != "" && reportPath != "-")
	col.AddRowIf("Focus", "suite has committed focused specs (FIt/FDescribe)", r.HasProgrammaticFocus)
	_ = col.Frender(w)

	for _, s := range r.Suites {
		fmt.Fprintln(w)
		if s.Error != "" {
			fmt.Fprintf(w, "%s could not be run:\n%s\n", s.Package, indent(strings.TrimSpace(s.Error), "    "))
			continue
		}

		title := s.Description
		if title == "" {
			title = s.Package
		}
		t := newStatsTable(title)
		t.AppendHeader(table.Row{"Spec", "State", "Time"})

		nodes := make([]specNode, len(s.Tests))
		for i, spec := range s.Tests {
			nodes[i] = specNode{containers: spec.containers, leaf: spec.leaf}
		}
		for _, row := range buildTree(nodes) {
			label := treeIndent(row.Depth) + row.Label
			if row.Spec < 0 {
				// a container level (Describe/Context): no state of its own.
				t.AppendRow(table.Row{label, "", ""})
				continue
			}
			spec := s.Tests[row.Spec]
			t.AppendRow(table.Row{label, spec.State, fmt.Sprintf("%.3fs", spec.DurationSeconds)})
		}
		t.SetColumnConfigs([]table.ColumnConfig{
			{Number: 2, Transformer: stateTransformer},
			{Number: 3, Align: text.AlignRight},
		})
		fmt.Fprintln(w, t.Render())
	}

	r.renderFailures(w)
}

// renderFailures prints the failure message and location for each non-passing spec.
// The spec table shows the pass/fail grid; this section carries the detail a single
// table cell cannot.
func (r *Report) renderFailures(w io.Writer) {
	if r.Totals.Failed == 0 {
		return
	}

	fmt.Fprintln(w, "\nFailures:")
	for _, s := range r.Suites {
		for _, t := range s.Tests {
			if !isFailedState(t.State) {
				continue
			}
			loc := ""
			if t.File != "" {
				loc = fmt.Sprintf("  (%s:%d)", t.File, t.Line)
			}
			fmt.Fprintf(w, "\n  %s: %s%s\n", strings.ToUpper(t.State), t.Name, loc)
			if t.Failure != "" {
				fmt.Fprintln(w, indent(strings.TrimSpace(t.Failure), "    "))
			}
		}
	}
}

// stateTransformer colors a spec-state cell: green for passed, yellow for
// skipped/pending (the closest ANSI color to orange), red for any failure state
// (failed, panicked, aborted, interrupted, timedout). Empty cells (the container rows
// of the spec tree) stay blank. go-pretty omits the color automatically when the
// terminal does not support it or NO_COLOR is set, so redirected output stays plain.
func stateTransformer(val any) string {
	state, _ := val.(string)
	if state == "" {
		return ""
	}
	return stateColor(state).Sprint(state)
}

// stateColor maps a Ginkgo spec state to its display color.
func stateColor(state string) text.Color {
	switch state {
	case "passed":
		return text.FgGreen
	case "skipped", "pending":
		return text.FgYellow
	default:
		return text.FgRed
	}
}

// newStatsTable returns a table writer styled like ntf-server's stats tables: a rounded
// border, a centered title, plain (not upper-cased) header text and no trailing
// whitespace on rendered lines.
func newStatsTable(title string) table.Writer {
	t := table.NewWriter()
	t.SetStyle(table.StyleRounded)
	t.Style().Title.Align = text.AlignCenter
	t.Style().Format.Header = text.FormatDefault
	t.Style().Format.Footer = text.FormatDefault
	t.SuppressTrailingSpaces()
	if title != "" {
		t.SetTitle(title)
	}
	return t
}

// specNode is one spec's hierarchy: its container (Describe/Context) texts and its
// leaf (It) label.
type specNode struct {
	containers []string
	leaf       string
}

// treeRow is one rendered line of an indented spec tree. Spec is the index of the leaf
// spec the row represents, or -1 for a container level.
type treeRow struct {
	Depth int
	Label string
	Spec  int
}

// buildTree turns spec nodes, in order, into indented rows that do not repeat the
// container hierarchy shared with the preceding spec - so a deep, repetitive Ginkgo
// hierarchy renders as a tree rather than a column of near-identical full names.
func buildTree(nodes []specNode) []treeRow {
	var rows []treeRow
	var prev []string
	for i, n := range nodes {
		common := commonPrefixLen(prev, n.containers)
		for d := common; d < len(n.containers); d++ {
			rows = append(rows, treeRow{Depth: d, Label: n.containers[d], Spec: -1})
		}
		rows = append(rows, treeRow{Depth: len(n.containers), Label: n.leaf, Spec: i})
		prev = n.containers
	}
	return rows
}

// commonPrefixLen returns how many leading elements a and b share.
func commonPrefixLen(a, b []string) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// treeIndent is the indent for a tree level: two spaces per depth.
func treeIndent(depth int) string { return strings.Repeat("  ", depth) }

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
