package jailer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

const (
	processTestFirecrackerPID   = 4242
	processTestRelayPID         = 4343
	processTestFirecrackerTicks = 987654
	processTestRelayTicks       = 876543
	processTestFirecrackerPIDFD = 100
	processTestRelayPIDFD       = 101
	processTestFirecrackerDirFD = 110
	processTestRelayDirFD       = 120
)

const (
	processTestHostBootID = "123e4567-e89b-42d3-a456-426614174000"
	processOtherBootID    = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
)

type processIdentityFixture struct {
	launch                      *launchLease
	processSource               *blockedLaunchProcesses
	fake                        *fakeProcessIdentityDeps
	firecrackerNamespace        instanceObjectIdentity
	firecrackerNetworkNamespace instanceObjectIdentity
	jailRoot                    instanceObjectIdentity
	firecrackerExecutable       processExecutableDescriptorIdentity
	hostNamespace               instanceObjectIdentity
	hostRoot                    instanceObjectIdentity
	relayExecutable             processExecutableDescriptorIdentity
	receiverIdentity            system.PinnedExecutableIdentity
	firecrackerDigest           string
}

func newProcessIdentityFixture(t *testing.T, relayPresent bool) *processIdentityFixture {
	t.Helper()

	authority, _ := newTestInstanceAuthority(t)
	prepared := prepareTestRelease(t, authority)
	preparedSnapshot := prepared.snapshot()
	firecrackerContent := readProcessTestDescriptor(
		t,
		preparedSnapshot.firecrackerFD,
		preparedSnapshot.manifest.firecracker.sizeBytes,
	)
	processFixture := newBlockedProcessFixture(relayPresent)
	processes, err := processFixture.admit(t.Context())
	require.NoError(t, err)
	launch, err := prepared.registerLaunching(
		t.Context(),
		testInstanceCaller(),
		testLaunchIntent(relayPresent),
		processes,
	)
	require.NoError(t, err)

	relayContent := []byte("exact pinned receiver executable")
	relayDigest := sha256.Sum256(relayContent)
	firecrackerNamespace := instanceObjectIdentity{device: 4, inode: 4026533001}
	firecrackerNetworkNamespace := instanceObjectIdentity{device: 4, inode: 4026533101}
	hostNamespace := instanceObjectIdentity{device: 4, inode: 4026531840}
	hostNetworkNamespace := instanceObjectIdentity{device: 4, inode: 4026531992}
	jailRoot := instanceObjectIdentity{device: 259, inode: 11223344}
	hostRoot := instanceObjectIdentity{device: 8, inode: 2}
	firecrackerStat := executableStat(259, 11223345, firecrackerContent)
	relayStat := executableStat(8, 33445566, relayContent)
	firecrackerExecutable := processExecutableDescriptorIdentityFromTestStat(firecrackerStat)
	relayExecutable := processExecutableDescriptorIdentityFromTestStat(relayStat)
	receiverIdentity := system.PinnedExecutableIdentity{
		Device: uint64(relayStat.Dev), Inode: relayStat.Ino, SizeBytes: uint64(relayStat.Size), Mode: relayStat.Mode,
		UID: relayStat.Uid, GID: relayStat.Gid, Links: relayStat.Nlink, SHA256: fmt.Sprintf("%x", relayDigest[:]),
	}

	fake := &fakeProcessIdentityDeps{
		bootIDs: []string{processTestHostBootID, processTestHostBootID, processTestHostBootID},
		alive: map[int][]bool{
			processTestFirecrackerPIDFD: {true, true},
			processTestRelayPIDFD:       {true, true},
		},
		processDirectories: map[int]int{
			processTestFirecrackerPID: processTestFirecrackerDirFD,
			processTestRelayPID:       processTestRelayDirFD,
		},
		metadata: map[processMetadataTestKey][][]byte{
			{processTestFirecrackerDirFD, processStatMetadata}: {
				[]byte(testProcessStat(processTestFirecrackerPID, processTestFirecrackerTicks, "S")),
				[]byte(testProcessStat(processTestFirecrackerPID, processTestFirecrackerTicks, "S")),
				[]byte(testProcessStat(processTestFirecrackerPID, processTestFirecrackerTicks, "S")),
			},
			{processTestFirecrackerDirFD, processStatusMetadata}: {
				[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")),
				[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")),
				[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")),
			},
			{processTestFirecrackerDirFD, processCgroupMetadata}: {
				[]byte("0::/" + infra.JailerCgroupParent + "/" + testVMID + "\n"),
				[]byte("0::/" + infra.JailerCgroupParent + "/" + testVMID + "\n"),
				[]byte("0::/" + infra.JailerCgroupParent + "/" + testVMID + "\n"),
			},
			{processTestRelayDirFD, processStatMetadata}: {
				[]byte(testProcessStat(processTestRelayPID, processTestRelayTicks, "S")),
				[]byte(testProcessStat(processTestRelayPID, processTestRelayTicks, "S")),
			},
			{processTestRelayDirFD, processStatusMetadata}: {
				[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")),
				[]byte(testProcessStatus(testAuthorityUID, testAuthorityGID, "")),
			},
			{processTestRelayDirFD, processCgroupMetadata}: {
				[]byte("0::/user.slice/user-1000.slice\n"),
				[]byte("0::/user.slice/user-1000.slice\n"),
			},
		},
		processObjects: map[processObjectTestKey][]int{
			{processTestFirecrackerDirFD, processMountNamespaceObject}:   {111, 114, 117},
			{processTestFirecrackerDirFD, processNetworkNamespaceObject}: {141, 142, 143},
			{processTestFirecrackerDirFD, processRootObject}:             {112, 115, 118},
			{processTestFirecrackerDirFD, processExecutableObject}:       {113, 116, 119},
			{processTestRelayDirFD, processMountNamespaceObject}:         {121, 124},
			{processTestRelayDirFD, processNetworkNamespaceObject}:       {151, 152},
			{processTestRelayDirFD, processRootObject}:                   {122, 125},
			{processTestRelayDirFD, processExecutableObject}:             {123, 126},
		},
		hostNamespaces:       []int{130, 132},
		hostRoots:            []int{131, 133},
		stats:                make(map[int][]unix.Stat_t),
		statIndex:            make(map[int]int),
		contents:             map[int][]byte{203: firecrackerContent},
		flags:                make(map[int]int),
		closed:               make(map[int]int),
		closeErrors:          make(map[int]error),
		destinationOpenFlags: make(map[int]int),
		receiverIdentity:     receiverIdentity,
		clock:                time.Now(),
	}
	preSealExecutable := firecrackerStat
	preSealExecutable.Uid = testAuthorityUID
	preSealExecutable.Gid = testAuthorityGID
	fake.stats[111] = []unix.Stat_t{objectStat(firecrackerNamespace, unix.S_IFREG|0444)}
	fake.stats[141] = []unix.Stat_t{objectStat(firecrackerNetworkNamespace, unix.S_IFREG|0444)}
	fake.stats[112] = []unix.Stat_t{objectStat(jailRoot, unix.S_IFDIR|0755)}
	fake.stats[113] = []unix.Stat_t{preSealExecutable}
	fake.stats[114] = []unix.Stat_t{objectStat(firecrackerNamespace, unix.S_IFREG|0444)}
	fake.stats[142] = []unix.Stat_t{objectStat(firecrackerNetworkNamespace, unix.S_IFREG|0444)}
	fake.stats[115] = []unix.Stat_t{objectStat(jailRoot, unix.S_IFDIR|0755)}
	fake.stats[116] = []unix.Stat_t{firecrackerStat}
	fake.stats[117] = []unix.Stat_t{objectStat(firecrackerNamespace, unix.S_IFREG|0444)}
	fake.stats[143] = []unix.Stat_t{objectStat(firecrackerNetworkNamespace, unix.S_IFREG|0444)}
	fake.stats[118] = []unix.Stat_t{objectStat(jailRoot, unix.S_IFDIR|0755)}
	fake.stats[119] = []unix.Stat_t{firecrackerStat}
	fake.stats[121] = []unix.Stat_t{objectStat(hostNamespace, unix.S_IFREG|0444)}
	fake.stats[151] = []unix.Stat_t{objectStat(hostNetworkNamespace, unix.S_IFREG|0444)}
	fake.stats[122] = []unix.Stat_t{objectStat(hostRoot, unix.S_IFDIR|0755)}
	fake.stats[123] = []unix.Stat_t{relayStat}
	fake.stats[124] = []unix.Stat_t{objectStat(hostNamespace, unix.S_IFREG|0444)}
	fake.stats[152] = []unix.Stat_t{objectStat(hostNetworkNamespace, unix.S_IFREG|0444)}
	fake.stats[125] = []unix.Stat_t{objectStat(hostRoot, unix.S_IFDIR|0755)}
	fake.stats[126] = []unix.Stat_t{relayStat}
	fake.stats[130] = []unix.Stat_t{objectStat(hostNamespace, unix.S_IFREG|0444)}
	fake.stats[131] = []unix.Stat_t{objectStat(hostRoot, unix.S_IFDIR|0755)}
	fake.stats[132] = []unix.Stat_t{objectStat(hostNamespace, unix.S_IFREG|0444)}
	fake.stats[133] = []unix.Stat_t{objectStat(hostRoot, unix.S_IFDIR|0755)}
	fake.stats[200] = repeatProcessStat(
		objectStat(instanceObjectIdentity{device: 259, inode: 200}, unix.S_IFDIR|0700),
		24,
	)
	fake.stats[201] = repeatProcessStat(
		objectStat(instanceObjectIdentity{device: 259, inode: 201}, unix.S_IFDIR|0700),
		24,
	)
	fake.stats[202] = repeatProcessStat(objectStat(jailRoot, unix.S_IFDIR|0755), 24)
	fake.stats[203] = append([]unix.Stat_t{preSealExecutable}, repeatProcessStat(firecrackerStat, 23)...)
	fake.destination = fake.newPreparedDestination()
	launch.state.preparedJailDestination = fake.destination
	topology, err := launch.retainTopologyAuthority(t.Context())
	require.NoError(t, err)
	require.NoError(t, launch.retainTopologySelectedNetworkNamespace(
		t.Context(),
		topology,
		topologySelectedNetworkNamespace{identity: firecrackerNetworkNamespace},
	))
	require.NoError(t, topology.Release(t.Context()))
	require.NoError(t, launch.releaseToJailer(t.Context()))

	return &processIdentityFixture{
		launch:                      launch,
		processSource:               processes,
		fake:                        fake,
		firecrackerNamespace:        firecrackerNamespace,
		firecrackerNetworkNamespace: firecrackerNetworkNamespace,
		jailRoot:                    jailRoot,
		firecrackerExecutable:       firecrackerExecutable,
		hostNamespace:               hostNamespace,
		hostRoot:                    hostRoot,
		relayExecutable:             relayExecutable,
		receiverIdentity:            receiverIdentity,
		firecrackerDigest:           fmt.Sprintf("%x", preparedSnapshot.manifest.firecracker.digest[:]),
	}
}

func readProcessTestDescriptor(t *testing.T, fd int, size uint64) []byte {
	t.Helper()
	require.LessOrEqual(t, size, uint64(maxProcessSystemExecutableBytes))
	content := make([]byte, int(size))
	read := 0
	for read < len(content) {
		count, err := unix.Pread(fd, content[read:], int64(read))
		require.NoError(t, err)
		require.Positive(t, count)
		read += count
	}
	return content
}

func executableStat(device int32, inode uint64, content []byte) unix.Stat_t {
	return unix.Stat_t{
		Dev: uint64(device), Ino: inode, Size: int64(len(content)), Mode: unix.S_IFREG | 0755,
		Uid: 0, Gid: 0, Nlink: 1,
		Mtim: unix.Timespec{Sec: 10, Nsec: 20}, Ctim: unix.Timespec{Sec: 30, Nsec: 40},
	}
}

func objectStat(identity instanceObjectIdentity, mode uint32) unix.Stat_t {
	return unix.Stat_t{Dev: identity.device, Ino: identity.inode, Mode: mode}
}

func processExecutableDescriptorIdentityFromTestStat(stat unix.Stat_t) processExecutableDescriptorIdentity {
	return processExecutableDescriptorIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, sizeBytes: uint64(stat.Size), mode: stat.Mode,
		uid: stat.Uid, gid: stat.Gid, links: stat.Nlink, modified: stat.Mtim, changed: stat.Ctim,
	}
}

func repeatProcessStat(stat unix.Stat_t, count int) []unix.Stat_t {
	result := make([]unix.Stat_t, count)
	for index := range result {
		result[index] = stat
	}
	return result
}

type processMetadataTestKey struct {
	directoryFD int
	kind        processMetadataKind
}

type processObjectTestKey struct {
	directoryFD int
	object      processPinnedObject
}

type fakeProcessIdentityDeps struct {
	mu sync.Mutex

	bootIDs                 []string
	bootIndex               int
	alive                   map[int][]bool
	aliveIndex              map[int]int
	processDirectories      map[int]int
	metadata                map[processMetadataTestKey][][]byte
	metadataIndex           map[processMetadataTestKey]int
	processObjects          map[processObjectTestKey][]int
	processObjectIndex      map[processObjectTestKey]int
	hostNamespaces          []int
	hostNamespaceIndex      int
	hostRoots               []int
	hostRootIndex           int
	stats                   map[int][]unix.Stat_t
	statIndex               map[int]int
	contents                map[int][]byte
	flags                   map[int]int
	closed                  map[int]int
	closeErrors             map[int]error
	closeOrder              []int
	receiverIdentity        system.PinnedExecutableIdentity
	reinspectIdentity       *system.PinnedExecutableIdentity
	reinspectErr            error
	destination             *preparedJailDestination
	destinationCloses       []int
	destinationOpenFlags    map[int]int
	destinationAbsentChecks int
	destinationMutations    int
	clock                   time.Time
	waits                   []time.Duration
	deriveLocatorOverride   func(context.Context, int) (instanceProcessLocator, error)
	onPread                 func(int)
	preadCalls              int
}

func (fake *fakeProcessIdentityDeps) deps() processIdentityVerifierDeps {
	fake.aliveIndex = make(map[int]int)
	fake.metadataIndex = make(map[processMetadataTestKey]int)
	fake.processObjectIndex = make(map[processObjectTestKey]int)
	fake.statIndex = make(map[int]int)
	return processIdentityVerifierDeps{
		readBootID: fake.readBootID,
		pidfdAlive: fake.pidfdAlive,
		deriveLocator: func(ctx context.Context, pidfd int) (instanceProcessLocator, error) {
			if fake.deriveLocatorOverride != nil {
				return fake.deriveLocatorOverride(ctx, pidfd)
			}
			switch pidfd {
			case processTestFirecrackerPIDFD:
				return instanceProcessLocator{
					pid:            processTestFirecrackerPID,
					startTimeTicks: processTestFirecrackerTicks,
				}, nil
			case processTestRelayPIDFD:
				return instanceProcessLocator{pid: processTestRelayPID, startTimeTicks: processTestRelayTicks}, nil
			default:
				return instanceProcessLocator{}, fmt.Errorf("unexpected pidfd %d", pidfd)
			}
		},
		now: func() time.Time {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return fake.clock
		},
		wait: func(ctx context.Context, wait time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.waits = append(fake.waits, wait)
			fake.clock = fake.clock.Add(wait)
			return nil
		},
		openProcessDirectory:  fake.openProcessDirectory,
		readProcessMetadata:   fake.readProcessMetadata,
		pinProcessObject:      fake.pinProcessObject,
		pinHostMountNamespace: fake.pinHostMountNamespace,
		pinHostRoot:           fake.pinHostRoot,
		descriptorFlags:       fake.descriptorFlags,
		fstat:                 fake.fstat,
		pread:                 fake.pread,
		close:                 fake.close,
		pinnedIdentity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			return fake.receiverIdentity, nil
		},
		pinnedReinspect: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			if fake.reinspectErr != nil {
				return system.PinnedExecutableIdentity{}, fake.reinspectErr
			}
			if fake.reinspectIdentity != nil {
				return *fake.reinspectIdentity, nil
			}
			return fake.receiverIdentity, nil
		},
	}
}

func (fake *fakeProcessIdentityDeps) newPreparedDestination() *preparedJailDestination {
	parent := fake.stats[200][0]
	deps := jailDestinationDeps{
		open: func(context.Context, string, int, uint32) (int, error) { return -1, unix.ENOSYS },
		openAt: func(_ context.Context, directoryFD int, name string, flags int, _ uint32) (int, error) {
			if directoryFD == 200 && name == testVMID {
				fake.mu.Lock()
				defer fake.mu.Unlock()
				fake.destinationAbsentChecks++
				if fake.destinationAbsentChecks <= 2 {
					return -1, unix.ENOENT
				}
				fake.destinationOpenFlags[201] = flags
				return 201, nil
			}
			if directoryFD == 201 && name == "root" {
				fake.destinationOpenFlags[202] = flags
				return 202, nil
			}
			if directoryFD == 202 && name == "firecracker" {
				fake.destinationOpenFlags[203] = flags
				return 203, nil
			}
			return -1, unix.ENOENT
		},
		fstat: fake.fstat,
		statx: func(_ context.Context, _ int, _ int, mask int, stat *unix.Statx_t) error {
			stat.Mask = uint32(mask)
			stat.Mnt_id = 77
			return nil
		},
		fchmod: func(context.Context, int, uint32) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.destinationMutations++
			return nil
		},
		fchown: func(context.Context, int, int, int) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.destinationMutations++
			return nil
		},
		close: func(_ context.Context, fd int) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.destinationCloses = append(fake.destinationCloses, fd)
			return fake.closeErrors[fd]
		},
	}
	return &preparedJailDestination{state: &preparedJailDestinationState{
		deps: deps,
		vmID: testVMID,
		parent: jailDestinationBinding{
			kind:     jailDestinationFirecracker,
			fd:       200,
			identity: instanceObjectIdentity{device: uint64(parent.Dev), inode: parent.Ino},
			mount:    jailDestinationMountIdentity{kind: managedCacheMountIdentityUnique, id: 77},
		},
		status: preparedJailDestinationAbsent,
	}}
}

