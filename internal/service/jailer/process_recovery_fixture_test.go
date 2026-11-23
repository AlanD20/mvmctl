package jailer

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

// newActiveRecoveryIdentityFixture isolates process-observation tests from root-only
// account allocation and protected-backing cleanup. It supplies a valid active
// record, not proof that a root lifecycle created that record; that composition
// requires the disposable runner VM.
func newActiveRecoveryIdentityFixture(t *testing.T, relayPresent bool) (instanceRecord, *fakeProcessIdentityDeps) {
	t.Helper()

	firecrackerContent := bytes.Repeat([]byte{'F'}, int(trustedReleaseExecutableMinBytes))
	firecrackerSum := sha256.Sum256(firecrackerContent)
	firecrackerDigest := fmt.Sprintf("%x", firecrackerSum)
	relayContent := []byte("exact pinned receiver executable")
	relaySum := sha256.Sum256(relayContent)
	relayDigest := fmt.Sprintf("%x", relaySum)

	firecrackerNamespace := instanceObjectIdentity{device: 4, inode: 4026533001}
	firecrackerNetworkNamespace := instanceObjectIdentity{device: 4, inode: 4026533101}
	jailRoot := instanceObjectIdentity{device: 259, inode: 11223344}
	firecrackerStat := executableStat(259, 11223345, firecrackerContent)
	hostNamespace := instanceObjectIdentity{device: 4, inode: 4026531840}
	hostNetworkNamespace := instanceObjectIdentity{device: 4, inode: 4026531992}
	hostRoot := instanceObjectIdentity{device: 8, inode: 2}
	relayStat := executableStat(8, 33445566, relayContent)

	record := testActiveInstanceRecord(relayPresent)
	record.launch.release.firecrackerSHA256 = firecrackerDigest
	record.active.mountNamespace = firecrackerNamespace
	record.active.jailRoot = jailRoot
	record.active.executable = instanceFirecrackerExecutableIdentity{
		device: uint64(firecrackerStat.Dev), inode: firecrackerStat.Ino,
		sizeBytes: uint64(firecrackerStat.Size), mode: firecrackerStat.Mode,
		uid: firecrackerStat.Uid, gid: firecrackerStat.Gid, links: firecrackerStat.Nlink,
		manifestSHA256: firecrackerDigest,
	}
	if relayPresent {
		record.active.relay.mountNamespace = hostNamespace
		record.active.relay.root = hostRoot
		record.active.relay.executable = instanceRelayExecutableIdentity{
			device: uint64(relayStat.Dev), inode: relayStat.Ino,
			sizeBytes: uint64(relayStat.Size), mode: relayStat.Mode,
			uid: relayStat.Uid, gid: relayStat.Gid, links: relayStat.Nlink,
			sha256: relayDigest,
		}
	}
	require.NoError(t, validateInstanceRecord(record))

	receiverIdentity := system.PinnedExecutableIdentity{
		Device: uint64(relayStat.Dev), Inode: relayStat.Ino, SizeBytes: uint64(relayStat.Size),
		Mode: relayStat.Mode, UID: relayStat.Uid, GID: relayStat.Gid, Links: relayStat.Nlink,
		SHA256: relayDigest,
	}
	const observations = 64
	fake := &fakeProcessIdentityDeps{
		bootIDs: slices.Repeat([]string{processTestHostBootID}, observations),
		alive: map[int][]bool{
			processTestFirecrackerPIDFD: slices.Repeat([]bool{true}, observations),
			processTestRelayPIDFD:       slices.Repeat([]bool{true}, observations),
		},
		processDirectories: map[int]int{
			processTestFirecrackerPID: processTestFirecrackerDirFD,
			processTestRelayPID:       processTestRelayDirFD,
		},
		metadata: map[processMetadataTestKey][][]byte{
			{processTestFirecrackerDirFD, processStatMetadata}: slices.Repeat(
				[][]byte{
					[]byte(testProcessStat(processTestFirecrackerPID, processTestFirecrackerTicks, "S")),
				},
				observations,
			),
			{processTestFirecrackerDirFD, processStatusMetadata}: slices.Repeat(
				[][]byte{[]byte(testProcessStatus(testExecutionUID, testExecutionGID, ""))}, observations,
			),
			{processTestFirecrackerDirFD, processCgroupMetadata}: slices.Repeat(
				[][]byte{[]byte("0::/" + infra.JailerCgroupParent + "/" + testVMID + "\n")}, observations,
			),
			{processTestRelayDirFD, processStatMetadata}: slices.Repeat(
				[][]byte{[]byte(testProcessStat(processTestRelayPID, processTestRelayTicks, "S"))}, observations,
			),
			{processTestRelayDirFD, processStatusMetadata}: slices.Repeat(
				[][]byte{[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, ""))}, observations,
			),
			{processTestRelayDirFD, processCgroupMetadata}: slices.Repeat(
				[][]byte{[]byte("0::/user.slice/user-1000.slice\n")}, observations,
			),
		},
		processObjects: map[processObjectTestKey][]int{
			{processTestFirecrackerDirFD, processMountNamespaceObject}: slices.Repeat(
				[]int{111, 114, 117},
				observations,
			),
			{processTestFirecrackerDirFD, processNetworkNamespaceObject}: slices.Repeat(
				[]int{141, 142, 143},
				observations,
			),
			{processTestFirecrackerDirFD, processRootObject}: slices.Repeat(
				[]int{112, 115, 118},
				observations,
			),
			{processTestFirecrackerDirFD, processExecutableObject}: slices.Repeat(
				[]int{113, 116, 119},
				observations,
			),
			{processTestRelayDirFD, processMountNamespaceObject}:   slices.Repeat([]int{121, 124}, observations),
			{processTestRelayDirFD, processNetworkNamespaceObject}: slices.Repeat([]int{151, 152}, observations),
			{processTestRelayDirFD, processRootObject}:             slices.Repeat([]int{122, 125}, observations),
			{processTestRelayDirFD, processExecutableObject}:       slices.Repeat([]int{123, 126}, observations),
		},
		hostNamespaces:       slices.Repeat([]int{130, 132}, observations),
		hostRoots:            slices.Repeat([]int{131, 133}, observations),
		stats:                make(map[int][]unix.Stat_t),
		statIndex:            make(map[int]int),
		contents:             make(map[int][]byte),
		flags:                make(map[int]int),
		closed:               make(map[int]int),
		closeErrors:          make(map[int]error),
		destinationOpenFlags: make(map[int]int),
		receiverIdentity:     receiverIdentity,
		clock:                time.Now(),
	}
	for _, fd := range []int{111, 114, 117} {
		fake.stats[fd] = repeatProcessStat(objectStat(firecrackerNamespace, unix.S_IFREG|0444), observations)
	}
	for _, fd := range []int{141, 142, 143} {
		fake.stats[fd] = repeatProcessStat(objectStat(firecrackerNetworkNamespace, unix.S_IFREG|0444), observations)
	}
	for _, fd := range []int{112, 115, 118} {
		fake.stats[fd] = repeatProcessStat(objectStat(jailRoot, unix.S_IFDIR|0755), observations)
	}
	for _, fd := range []int{113, 116, 119} {
		fake.stats[fd] = repeatProcessStat(firecrackerStat, observations)
		fake.contents[fd] = firecrackerContent
	}
	for _, fd := range []int{121, 124, 130, 132} {
		fake.stats[fd] = repeatProcessStat(objectStat(hostNamespace, unix.S_IFREG|0444), observations)
	}
	for _, fd := range []int{151, 152} {
		fake.stats[fd] = repeatProcessStat(objectStat(hostNetworkNamespace, unix.S_IFREG|0444), observations)
	}
	for _, fd := range []int{122, 125, 131, 133} {
		fake.stats[fd] = repeatProcessStat(objectStat(hostRoot, unix.S_IFDIR|0755), observations)
	}
	for _, fd := range []int{123, 126} {
		fake.stats[fd] = repeatProcessStat(relayStat, observations)
		fake.contents[fd] = relayContent
	}
	return record, fake
}
