package version

// IsFirecrackerVersionAtLeast checks if a Firecracker version string is >= minVersion.
// Matches Python's VersionGate.require() logic.
func IsFirecrackerVersionAtLeast(ver, minVersion string) bool {
	vParts, _ := SplitVersionParts(ver)
	mParts, _ := SplitVersionParts(minVersion)
	for i := 0; i < len(vParts) && i < len(mParts); i++ {
		if vParts[i] != mParts[i] {
			return vParts[i] > mParts[i]
		}
	}
	return len(vParts) >= len(mParts)
}
