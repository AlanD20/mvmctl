package jailer

import (
	"fmt"

	"mvmctl/internal/infra"
)

const (
	trustedReleaseFirecrackerLeaf = "firecracker"
	trustedReleaseJailerLeaf      = "jailer"
	trustedReleaseManifestLeaf    = "manifest.json"
)

type trustedReleaseSource struct {
	slot              releaseSlot
	archiveName       string
	archiveURL        string
	checksumURL       string
	archiveRoot       string
	firecrackerMember string
	jailerMember      string
}

// CRITICAL: Every source identity is derived from the validated release slot. No caller-supplied URL, archive name, or
// member name crosses the privileged release boundary.
func newTrustedReleaseSource(slot releaseSlot) (trustedReleaseSource, error) {
	if err := validateReleaseSlotValue(slot); err != nil {
		return trustedReleaseSource{}, instanceValidationError(err.Error())
	}

	releaseTag := "v" + slot.version
	artifactSuffix := releaseTag + "-" + slot.architecture
	firecrackerName := "firecracker-" + artifactSuffix
	jailerName := "jailer-" + artifactSuffix
	archiveName := firecrackerName + ".tgz"
	archiveRoot := "release-" + artifactSuffix
	archiveURL := fmt.Sprintf(
		"%s/%s/%s",
		infra.FirecrackerGithubDownloadURL,
		releaseTag,
		archiveName,
	)

	return trustedReleaseSource{
		slot:              slot,
		archiveName:       archiveName,
		archiveURL:        archiveURL,
		checksumURL:       archiveURL + ".sha256.txt",
		archiveRoot:       archiveRoot,
		firecrackerMember: archiveRoot + "/" + firecrackerName,
		jailerMember:      archiveRoot + "/" + jailerName,
	}, nil
}
