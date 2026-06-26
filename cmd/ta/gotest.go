package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNoTestPackages is returned when a suite directory holds no Go packages with test
// files. Treated as a hard error, never an empty (and misleadingly green) run.
var ErrNoTestPackages = errors.New("no test packages found")

// listTestPackages returns the import paths of every package under dir that has Go
// test files (in-package or external _test). dir is the working directory so a suite
// with its own go.mod and relative replace directives resolves correctly.
//
// A `go list` failure (broken module, missing toolchain) is surfaced as an error; it
// is never collapsed into "no packages", which would otherwise produce a green run.
func listTestPackages(dir string) ([]string, error) {
	cmd := exec.Command("go", "list",
		"-f", "{{.ImportPath}}{{if or .TestGoFiles .XTestGoFiles}}\ttest{{end}}",
		"./...")
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("enumerating packages in %s: %w\n%s", dir, err, strings.TrimSpace(stderr.String()))
	}

	var pkgs []string
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		name, marker, found := strings.Cut(line, "\t")
		if found && marker == "test" {
			pkgs = append(pkgs, name)
		}
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoTestPackages, dir)
	}
	return pkgs, nil
}

// ginkgoRun is the outcome of compiling and running one package's test binary.
type ginkgoRun struct {
	pkg      string
	exitCode int            // go test exit code; -1 if the process could not run or was killed
	output   string         // combined stdout+stderr from the go test invocation
	reports  []ginkgoReport // nil when the report file was absent or unparseable
	parseErr error          // set when a report file existed but did not parse
	runErr   error          // raw error from exec (timeout, missing toolchain, ...)
}

// runPackage compiles and runs one test package's Ginkgo suite, writing its structured
// report to reportPath. ginkgoArgs are passed to the test binary after -args (e.g.
// -ginkgo.dry-run, -ginkgo.focus=...); env is the full child environment.
//
// SECURITY: this compiles and executes the Go code in dir. Only run suites you trust.
func runPackage(ctx context.Context, dir, pkg, reportPath string, env, goTestArgs, ginkgoArgs []string) ginkgoRun {
	args := []string{"test", "-count=1"}
	args = append(args, goTestArgs...)
	args = append(args, pkg, "-args", "-ginkgo.no-color", "-ginkgo.json-report="+reportPath)
	args = append(args, ginkgoArgs...)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = env

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()

	run := ginkgoRun{pkg: pkg, output: buf.String(), runErr: runErr, exitCode: exitCodeOf(runErr)}

	// A missing report is left as nil for the caller to classify: it means the suite
	// is not Ginkgo, failed to build, or crashed before writing - never zero specs. A
	// present-but-unparseable report is a parse error (a partial write after a crash).
	_, statErr := os.Stat(reportPath)
	if statErr == nil {
		reports, loadErr := loadGinkgoReports(reportPath)
		if loadErr != nil {
			run.parseErr = loadErr
		} else {
			run.reports = reports
		}
	}
	return run
}

// exitCodeOf extracts a process exit code from an exec error. A nil error is 0; an
// ExitError carries the real code; anything else (could not start, context killed) is
// -1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	ee, ok := errors.AsType[*exec.ExitError](err)
	if ok {
		return ee.ExitCode()
	}
	return -1
}
