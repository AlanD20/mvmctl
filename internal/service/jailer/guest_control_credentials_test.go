package jailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolauth"
)

func guestControlColdFixture(t *testing.T) (string, *protectedBackingLease) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root-owned credential metadata requires the disposable QA VM")
	}
	directory := t.TempDir()
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(fd)) })
	return directory, &protectedBackingLease{metadataFD: fd, ownerUID: testAuthorityUID,
		record: protectedBackingRecord{Version: 1, Kind: protectedBackingRootfs, ID: testVMID, Owner: testAuthorityUID,
			Phase: "absent", References: []protectedBackingReference{}}}
}

func TestGuestControlColdPreparationRetryRetainsRootSelectionAndBindsImport(t *testing.T) {
	directory, backing := guestControlColdFixture(t)
	first, material, err := prepareColdGuestControl(t.Context(), backing)
	require.NoError(t, err)
	require.Equal(t, testVMID, first.SourceVMID)
	require.Equal(t, "pending", first.Phase)
	again, replayed, err := prepareColdGuestControl(t.Context(), backing)
	require.NoError(t, err)
	require.Equal(t, first, again)
	if diff := cmp.Diff(material, replayed); diff != "" {
		t.Fatalf("retry replaced immutable material (-want +got):\n%s", diff)
	}
	info, err := os.Stat(filepath.Join(directory, guestControlCredentialLeaf(first.Version)))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	lease := &VMProvisionRootfsLease{vmProvisionRootfsLeaseState: &vmProvisionRootfsLeaseState{backing: backing}}
	require.NoError(t, lease.CheckGuestControlAgentMaterial(t.Context(), material.AgentMaterial))
	_, foreign, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	foreignBytes, err := foreign.Encode()
	require.NoError(t, err)
	require.Error(t, lease.CheckGuestControlAgentMaterial(t.Context(), foreignBytes))
	// Guest bytes can be changed by the operator. Binding imports root's EXPECTED pin, never those bytes.
	backing.record.Phase = "ready"
	require.NoError(t, bindColdGuestControl(t.Context(), backing))
	var published protectedBackingRecord
	require.NoError(t, readProtectedRecord(t.Context(), backing.metadataFD, "record.json", &published))
	require.Equal(t, &guestControlBackingReceipt{testVMID, first.Version, "bound"}, published.GuestControl)
	unchanged, err := os.ReadFile(filepath.Join(directory, "record.json"))
	require.NoError(t, err)
	require.NoError(t, bindColdGuestControl(t.Context(), backing))
	after, err := os.ReadFile(filepath.Join(directory, "record.json"))
	require.NoError(t, err)
	require.Equal(t, unchanged, after)
	observed, err := readGuestControlCredential(t.Context(), backing.metadataFD, testAuthorityUID, *published.GuestControl)
	require.NoError(t, err)
	require.Equal(t, material.RootMaterial, observed.RootMaterial)
}

func TestGuestControlPreparationPublicationFailureReturnsNoUsableBinding(t *testing.T) {
	directory, backing := guestControlColdFixture(t)
	// A foreign/partial next record must be preserved, never repaired into trusted credential authority.
	next := filepath.Join(directory, guestControlBindingLeaf+".next")
	require.NoError(t, os.WriteFile(next, []byte("foreign bytes"), 0600))
	_, _, err := prepareColdGuestControl(t.Context(), backing)
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(directory, guestControlBindingLeaf))
	require.True(t, os.IsNotExist(err))
	kept, err := os.ReadFile(next)
	require.NoError(t, err)
	require.Equal(t, "foreign bytes", string(kept))
}

func TestGuestControlImmutableRecordRejectsRebindingAndRetirementPreservesForeignLeaf(t *testing.T) {
	directory, backing := guestControlColdFixture(t)
	receipt, original, err := prepareColdGuestControl(t.Context(), backing)
	require.NoError(t, err)
	wrong := receipt
	wrong.SourceVMID = strings.Repeat("e", 32)
	_, err = readGuestControlCredential(t.Context(), backing.metadataFD, testAuthorityUID, wrong)
	require.Error(t, err)
	changed := original
	changed.SourceVMID = wrong.SourceVMID
	require.NoError(t, writeProtectedRecord(
		t.Context(), backing.metadataFD, guestControlCredentialLeaf(receipt.Version), changed,
	))
	_, err = readGuestControlCredential(t.Context(), backing.metadataFD, testAuthorityUID, receipt)
	require.Error(t, err)
	_, err = admitGuestControlRetirementLeaf(t.Context(), backing.metadataFD,
		guestControlCredentialLeaf(receipt.Version), testAuthorityUID, testVMID)
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(directory, guestControlCredentialLeaf(receipt.Version)))
	require.NoError(t, err)
}

