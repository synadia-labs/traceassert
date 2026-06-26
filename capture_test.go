package traceassert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturePath(t *testing.T) {
	t.Run("defaults to testdata", func(t *testing.T) {
		t.Setenv(TraceDirEnv, "")
		got := CapturePath("cap.json")
		want := filepath.Join("testdata", "cap.json")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("TRACE_DIR overrides testdata", func(t *testing.T) {
		t.Setenv(TraceDirEnv, "/traces")
		got := CapturePath("cap.json")
		want := filepath.Join("/traces", "cap.json")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLoadCapture(t *testing.T) {
	dir := t.TempDir()
	writeTestCapture(t, filepath.Join(dir, "ok.expanded.json"), true)
	writeTestCapture(t, filepath.Join(dir, "trunc.expanded.json"), false)
	t.Setenv(TraceDirEnv, dir)

	t.Run("loads a complete capture via TRACE_DIR", func(t *testing.T) {
		tr, err := LoadCapture("ok.expanded.json")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tr.Events) != 1 {
			t.Fatalf("got %d events, want 1", len(tr.Events))
		}
	})

	t.Run("truncated capture is an error", func(t *testing.T) {
		_, err := LoadCapture("trunc.expanded.json")
		if err == nil {
			t.Fatal("expected an error for a truncated capture")
		}
		if !strings.Contains(err.Error(), "truncated") {
			t.Errorf("error %q does not mention truncation", err)
		}
	})

	t.Run("missing capture is an error", func(t *testing.T) {
		_, err := LoadCapture("absent.expanded.json")
		if err == nil {
			t.Fatal("expected an error for a missing capture")
		}
	})
}

// writeTestCapture writes a minimal expanded trace to path. A complete trace gets a
// footer; an incomplete one omits it (which LoadExpanded reports as truncated).
func writeTestCapture(t *testing.T, path string, complete bool) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tr := &Trace{
		Header: Header{Version: 1},
		Events: []*Event{{Line: 1, Verb: "PUB", Subject: "a.b", Payload: []byte("x")}},
	}
	if complete {
		tr.Footer = &Footer{Duration: 1}
	}
	if err := WriteExpanded(f, tr); err != nil {
		t.Fatal(err)
	}
}
