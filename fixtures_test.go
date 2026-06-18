package traceassert

import (
	"path/filepath"
	"testing"

	"github.com/synadia-labs/traceassert/tracegen"
)

// fastIngestBuilder builds a minimal, conformant fast-ingest batch for one batch uuid.
func fastIngestBuilder() *tracegen.Builder {
	const inbox = "_INBOX.batch1"
	b := tracegen.New("client")
	b.Info(`{"server_id":"test"}`)
	b.Connect("{}")
	b.Sub(inbox+".>", "1")                                                   // old-style inbox
	b.Pub("ORDERS", inbox+".10.ok.1.0.$FI", []byte("m1"))                    // start, seq 1
	b.MsgString(inbox+".ack", "1", `{"type":"ack","seq":1,"msgs":15}`)       // first flow ack
	b.Pub("ORDERS", inbox+".10.ok.2.1.$FI", []byte("m2"))                    // append, seq 2
	b.Pub("ORDERS", inbox+".10.ok.3.1.$FI", []byte("m3"))                    // append, seq 3
	b.Pub("ORDERS", inbox+".10.ok.4.2.$FI", []byte("m4"))                    // commit+store
	b.MsgString(inbox+".ack", "1", `{"stream":"ORDERS","seq":42,"count":4}`) // final pub ack
	return b
}

// loadExpanded renders b as an expanded trace and loads it back via LoadExpanded —
// the parser-free path the assertion side relies on.
func loadExpanded(t *testing.T, b *tracegen.Builder) *Trace {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.expanded.json")
	if err := b.WriteExpandedFile(path); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tr, err := LoadExpanded(path)
	if err != nil {
		t.Fatalf("LoadExpanded: %v", err)
	}
	return tr
}
