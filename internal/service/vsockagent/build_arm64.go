//go:build arm64

//go:generate touch agent-linux-arm64.gz

package vsockagent

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"log/slog"
	"sync"
)

// Pre-compiled guest agent binary for the host architecture (gzip-compressed).
// Built by scripts/build.sh and embedded at compile time.
//
//go:embed agent-linux-arm64.gz
var agentBinaryGZ []byte

var (
	agentBinaryOnce sync.Once
	agentBinaryData []byte
)

// AgentBinary returns the pre-compiled vsock guest agent binary for the
// host architecture. The binary is embedded as gzip and decompressed once
// on first call, saving ~60% in embedded binary size.
func AgentBinary() []byte {
	agentBinaryOnce.Do(func() {
		if len(agentBinaryGZ) == 0 {
			return
		}
		r, err := gzip.NewReader(bytes.NewReader(agentBinaryGZ))
		if err != nil {
			slog.Error("vsockagent: failed to create gzip reader", "error", err)
			return
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			slog.Error("vsockagent: failed to decompress agent binary", "error", err)
			return
		}
		agentBinaryData = data
	})
	return agentBinaryData
}