func (fake *fakeProcessIdentityDeps) readBootID(context.Context) (string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	index := fake.bootIndex
	fake.bootIndex++
	if index >= len(fake.bootIDs) {
		return "", io.EOF
	}
	return fake.bootIDs[index], nil
}

func (fake *fakeProcessIdentityDeps) pidfdAlive(_ context.Context, fd int) (bool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	index := fake.aliveIndex[fd]
	fake.aliveIndex[fd]++
	values := fake.alive[fd]
	if index >= len(values) {
		return false, io.EOF
	}
	return values[index], nil
}

func (fake *fakeProcessIdentityDeps) openProcessDirectory(_ context.Context, pid int) (int, error) {
	fd, found := fake.processDirectories[pid]
	if !found {
		return -1, unix.ENOENT
	}
	return fd, nil
}

func (fake *fakeProcessIdentityDeps) readProcessMetadata(
	_ context.Context,
	directoryFD int,
	kind processMetadataKind,
	_ int,
) ([]byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := processMetadataTestKey{directoryFD: directoryFD, kind: kind}
	index := fake.metadataIndex[key]
	fake.metadataIndex[key]++
	values := fake.metadata[key]
	if index >= len(values) {
		return nil, io.EOF
	}
	return append([]byte{}, values[index]...), nil
}

