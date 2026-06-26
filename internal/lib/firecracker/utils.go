package firecracker

import "slices"

// SupportedArches lists CPU architectures that Firecracker supports.
var SupportedArches = []string{"x86_64", "amd64", "aarch64", "arm64"}

// SupportsArch returns true if the given architecture is supported by Firecracker.
func SupportsArch(arch string) bool {
	return slices.Contains(SupportedArches, arch)
}
