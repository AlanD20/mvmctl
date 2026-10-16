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
)

func TestVolumeDetachInterruptedACLObservationAndRecovery(t *testing.T) {
	// Rationale: Revoke may crash on either side of its ACL write while the detach journal remains transitional.
	for _, applied := range []bool{false, true} {
		name := "old ACL"
		if applied {
			name = "next ACL"
		}
		t.Run(name, func(t *testing.T) {
			f := preparePendingVolumeRevoke(t)
			if applied {
				require.NoError(t, setProtectedACL(t.Context(), f.lease.fd, f.lease.record.NextReferences))
			}
			before := f.persisted(t)
			assert.NoError(t, admitVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
			f.comparePersisted(t, before)
			assert.NoError(t, recoverVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
			var got protectedBackingRecord
			require.NoError(t, readProtectedRecord(t.Context(), f.lease.metadataFD, "record.json", &got))
			want := before.Record
			want.Phase, want.References, want.NextReferences = "ready", before.Record.NextReferences, nil
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("exact revoke recovery (-want +got):\n%s", diff)
			}
			assert.NoError(t, verifyProtectedACL(t.Context(), f.lease.fd, want.References))
			assert.NoError(t, f.transaction.proveTargetAbsent(t.Context()))
			completed := f.persisted(t)
			assert.NoError(t, f.lease.Revoke(t.Context(), f.instance.vmID, f.instance.active.userIDs.effective))
			f.comparePersisted(t, completed)
		})
	}
}

func TestVolumeDetachPendingACLRefusalPreservesEvidence(t *testing.T) {
	// Rationale: A detaching receipt must never authorize another grant/revoke or a still-retained drive/target.
	for name, mutate := range map[string]func(*testing.T, *pendingVolumeRevokeFixture){
		"foreign revoke": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			f.lease.record.NextReferences = f.lease.record.References[:1]
		},
		"other grant changed": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			f.lease.record.NextReferences[0].Writable = false
		},
		"execution changed": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			f.instance.active.userIDs.effective++
		},
		"access changed": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			f.transaction.record.Volume.ReadOnly = true
		},
		"drive retained": func(t *testing.T, f *pendingVolumeRevokeFixture) { f.driveAbsent = false },
		"target retained": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.targets, f.transaction.record.basename()), nil, 0600))
		},
		"unrelated ACL": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			require.NoError(t, setProtectedACL(t.Context(), f.lease.fd, nil))
		},
		"growth intent": func(t *testing.T, f *pendingVolumeRevokeFixture) { f.lease.record.GrowBytes = 8192 },
		"cancelled": func(t *testing.T, f *pendingVolumeRevokeFixture) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			f.ctx = ctx
		},
	} {
		t.Run(name, func(t *testing.T) {
			for boundary, operation := range map[string]func(
				context.Context, *protectedBackingLease, *volumeHotplugTransaction, instanceRecord, bool,
			) error{
				"read-only admission": admitVolumeDetachBacking,
				"typed recovery":      recoverVolumeDetachBacking,
			} {
				t.Run(boundary, func(t *testing.T) {
					f := preparePendingVolumeRevoke(t)
					mutate(t, f)
					require.NoError(
						t,
						writeProtectedRecord(t.Context(), f.lease.metadataFD, "record.json", f.lease.record),
					)
					before := f.persisted(t)
					defer f.comparePersisted(t, before)
					assert.Error(t, operation(f.ctx, f.lease, f.transaction, f.instance, f.driveAbsent))
				})
			}
		})
	}
}

