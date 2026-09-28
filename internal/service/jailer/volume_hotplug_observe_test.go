package jailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

func TestVolumeObservationPreservesPendingJournal(t *testing.T) {
	// Rationale: Read-only observation must not erase evidence of interrupted publication.
	directory := t.TempDir()
	name := strings.Repeat("a", 64) + ".pending"
	payload := []byte("interrupted publication evidence")
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), payload, 0600))
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unix.Close(fd)) })
	_, err = readVolumeHotplugRecords(context.Background(), fd, instanceRecord{vmID: testVMID})
	assert.Error(t, err)
	got, err := os.ReadFile(filepath.Join(directory, name))
	require.NoError(t, err)
	if diff := cmp.Diff(payload, got); diff != "" {
		t.Errorf("observation changed pending evidence (-want +got):\n%s", diff)
	}
}

func TestRestoredVolumeUsesCapturedGuestDrive(t *testing.T) {
	// Rationale: A restored logical ID must never replace its captured guest drive or mount ID.
	logicalID, guestID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	record := volumeHotplugRecord{
		GuestDriveID: guestID,
		Volume: VMLaunchVolume{
			ID:            logicalID,
			Format:        model.VolumeFormatRaw,
			ExpectedBytes: 4096,
			CacheType:     model.CacheTypeUnsafe,
		},
	}
	guestSelection := record.Volume
	guestSelection.ID = guestID
	assert.True(t, volumeRecordDriveMatches(volumeDrive(guestSelection), record))
	assert.False(t, volumeRecordDriveMatches(volumeDrive(record.Volume), record))
	assert.Equal(t, guestID+".raw", record.basename())
}

func TestFirecrackerVolumeSerialMatchesDecimalIdentity(t *testing.T) {
	// Rationale: Logical IDs are not Firecracker's virtio serial, and truncation can collide.
	for name, tc := range map[string]struct {
		device, rdev, inode uint64
		want                string
	}{
		"regular backing":        {device: 2049, inode: 123456, want: "20490123456"},
		"decimal components":     {device: 12, rdev: 34, inode: 56, want: "123456"},
		"twenty byte truncation": {device: 1234567890123456789, inode: 998, want: "12345678901234567890"},
	} {
		t.Run(
			name,
			func(t *testing.T) { assert.Equal(t, tc.want, firecrackerVolumeSerial(tc.device, tc.rdev, tc.inode)) },
		)
	}
}
