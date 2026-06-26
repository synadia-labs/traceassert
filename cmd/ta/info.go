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

// infoCmd backs `ta info`: list the specs a suite declares, without running them.
type infoCmd struct {
	suite string
	json  bool
}

func registerInfo(app *fisk.Application) {
	c := &infoCmd{}

	info := app.Command("info", "List the specs in a test suite without running them").Action(c.action)
	info.HelpLong(`Compiles the suite in <suite> and lists its specs using Ginkgo's dry-run, without
executing them or touching any traces.

SECURITY: this compiles the code in <suite>. Only point it at suites you trust.

Note: only statically-declared specs are listed. Specs generated at runtime from data
read inside a container body will not appear here, and may differ from what "ta run"
executes.`)

	info.Arg("suite", "Directory holding the test suite").Required().ExistingDirVar(&c.suite)
	info.Flag("json", "Render the spec list as JSON").UnNegatableBoolVar(&c.json)
}

func (c *infoCmd) action(_ *fisk.ParseContext) error {
	os.Exit(c.execute())
	return nil
}

// specInfo is one listed spec.
type specInfo struct {
	Suite   string `json:"suite"`
	Package string `json:"package"`
	Name    string `json:"name"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Pending bool   `json:"pending,omitempty"`

	// containers and leaf hold the spec's hierarchy for the indented tree listing;
	// not serialized (Name carries the full text).
	containers []string
	leaf       string
}

func (c *infoCmd) execute() int {
	suiteAbs, err := filepath.Abs(c.suite)
	if err != nil {
		return c.fatal(err)
	}

	pkgs, err := listTestPackages(suiteAbs)
	if err != nil {
		return c.fatal(err)
	}
	sort.Strings(pkgs)

	tmpDir, err := os.MkdirTemp("", "ta-info-")
	if err != nil {
		return c.fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	env := os.Environ()
	ginkgoArgs := []string{"-ginkgo.dry-run"}
	goTestArgs := []string{"-timeout=5m"}

	var specs []specInfo
	focusSeen := false

	for i, pkg := range pkgs {
		reportPath := filepath.Join(tmpDir, fmt.Sprintf("report-%d.json", i))
		runCtx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		run := runPackage(runCtx, suiteAbs, pkg, reportPath, env, goTestArgs, ginkgoArgs)
		cancel()

		if len(run.reports) == 0 {
			msg := run.output
			if run.parseErr != nil {
				msg = run.parseErr.Error() + "\n" + run.output
			}
			return c.fatal(fmt.Errorf("could not list specs in %s:\n%s", pkg, msg))
		}

		for _, r := range run.reports {
			if r.SuiteHasProgrammaticFocus {
				focusSeen = true
			}
			for j := range r.SpecReports {
				s := &r.SpecReports[j]
				if !s.isSpec() {
					continue
				}
				specs = append(specs, specInfo{
					Suite:      r.SuiteDescription,
					Package:    pkg,
					Name:       s.fullText(),
					File:       s.LeafNodeLocation.FileName,
					Line:       s.LeafNodeLocation.LineNumber,
					Pending:    s.State == "pending",
					containers: s.ContainerHierarchyTexts,
					leaf:       s.LeafNodeText,
				})
			}
		}
	}

	if c.json {
		out, err := json.MarshalIndent(specs, "", "  ")
		if err != nil {
			return c.fatal(err)
		}
		fmt.Println(string(out))
		return 0
	}

	c.render(specs, focusSeen)
	return 0
}

func (c *infoCmd) render(specs []specInfo, focusSeen bool) {
	if len(specs) == 0 {
		fmt.Println("No specs found")
		return
	}

	// Specs arrive grouped per package and in declaration order, so consecutive specs
	// sharing a suite form one group rendered as a single indented tree.
	for i := 0; i < len(specs); {
		j := i
		for j < len(specs) && specs[j].Suite == specs[i].Suite && specs[j].Package == specs[i].Package {
			j++
		}
		group := specs[i:j]

		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s (%s)\n", group[0].Suite, group[0].Package)

		nodes := make([]specNode, len(group))
		for k, s := range group {
			nodes[k] = specNode{containers: s.containers, leaf: s.leaf}
		}
		for _, row := range buildTree(nodes) {
			label := "  " + treeIndent(row.Depth) + row.Label
			if row.Spec >= 0 {
				s := group[row.Spec]
				if s.Pending {
					label += " [pending]"
				}
				if s.File != "" {
					label += fmt.Sprintf("  (%s:%d)", filepath.Base(s.File), s.Line)
				}
			}
			fmt.Println(label)
		}

		i = j
	}

	fmt.Printf("\n%d spec(s)\n", len(specs))
	if focusSeen {
		fmt.Println("note: suite has committed focused specs (FIt/FDescribe); this listing reflects the focused subset")
	}
}

func (c *infoCmd) fatal(err error) int {
	if c.json {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	return 2
}
