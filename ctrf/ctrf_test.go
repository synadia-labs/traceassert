package ctrf_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/synadia-labs/traceassert/ctrf"
)

func writeReport(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "report.json")

	err := os.WriteFile(path, []byte(body), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoad(t *testing.T) {
	path := writeReport(t, `{
  "reportFormat": "CTRF",
  "specVersion": "0.0.0",
  "results": {
    "tool": {"name": "ta", "version": "0.0.0-dev"},
    "summary": {"tests": 1, "passed": 0, "failed": 0, "skipped": 1, "pending": 0, "other": 0, "start": 1, "stop": 2},
    "tests": [
      {
        "name": "ADR9001-S-106 reports how many messages it holds",
        "status": "skipped",
        "duration": 4,
        "suite": ["ADR9001-S-106"],
        "tags": ["ADR9001-S-106"],
        "rawStatus": "skipped",
        "extra": {"skip_reason": "conformance-skip: server-too-old: server 2.15.0 is older than 99.0.0"}
      }
    ],
    "extra": {
      "suite_dir": "/work/suite",
      "traces_dir": "/tmp/traces",
      "success": true,
      "has_programmatic_focus": false,
      "packages": [{"package": "example.com/suite", "succeeded": true, "duration_ms": 5, "error": "none really"}]
    }
  }
}`)

	report, err := ctrf.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if report.Results.Tool.Name != "ta" || report.Results.Summary.Skipped != 1 || len(report.Results.Tests) != 1 {
		t.Fatalf("decoded %+v", report.Results)
	}

	test := report.Results.Tests[0]
	if test.Status != ctrf.StatusSkipped || test.RawStatus != "skipped" || test.Tags[0] != "ADR9001-S-106" {
		t.Fatalf("decoded test %+v", test)
	}
	if test.SkipReason() != "conformance-skip: server-too-old: server 2.15.0 is older than 99.0.0" {
		t.Fatalf("got skip reason %q", test.SkipReason())
	}

	pkg := report.Results.Extra.Packages[0]
	if pkg.Package != "example.com/suite" || pkg.Error != "none really" || pkg.DurationMS != 5 {
		t.Fatalf("decoded package %+v", pkg)
	}
}

func TestLoadRefuses(t *testing.T) {
	for name, path := range map[string]string{
		"missing":    filepath.Join(t.TempDir(), "absent.json"),
		"not json":   writeReport(t, "{"),
		"not a CTRF": writeReport(t, `{"reportFormat": "JUnit"}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ctrf.Load(path)
			if !errors.Is(err, ctrf.ErrReport) {
				t.Fatalf("got %v, want ErrReport", err)
			}
		})
	}

	_, err := ctrf.Load(filepath.Join(t.TempDir(), "absent.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want it to wrap os.ErrNotExist", err)
	}
}

func TestSkipReasonWithoutExtra(t *testing.T) {
	got := ctrf.Test{Status: ctrf.StatusSkipped}.SkipReason()
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestLeafText(t *testing.T) {
	for name, tc := range map[string]struct {
		test ctrf.Test
		want string
	}{
		"one container": {
			test: ctrf.Test{Name: "ADR9001-S-101 refuses an overlap", Suite: []string{"ADR9001-S-101"}},
			want: "refuses an overlap",
		},
		"adversarial entry": {
			test: ctrf.Test{Name: "ADR9001-S-107 adversarial adversarial #2: with max_msgs_per_subject 1", Suite: []string{"ADR9001-S-107", "adversarial"}},
			want: "adversarial #2: with max_msgs_per_subject 1",
		},
		"setup node": {
			test: ctrf.Test{Name: "[BeforeSuite]", Suite: []string{"ADR-9001 server"}},
			want: "[BeforeSuite]",
		},
		"no suite": {
			test: ctrf.Test{Name: "stands alone"},
			want: "stands alone",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.test.LeafText()
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