func (fake *fakeProcessIdentityDeps) pinProcessObject(
	_ context.Context,
	directoryFD int,
	object processPinnedObject,
) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := processObjectTestKey{directoryFD: directoryFD, object: object}
	index := fake.processObjectIndex[key]
	fake.processObjectIndex[key]++
	values := fake.processObjects[key]
	if index >= len(values) {
		return -1, io.EOF
	}
	return values[index], nil
}

func (fake *fakeProcessIdentityDeps) pinHostMountNamespace(context.Context) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.hostNamespaceIndex >= len(fake.hostNamespaces) {
		return -1, io.EOF
	}
	fd := fake.hostNamespaces[fake.hostNamespaceIndex]
	fake.hostNamespaceIndex++
	return fd, nil
}

func (fake *fakeProcessIdentityDeps) pinHostRoot(context.Context) (int, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.hostRootIndex >= len(fake.hostRoots) {
		return -1, io.EOF
	}
	fd := fake.hostRoots[fake.hostRootIndex]
	fake.hostRootIndex++
	return fd, nil
}

func (fake *fakeProcessIdentityDeps) descriptorFlags(_ context.Context, fd int) (int, error) {
	if flags, found := fake.flags[fd]; found {
		return flags, nil
	}
	return unix.FD_CLOEXEC, nil
}

