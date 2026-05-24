package image

import (
	"runtime"

	"mvmctl/internal/infra/model"
)

// Type aliases so existing code continues to compile without rename churn.
// These resolve to the canonical types in infra/model.
type (
	ImageItem    = model.ImageItem
	ImageSpec    = model.ImageSpec
	ImageVersion = model.ImageVersion
)

// DefaultArch returns the default architecture based on the current machine.
// Matches Python's platform.machine().
func DefaultArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return runtime.GOARCH
	}
}
