package match_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"

	"github.com/synadia-labs/traceassert"
	. "github.com/synadia-labs/traceassert/match"
)

func TestMustLoadCapture(t *testing.T) {
	gomega.RegisterTestingT(t)

	dir := t.TempDir()
	writeCapture(t, filepath.Join(dir, "ok.expanded.json"))
	t.Setenv(traceassert.TraceDirEnv, dir)

	t.Run("loads a present capture", func(t *testing.T) {
		tr := MustLoadCapture("ok.expanded.json")
		if len(tr.Events) != 1 {
			t.Fatalf("got %d events, want 1", len(tr.Events))
		}
	})

	t.Run("fails cleanly (no panic) on a missing capture", func(t *testing.T) {
		failure := gomega.InterceptGomegaFailure(func() {
			MustLoadCapture("absent.expanded.json")
		})
		if failure == nil {
			t.Fatal("expected MustLoadCapture to register a Gomega failure for a missing capture")
		}
	})
}

func writeCapture(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tr := &traceassert.Trace{
		Header: traceassert.Header{Version: 1},
		Events: []*traceassert.Event{{Line: 1, Verb: "PUB", Subject: "a.b", Payload: []byte("x")}},
		Footer: &traceassert.Footer{Duration: 1},
	}
	if err := traceassert.WriteExpanded(f, tr); err != nil {
		t.Fatal(err)
	}
}