func (fake *fakeProcessIdentityDeps) fstat(_ context.Context, fd int, stat *unix.Stat_t) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	index := fake.statIndex[fd]
	fake.statIndex[fd]++
	values := fake.stats[fd]
	if index >= len(values) {
		return io.EOF
	}
	*stat = values[index]
	return nil
}

func (fake *fakeProcessIdentityDeps) pread(ctx context.Context, fd int, target []byte, offset int64) (int, error) {
	fake.mu.Lock()
	fake.preadCalls++
	call := fake.preadCalls
	onPread := fake.onPread
	content := fake.contents[fd]
	fake.mu.Unlock()
	if onPread != nil {
		onPread(call)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if offset >= int64(len(content)) {
		return 0, nil
	}
	return copy(target, content[offset:]), nil
}

func (fake *fakeProcessIdentityDeps) close(_ context.Context, fd int) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.closed[fd]++
	fake.closeOrder = append(fake.closeOrder, fd)
	return fake.closeErrors[fd]
}

func (fake *fakeProcessIdentityDeps) replaceProcessStatus(directoryFD int, uid, gid uint32, groups string) {
	fake.metadata[processMetadataTestKey{directoryFD: directoryFD, kind: processStatusMetadata}] = [][]byte{
		[]byte(testProcessStatus(uid, gid, groups)), []byte(testProcessStatus(uid, gid, groups)),
	}
}

func (fake *fakeProcessIdentityDeps) replaceProcessCgroup(directoryFD int, cgroup string) {
	fake.metadata[processMetadataTestKey{directoryFD: directoryFD, kind: processCgroupMetadata}] = [][]byte{
		[]byte("0::" + cgroup + "\n"), []byte("0::" + cgroup + "\n"),
	}
}

func (fake *fakeProcessIdentityDeps) closeCount(fd int) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.closed[fd]
}

func (fake *fakeProcessIdentityDeps) destinationCloseOrder() []int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]int{}, fake.destinationCloses...)
}
