//go:build arm64

//go:generate touch agent-linux-arm64.zst

package vsockagent

import (
	"bytes"
	_ "embed"
	"io"
	"log/slog"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Pre-compiled guest agent binary for the host architecture (zstd-compressed).
// Built by scripts/build.sh and embedded at compile time.
//
//go:embed agent-linux-arm64.zst
var agentBinaryZST []byte

var (
	agentBinaryOnce sync.Once
	agentBinaryData []byte
)

// AgentBinary returns the pre-compiled vsock guest agent binary for the
// host architecture. The binary is embedded as zstd and decompressed once
// on first call, saving ~60% in embedded binary size.
func AgentBinary() []byte {
	agentBinaryOnce.Do(func() {
		if len(agentBinaryZST) == 0 {
			return
		}
		r, err := zstd.NewReader(bytes.NewReader(agentBinaryZST))
		if err != nil {
			slog.Error("vsockagent: failed to create zstd reader", "error", err)
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
