// Package hostpolicy owns the fixed managed sudoers marker policy grammar.
package hostpolicy

import (
	"fmt"
	"strings"

	"mvmctl/internal/infra"
)

// InvalidMarkerPolicyLine returns the first rejected line, or zero for the exact marker-only policy.
// Ordinary comments and blank lines are allowed; includes, duplicate grants and raw commands are not.
func InvalidMarkerPolicyLine(content string) (int, string) {
	grant := fmt.Sprintf("%%%s ALL=(root) NOPASSWD: %s %s *",
		infra.MVMUnixGroup, infra.SystemBinaryPath, infra.PrivilegedProtocolMarker)
	found := false
	for index, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") &&
			!strings.HasPrefix(line, "#include") && !strings.HasPrefix(line, "#includedir") {
			continue
		}
		if line != grant || found {
			return index + 1, line
		}
		found = true
	}
	if !found {
		return 1, "missing marker-only grant"
	}
	return 0, ""
}
