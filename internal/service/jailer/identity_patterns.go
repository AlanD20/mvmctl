package jailer

import "regexp"

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[-+][A-Za-z0-9.-]+)?$`)
	vmIDPattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	driveIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)
