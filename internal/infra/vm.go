package infra

import "strings"

// ParseVMPath parses a "vm:path" string into VM identifier and remote path.
// If no colon is found, returns empty VM identifier and the original path.
func ParseVMPath(path string) (vmIdent, remotePath string) {
	vmIdent, remotePath, found := strings.Cut(path, ":")
	if !found {
		return "", path
	}
	return vmIdent, remotePath
}
