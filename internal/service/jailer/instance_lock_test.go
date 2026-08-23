package jailer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReleaseLockNameUsesCanonicalStoreSlot(t *testing.T) {
	t.Parallel()

	release := testLaunchRegistration().release
	sameSlot := testAlternateReleaseIdentity()
	differentVersion := release
	differentVersion.version = "1.17.0"
	differentArchitecture := release
	differentArchitecture.architecture = "aarch64"

	assert.Equal(
		t,
		releaseLockName(releaseSlotForIdentity(release)),
		releaseLockName(releaseSlotForIdentity(sameSlot)),
	)
	assert.NotEqual(
		t,
		releaseLockName(releaseSlotForIdentity(release)),
		releaseLockName(releaseSlotForIdentity(differentVersion)),
	)
	assert.NotEqual(
		t,
		releaseLockName(releaseSlotForIdentity(release)),
		releaseLockName(releaseSlotForIdentity(differentArchitecture)),
	)
}

func testAlternateReleaseIdentity() releaseIdentity {
	release := testLaunchRegistration().release
	release.firecrackerSHA256 = strings.Repeat("a", 64)
	release.jailerSHA256 = strings.Repeat("b", 64)
	return release
}