func TestGuestControlWholeCacheRetirementAdmitsPendingAndImmutableLeaves(t *testing.T) {
	directory, backing := guestControlColdFixture(t)
	receipt, _, err := prepareColdGuestControl(t.Context(), backing)
	require.NoError(t, err)
	plan := &protectedCacheCleanupPlan{cache: &managedCacheLease{ownerUID: testAuthorityUID}}
	ordinary, err := plan.admitGuestControlMetadata(t.Context(), backing.metadataFD, testVMID,
		[]string{guestControlBindingLeaf, guestControlCredentialLeaf(receipt.Version), "lock"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeProtectedFDs(plan.retained)) })
	require.Equal(t, []string{"lock"}, ordinary)
	require.NoError(t, plan.retireGuestControlCredentials(t.Context()))
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestGuestControlPrepareResultDoesNotAcceptHostMaterialOrAnotherColdSource(t *testing.T) {
	host, agent, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	hostBytes, err := host.Encode()
	require.NoError(t, err)
	agentBytes, err := agent.Encode()
	require.NoError(t, err)
	request := GuestControlPrepareRequest{VMID: testVMID, CacheRoot: "/home/operator/cache"}
	result := GuestControlPrepareResult{VMID: testVMID, NeedsProvision: true,
		Reference: GuestControlReference{testVMID, strings.Repeat("a", 64)}, AgentMaterial: agentBytes}
	require.NoError(t, result.Validate(request))
	result.AgentMaterial = hostBytes
	err = result.Validate(request)
	require.Error(t, err)
	require.NotContains(t, err.Error(), string(hostBytes))
	result.AgentMaterial = agentBytes
	result.Reference.SourceVMID = strings.Repeat("e", 32)
	require.Error(t, result.Validate(request))
	result.NeedsProvision, result.AgentMaterial = false, nil
	require.NoError(t, result.Validate(request), "a bound root snapshot lineage may retain its original credential source")
	_, err = NewGuestControlCredentialServer().Prepare(context.Background(), 0, 0, request)
	require.Error(t, err, "a root process is not an authenticated non-root operator")
}

func TestGuestControlCacheRetirementPreservesSnapshotReferencesThenRetiresSecrets(t *testing.T) {
	fixture := newProtectedCacheCleanupFixture(t)
	id := fixture.objects[protectedBackingRootfs].ID
	path := filepath.Join(fixture.anchorPath, protectedBackingRootfs, id)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	backing := &protectedBackingLease{metadataFD: fd, ownerUID: fixture.cache.ownerUID,
		record: fixture.objects[protectedBackingRootfs]}
	receipt, _, err := prepareColdGuestControl(t.Context(), backing)
	require.NoError(t, err)
	capture := filepath.Join(fixture.cachePath, protectedBackingDirectory, "snapshots", strings.Repeat("c", 64))
	require.NoError(t, os.Mkdir(capture, 0700))
	require.Error(t, retireProtectedCache(t.Context(), fixture.cache, fixture.anchor, fixture.owner))
	_, err = os.Stat(filepath.Join(path, guestControlCredentialLeaf(receipt.Version)))
	require.NoError(t, err, "a retained capture must preserve its credential lineage")
	require.NoError(t, os.Remove(capture))
	require.NoError(t, retireProtectedCache(t.Context(), fixture.cache, fixture.anchor, fixture.owner))
	_, err = os.Stat(filepath.Join(path, guestControlCredentialLeaf(receipt.Version)))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(path, guestControlBindingLeaf))
	require.True(t, os.IsNotExist(err))
}

func TestGuestControlMemorySnapshotRecordRetainsOnlyImmutableLineageReference(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("snapshot authority metadata requires the disposable QA VM")
	}
	fixture := newSnapshotRestoreBackingFixture(t)
	source := testManagedLaunchDeviceAuthority()
	makeNetworklessLaunchDeviceAuthority(&source)
	source.VMID, source.Rootfs.logicalID = fixture.prepared.targetVMID, fixture.prepared.targetVMID
	source.KernelID = fixture.prepared.metadata.Source.KernelID
	source.KernelBacking.logicalID, source.KernelBacking.bytes = source.KernelID, 6
	source.Rootfs.cacheIdentity, source.KernelBacking.cacheIdentity = fixture.cache.identity, fixture.cache.identity
	var err error
	source.Rootfs.identity, err = stableManagedVMConfigStat(
		t.Context(), fixture.cache.configDeps, fixture.prepared.artifacts["rootfs.img"],
	)
	require.NoError(t, err)
	source.KernelBacking.identity, err = stableManagedVMConfigStat(
		t.Context(), fixture.cache.configDeps, fixture.prepared.artifacts["kernel"],
	)
	require.NoError(t, err)
	source.Release.Version = infra.QualifiedVMLaunchFirecrackerVersion
	source.Config.MachineConfig = managedFirecrackerMachineConfig{VCPUCount: 1, MemSizeMiB: 256}
	memory, err := managedSnapshotMemoryBytes(256)
	require.NoError(t, err)
	version := strings.Repeat("e", 64)
	record := snapshotCaptureRecord{Schema: 1, SnapshotID: strings.Repeat("d", 64), OwnerUID: 1000,
		Mode: "full", Source: source, GuestControl: &guestControlBackingReceipt{strings.Repeat("b", 32), version, "bound"}}
	for index, artifact := range []struct {
		name  string
		bytes uint64
	}{
		{"rootfs.img", source.Rootfs.bytes}, {"kernel", source.KernelBacking.bytes}, {"memory", memory}, {"vmstate", 1},
	} {
		record.Artifacts = append(record.Artifacts, snapshotArtifact{Name: artifact.name, Bytes: artifact.bytes,
			SHA256: strings.Repeat("f", 64), Device: 1, Inode: uint64(index + 1)})
	}
	require.NoError(t, record.validate())
	directory := t.TempDir()
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	entry := &snapshotStoreEntry{snapshotID: record.SnapshotID, directoryFD: fd}
	require.NoError(t, entry.publish(t.Context(), record))
	observed, err := entry.readRecord(t.Context(), record.OwnerUID)
	require.NoError(t, err)
	require.Equal(t, record.GuestControl, observed.GuestControl)
	require.NotEqual(t, source.VMID, observed.GuestControl.SourceVMID,
		"a captured restored VM retains the original root-authorized credential lineage")
	observed.GuestControl.Phase = "pending"
	require.Error(t, observed.validate())
}