func TestVolumeDetachACLWriteCompletedButRecordPublicationFailedCanRetry(t *testing.T) {
	// Rationale: A failed completion write leaves old durable intent with the new ACL, not permanent retry rejection.
	f := preparePendingVolumeRevoke(t)
	before := f.persisted(t)
	metadata := f.lease.metadataFD
	f.lease.metadataFD = -1 // Fail publication after the real ACL update and sync.
	t.Run("publication fails", func(t *testing.T) {
		assert.Error(t, recoverVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
	})
	f.lease.metadataFD = metadata
	got, err := os.ReadFile(filepath.Join(f.metadata, "record.json"))
	require.NoError(t, err)
	if diff := cmp.Diff(before.Bytes, got); diff != "" {
		t.Errorf("failed completion changed durable intent (-want +got):\n%s", diff)
	}
	assert.NoError(t, verifyProtectedACL(t.Context(), f.lease.fd, before.Record.NextReferences))
	require.NoError(t, readProtectedRecord(t.Context(), metadata, "record.json", &f.lease.record))
	assert.NoError(t, admitVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
	assert.NoError(t, recoverVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
	var completed protectedBackingRecord
	require.NoError(t, readProtectedRecord(t.Context(), metadata, "record.json", &completed))
	want := before.Record
	want.Phase, want.References, want.NextReferences = "ready", before.Record.NextReferences, nil
	if diff := cmp.Diff(want, completed); diff != "" {
		t.Errorf("retry changed exact revoke intent (-want +got):\n%s", diff)
	}
}

func TestVolumeDetachACLRecoveryRefusesChangedBacking(t *testing.T) {
	// Rationale: Exact pending intent does not authorize effects on a substituted inode.
	f := preparePendingVolumeRevoke(t)
	f.lease.record.Backing.Inode++
	require.NoError(t, writeProtectedRecord(t.Context(), f.lease.metadataFD, "record.json", f.lease.record))
	before := f.persisted(t)
	defer f.comparePersisted(t, before)
	assert.Error(t, recoverVolumeDetachBacking(t.Context(), f.lease, f.transaction, f.instance, true))
}

type pendingVolumeRevokeFixture struct {
	lease       *protectedBackingLease
	transaction *volumeHotplugTransaction
	instance    instanceRecord
	metadata    string
	targets     string
	driveAbsent bool
	ctx         context.Context
}

func preparePendingVolumeRevoke(t *testing.T) *pendingVolumeRevokeFixture {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("protected backing authority requires root in the disposable runner")
	}
	root := t.TempDir()
	metadata, targets := filepath.Join(root, "metadata"), filepath.Join(root, "volumes")
	require.NoError(t, os.Mkdir(metadata, 0700))
	require.NoError(t, os.Mkdir(targets, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "disk"), make([]byte, 4096), 0600))
	fd, err := unix.Open(filepath.Join(root, "disk"), unix.O_RDWR|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	metadataFD, err := unix.Open(metadata, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	targetFD, err := unix.Open(targets, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	lock, err := acquireProtectedLock(t.Context(), metadataFD)
	require.NoError(t, err)
	identity, err := protectedIdentity(fd)
	require.NoError(t, err)
	vmID, volumeID := strings.Repeat("a", 32), strings.Repeat("b", 64)
	selected := protectedBackingReference{VMID: vmID, ExecutionUID: 20000, Writable: true}
	peer := protectedBackingReference{VMID: strings.Repeat("c", 32), ExecutionUID: 20001, Writable: true}
	lease := &protectedBackingLease{
		fd: fd, metadataFD: metadataFD, lockFD: lock, retained: []int{fd, metadataFD, targetFD, lock},
		record: protectedBackingRecord{
			Version: 1, Kind: protectedBackingVolume, ID: volumeID, Phase: "acl", Backing: identity,
			References: []protectedBackingReference{selected, peer}, NextReferences: []protectedBackingReference{peer},
		},
	}
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	require.NoError(t, setProtectedACL(t.Context(), fd, lease.record.References))
	require.NoError(t, writeProtectedRecord(t.Context(), metadataFD, "record.json", lease.record))
	return &pendingVolumeRevokeFixture{
		lease: lease, metadata: metadata, targets: targets, driveAbsent: true, ctx: t.Context(),
		transaction: &volumeHotplugTransaction{directory: targetFD, record: volumeHotplugRecord{
			VMID:         vmID,
			GuestDriveID: volumeID,
			State:        "detaching",
			Volume:       VMLaunchVolume{ID: volumeID, ExpectedBytes: 4096},
		}},
		instance: instanceRecord{vmID: vmID, active: &instanceActiveIdentity{
			userIDs: processCredentialIdentity{effective: 20000},
		}},
	}
}

type pendingRevokePersistence struct {
	Record  protectedBackingRecord
	Bytes   []byte
	ACL     []byte
	Targets []string
}

func (f *pendingVolumeRevokeFixture) persisted(t *testing.T) pendingRevokePersistence {
	t.Helper()
	var result pendingRevokePersistence
	require.NoError(t, readProtectedRecord(t.Context(), f.lease.metadataFD, "record.json", &result.Record))
	var err error
	result.Bytes, err = os.ReadFile(filepath.Join(f.metadata, "record.json"))
	require.NoError(t, err)
	buffer := make([]byte, 4096)
	n, err := unix.Fgetxattr(f.lease.fd, protectedBackingACL, buffer)
	require.NoError(t, err)
	result.ACL = buffer[:n]
	entries, err := os.ReadDir(f.targets)
	require.NoError(t, err)
	for _, entry := range entries {
		result.Targets = append(result.Targets, entry.Name())
	}
	return result
}

func (f *pendingVolumeRevokeFixture) comparePersisted(t *testing.T, want pendingRevokePersistence) {
	t.Helper()
	if diff := cmp.Diff(want, f.persisted(t)); diff != "" {
		t.Errorf("admission changed pending evidence (-want +got):\n%s", diff)
	}
}
