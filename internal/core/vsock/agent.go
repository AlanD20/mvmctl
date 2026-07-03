package vsock

import "mvmctl/internal/service/agent"

// AgentBinary returns the pre-compiled vsock guest agent binary for the
// host architecture. The binary is embedded as zstd and decompressed on
// first call. Built by scripts/build.sh.
func AgentBinary() []byte {
	return agent.AgentBinary()
}
