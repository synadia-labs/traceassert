// Package shaped is the worked example of a suite over a shaped session: the captures
// of a client whose fast-ingest flow ack the proxy dropped and whose connection the
// proxy closed mid-batch, loaded as one session of two connections.
package shaped

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestShapedSession(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Shaped session: a fast-ingest client with a lost ack and a reconnect")
}
