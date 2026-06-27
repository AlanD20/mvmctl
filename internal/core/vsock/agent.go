package vsock

import "mvmctl/internal/service/vsockagent"

// AgentBinary returns the pre-compiled vsock guest agent binary for the
// host architecture. The binary is embedded as zstd and decompressed on
// first call. Built by scripts/build.sh.
func AgentBinary() []byte {
	return vsockagent.AgentBinary()
}
