package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ginkgoReport mirrors the subset of github.com/onsi/ginkgo/v2/types.Report that ta
// reads from a suite's -ginkgo.json-report output. It is defined locally, and parsed
// as plain JSON, so traceassert never links Ginkgo: the runner stays independent of
// the framework the suites happen to use, exactly as the assertion library does.
//
// The report file is a JSON array of these - one element per suite the test binary
// ran (normally one).
type ginkgoReport struct {
	SuiteDescription           string             `json:"SuiteDescription"`
	SuitePath                  string             `json:"SuitePath"`
	SuiteSucceeded             bool               `json:"SuiteSucceeded"`
	SuiteHasProgrammaticFocus  bool               `json:"SuiteHasProgrammaticFocus"`
	SpecialSuiteFailureReasons []string           `json:"SpecialSuiteFailureReasons"`
	RunTime                    time.Duration      `json:"RunTime"`
	PreRunStats                ginkgoPreRunStats  `json:"PreRunStats"`
	SpecReports                []ginkgoSpecReport `json:"SpecReports"`
}

// ginkgoPreRunStats is Ginkgo's count of declared vs selected specs. TotalSpecs==0
// means an empty suite; SpecsThatWillRun==0 means nothing was selected to run (e.g. a
// --focus that matched nothing). ta treats either as a non-success unless allowed.
type ginkgoPreRunStats struct {
	TotalSpecs       int `json:"TotalSpecs"`
	SpecsThatWillRun int `json:"SpecsThatWillRun"`
}

// ginkgoSpecReport is one node's report. Setup nodes (BeforeSuite, AfterSuite, ...)
// appear here alongside the actual It specs; isSpec distinguishes them.
type ginkgoSpecReport struct {
	ContainerHierarchyTexts    []string           `json:"ContainerHierarchyTexts"`
	LeafNodeType               string             `json:"LeafNodeType"`
	LeafNodeText               string             `json:"LeafNodeText"`
	LeafNodeLocation           ginkgoCodeLocation `json:"LeafNodeLocation"`
	State                      string             `json:"State"`
	RunTime                    time.Duration      `json:"RunTime"`
	Failure                    ginkgoFailure      `json:"Failure"`
	CapturedGinkgoWriterOutput string             `json:"CapturedGinkgoWriterOutput"`
	CapturedStdOutErr          string             `json:"CapturedStdOutErr"`
}

type ginkgoCodeLocation struct {
	FileName   string `json:"FileName"`
	LineNumber int    `json:"LineNumber"`
}

type ginkgoFailure struct {
	Message string `json:"Message"`
}

// fullText is the human spec name: the container hierarchy joined with the leaf text,
// matching Ginkgo's own SpecReport.FullText. Setup nodes have no text, so they are
// named by their node type, e.g. "[BeforeSuite]".
func (s *ginkgoSpecReport) fullText() string {
	parts := make([]string, 0, len(s.ContainerHierarchyTexts)+1)
	parts = append(parts, s.ContainerHierarchyTexts...)
	if s.LeafNodeText != "" {
		parts = append(parts, s.LeafNodeText)
	}
	if len(parts) == 0 {
		return "[" + s.LeafNodeType + "]"
	}
	return strings.Join(parts, " ")
}

// isSpec reports whether the node is an actual It spec, as opposed to a suite setup
// node such as BeforeSuite.
func (s *ginkgoSpecReport) isSpec() bool { return s.LeafNodeType == "It" }

// combinedOutput merges the stdout/stderr capture and the GinkgoWriter capture the way
// Ginkgo's own reporters do.
func (s *ginkgoSpecReport) combinedOutput() string {
	switch {
	case s.CapturedStdOutErr == "":
		return s.CapturedGinkgoWriterOutput
	case s.CapturedGinkgoWriterOutput == "":
		return s.CapturedStdOutErr
	default:
		return s.CapturedStdOutErr + "\n" + s.CapturedGinkgoWriterOutput
	}
}

// loadGinkgoReports decodes a -ginkgo.json-report file.
func loadGinkgoReports(path string) ([]ginkgoReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var reports []ginkgoReport
	err = json.Unmarshal(raw, &reports)
	if err != nil {
		return nil, fmt.Errorf("parsing ginkgo report %s: %w", path, err)
	}
	return reports, nil
}
