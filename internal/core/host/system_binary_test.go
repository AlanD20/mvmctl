package host

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	installRootFD   = 40
	installSourceFD = 41
	installUsrFD    = 42
	installLocalFD  = 43
	installBinFD    = 44
	installTargetFD = 45
	installTempFD   = 46
)

func TestInstallSystemBinary_RequiresRoot(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	fixture.deps.effectiveUID = func() int { return 1000 }

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, errs.CodePrivilegeRequired, errs.AsDomainError(err).Code)
	assert.Empty(t, fixture.openCalls)
}

func TestInstallSystemBinary_RejectsLegacyNonCanonicalSudoersWildcardBeforeInstall(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	readPaths := []string{}
	fixture.deps.readSudoers = func(path string) ([]byte, error) {
		readPaths = append(readPaths, path)
		return []byte("%mvm ALL=(root) NOPASSWD: /home/operator/mvm *\n"), nil
	}

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Equal(t, []string{infra.SudoersDropInPath()}, readPaths)
	assert.Empty(t, fixture.openCalls, "legacy policy must be refused before installer descriptors or writes")
	assert.ErrorContains(t, err, "cannot prove whether sudo password authentication was used")
	assert.ErrorContains(t, err, "host install-system")
}

func TestValidateSystemBinaryInstallSudoers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "empty", content: ""},
		{name: "comments only", content: "# Managed by mvmctl\n# no active policy\n"},
		{name: "generated transitional policy", content: GenerateSudoersContent(infra.MVMUnixGroup)},
		{
			name: "current canonical wildcard",
			content: "%mvm ALL=(root) NOPASSWD: " + infra.PrivilegedBinariesOrdered[0] +
				", " + infra.SystemBinaryPath + " *\n",
		},
		{
			name: "recognized marker wildcard",
			content: "%mvm ALL=(root) NOPASSWD: " + infra.SystemBinaryPath +
				" " + infra.PrivilegedProtocolMarker + " *\n",
		},
		{
			name:    "comment containing old example is inert",
			content: "# old example: /home/operator/mvm *\n",
		},
		{
			name:    "user executable wildcard",
			content: "%mvm ALL=(root) NOPASSWD: /home/operator/mvm *\n",
			wantErr: true,
		},
		{
			name:    "relative traversal-looking executable wildcard",
			content: "%mvm ALL=(root) NOPASSWD: /usr/local/bin/../share/mvm *\n",
			wantErr: true,
		},
		{
			name:    "wildcard after line continuation",
			content: "%mvm ALL=(root) NOPASSWD: /usr/bin/ip, \\\n /tmp/mvm *\n",
			wantErr: true,
		},
		{
			name:    "arbitrary tool wildcard",
			content: "%mvm ALL=(root) NOPASSWD: /usr/bin/env *\n",
			wantErr: true,
		},
		{
			name: "unsupported privileged marker wildcard",
			content: "%mvm ALL=(root) NOPASSWD: " + infra.SystemBinaryPath +
				" __mvm_privileged_v999 *\n",
			wantErr: true,
		},
		{
			name:    "command alias wildcard",
			content: "Cmnd_Alias MVM = /opt/mvm *\n%mvm ALL=(root) NOPASSWD: MVM\n",
			wantErr: true,
		},
		{
			name:    "no whitespace after sudoers tag",
			content: "%mvm ALL=(root) NOPASSWD:/home/operator/mvm *\n",
			wantErr: true,
		},
		{
			name:    "no whitespace after command alias assignment",
			content: "Cmnd_Alias MVM=/opt/mvm *\n%mvm ALL=(root) NOPASSWD: MVM\n",
			wantErr: true,
		},
		{
			name:    "wildcard embedded in executable path",
			content: "%mvm ALL=(root) NOPASSWD: /home/*/mvm\n",
			wantErr: true,
		},
		{
			name:    "at include directive",
			content: "@include /home/operator/legacy-sudoers\n",
			wantErr: true,
		},
		{
			name:    "legacy hash include directive",
			content: "#include /home/operator/legacy-sudoers\n",
			wantErr: true,
		},
		{
			name:    "alias without inline wildcard",
			content: "Cmnd_Alias MVM = /opt/mvm\n%mvm ALL=(root) NOPASSWD: MVM\n",
			wantErr: true,
		},
		{
			name:    "unknown command without wildcard",
			content: "%mvm ALL=(root) NOPASSWD: /opt/mvm host init\n",
			wantErr: true,
		},
		{
			name:    "canonical command granted to wrong group",
			content: "%wheel ALL=(root) NOPASSWD: " + infra.SystemBinaryPath + " *\n",
			wantErr: true,
		},
		{
			name:    "unknown defaults directive",
			content: "Defaults env_keep += \"MVM_*\"\n",
			wantErr: true,
		},
		{
			name:    "escaped legacy command",
			content: "%mvm ALL=(root) NOPASSWD: /home/operator/mvm\\ *\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSystemBinaryInstallSudoers(tt.content)
			if tt.wantErr {
				require.Error(t, err)
				assert.NotNil(t, errs.AsDomainError(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestInstallSystemBinary_InstallsPinnedRunningImage(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	fixture.targetExists = false

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, fixture.sourceData, fixture.targetData)
	assert.Equal(t, uint32(0), fixture.stats[installTargetFD].Uid)
	assert.Equal(t, uint32(0), fixture.stats[installTargetFD].Gid)
	assert.Equal(t, uint32(unix.S_IFREG|0755), fixture.stats[installTargetFD].Mode)
	assert.True(t, fixture.renamed)
	assert.Contains(t, fixture.syncedFDs, installTempFD)
	assert.Contains(t, fixture.syncedFDs, installBinFD)
	assert.Empty(t, fixture.tempData)
	assertDomainErrorsOnly(t, err)
}

// Rationale: A host init executed from the already-installed image must not
// replace that image or create a temporary file when its metadata is exact.
func TestInstallSystemBinary_SameInodeAndMetadataIsNoOp(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	targetStat := fixture.stats[installTargetFD]
	targetStat.Dev = fixture.stats[installSourceFD].Dev
	targetStat.Ino = fixture.stats[installSourceFD].Ino
	fixture.stats[installTargetFD] = targetStat

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.False(t, fixture.tempCreated)
	assert.False(t, fixture.renamed)
}

func TestInstallSystemBinary_RejectsUnsafeTargetTypes(t *testing.T) {
	tests := map[string]uint32{
		"symlink":   unix.S_IFLNK | 0777,
		"directory": unix.S_IFDIR | 0755,
		"fifo":      unix.S_IFIFO | 0600,
	}
	for name, mode := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			stat := fixture.stats[installTargetFD]
			stat.Mode = mode
			fixture.stats[installTargetFD] = stat

			changed, err := installSystemBinary(context.Background(), fixture.deps)
			require.Error(t, err)
			assert.False(t, changed)
			assert.ErrorContains(t, err, "regular file")
			assert.False(t, fixture.tempCreated)
			assertDomainErrorsOnly(t, err)
		})
	}
}

func TestInstallSystemBinary_RejectsUnsafeAncestors(t *testing.T) {
	tests := map[string]struct {
		fd   int
		mode uint32
		uid  uint32
	}{
		"non_root_owned_root": {fd: installRootFD, mode: unix.S_IFDIR | 0755, uid: 1000},
		"writable_usr":        {fd: installUsrFD, mode: unix.S_IFDIR | 0775, uid: 0},
		"writable_local":      {fd: installLocalFD, mode: unix.S_IFDIR | 0757, uid: 0},
		"non_directory_bin":   {fd: installBinFD, mode: unix.S_IFREG | 0755, uid: 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			stat := fixture.stats[tc.fd]
			stat.Mode = tc.mode
			stat.Uid = tc.uid
			fixture.stats[tc.fd] = stat

			changed, err := installSystemBinary(context.Background(), fixture.deps)
			require.Error(t, err)
			assert.False(t, changed)
			assert.False(t, fixture.tempCreated)
			assertDomainErrorsOnly(t, err)
		})
	}
}

// Rationale: The installer may create only the policy-approved local/bin
// suffix. Creating /usr would let an unexpected filesystem layout become a
// trusted executable root.
func TestInstallSystemBinary_CreatesOnlyAllowedMissingDirectories(t *testing.T) {
	t.Run("missing_usr_is_refused", func(t *testing.T) {
		fixture := newSystemBinaryInstallFixture()
		fixture.usrExists = false

		changed, err := installSystemBinary(context.Background(), fixture.deps)
		require.Error(t, err)
		assert.False(t, changed)
		assert.Empty(t, fixture.mkdirCalls)
		assertDomainErrorsOnly(t, err)
	})

	t.Run("missing_local_and_bin_are_created_exactly", func(t *testing.T) {
		fixture := newSystemBinaryInstallFixture()
		fixture.localExists = false
		fixture.binExists = false
		fixture.targetExists = false

		changed, err := installSystemBinary(context.Background(), fixture.deps)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, []installAtCall{
			{dirFD: installUsrFD, name: "local", mode: 0755},
			{dirFD: installLocalFD, name: "bin", mode: 0755},
		}, fixture.mkdirCalls)
		for _, fd := range []int{installLocalFD, installBinFD} {
			stat := fixture.stats[fd]
			assert.Equal(t, uint32(0), stat.Uid)
			assert.Equal(t, uint32(0), stat.Gid)
			assert.Equal(t, uint32(unix.S_IFDIR|0755), stat.Mode)
		}
		assert.Contains(t, fixture.syncedFDs, installUsrFD)
		assert.Contains(t, fixture.syncedFDs, installLocalFD)
	})

	t.Run("concurrently_created_local_is_validated_not_mutated", func(t *testing.T) {
		fixture := newSystemBinaryInstallFixture()
		fixture.localExists = false
		fixture.mkdirRaceName = "local"
		fixture.targetExists = false

		changed, err := installSystemBinary(context.Background(), fixture.deps)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.NotContains(t, fixture.chownedFDs, installLocalFD)
		assert.NotContains(t, fixture.chmoddedFDs, installLocalFD)
	})
}

// Rationale: read(2) and write(2) may make short progress without an error;
// truncating the trusted executable would leave host init apparently green.
func TestInstallSystemBinary_HandlesPartialReadsAndWrites(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	fixture.targetExists = false
	fixture.maxRead = 3
	fixture.maxWrite = 2

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, fixture.sourceData, fixture.targetData)
	assert.Greater(t, fixture.readCalls, 1)
	assert.Greater(t, fixture.writeCalls, 1)
}

// Rationale: Every failure before rename must preserve the previous trusted
// executable and remove the incomplete sibling temporary file.
func TestInstallSystemBinary_PreRenameFailuresPreserveTarget(t *testing.T) {
	tests := map[string]func(*systemBinaryInstallFixture){
		"read":   func(f *systemBinaryInstallFixture) { f.failRead = true },
		"write":  func(f *systemBinaryInstallFixture) { f.failWrite = true },
		"fchown": func(f *systemBinaryInstallFixture) { f.failChownFD = installTempFD },
		"fchmod": func(f *systemBinaryInstallFixture) { f.failChmodFD = installTempFD },
		"file_fsync": func(f *systemBinaryInstallFixture) {
			f.failSyncFD = installTempFD
		},
		"temp_close": func(f *systemBinaryInstallFixture) {
			f.failCloseFD = installTempFD
		},
		"rename": func(f *systemBinaryInstallFixture) { f.failRename = true },
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			original := append([]byte(nil), fixture.targetData...)
			inject(fixture)

			changed, err := installSystemBinary(context.Background(), fixture.deps)
			require.Error(t, err)
			assert.False(t, changed)
			assert.Equal(t, original, fixture.targetData)
			assert.False(t, fixture.renamed)
			assert.True(t, fixture.tempUnlinked)
			assertDomainErrorsOnly(t, err)
		})
	}
}

func TestInstallSystemBinary_ReportsPostRenameAndCleanupFailures(t *testing.T) {
	tests := map[string]struct {
		inject                  func(*systemBinaryInstallFixture)
		wantRenamed             bool
		wantChanged             bool
		wantDurabilityUncertain bool
		wantErr                 string
	}{
		"directory_fsync": {
			inject:                  func(f *systemBinaryInstallFixture) { f.failSyncFD = installBinFD },
			wantRenamed:             true,
			wantChanged:             true,
			wantDurabilityUncertain: true,
			wantErr:                 "sync system binary directory",
		},
		"retained_descriptor_close": {
			inject:      func(f *systemBinaryInstallFixture) { f.failCloseFD = installSourceFD },
			wantRenamed: true,
			wantChanged: true,
			wantErr:     "close system binary installer descriptor",
		},
		"directory_fsync_and_retained_descriptor_close": {
			inject: func(f *systemBinaryInstallFixture) {
				f.failSyncFD = installBinFD
				f.failCloseFD = installSourceFD
			},
			wantRenamed:             true,
			wantChanged:             true,
			wantDurabilityUncertain: true,
			wantErr:                 "close system binary installer descriptor",
		},
		"cleanup_unlink": {
			inject: func(f *systemBinaryInstallFixture) {
				f.failWrite = true
				f.failUnlink = true
			},
			wantRenamed: false,
			wantErr:     "remove incomplete system binary temporary file",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			tc.inject(fixture)

			changed, err := installSystemBinary(context.Background(), fixture.deps)
			require.Error(t, err)
			assert.Equal(t, tc.wantChanged, changed)
			assert.Equal(t, tc.wantRenamed, fixture.renamed)
			assert.ErrorContains(t, err, tc.wantErr)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if tc.wantRenamed {
				assert.Equal(t, true, domainErr.Details["system_binary_replaced"])
			}
			if tc.wantDurabilityUncertain {
				assert.Equal(t, true, domainErr.Details["durability_uncertain"])
			}
			assertDomainErrorsOnly(t, err)
		})
	}
}

// Rationale: The real descriptor-relative implementation must work on an
// isolated filesystem root without exposing a caller-selected production path.
func TestInstallSystemBinary_RealFilesystem(t *testing.T) {
	newPolicy := func(root string) systemBinaryInstallPolicy {
		return systemBinaryInstallPolicy{
			rootPath:    root,
			expectedUID: uint32(os.Geteuid()),
			expectedGID: uint32(os.Getegid()),
		}
	}
	newDeps := func(t *testing.T) systemBinaryInstallDeps {
		t.Helper()
		deps := realSystemBinaryInstallDeps()
		deps.effectiveUID = func() int { return 0 }
		deps.readSudoers = func(path string) ([]byte, error) {
			assert.Equal(t, infra.SudoersDropInPath(), path)
			return nil, os.ErrNotExist
		}
		return deps
	}

	t.Run("copies current image atomically with exact metadata", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, "usr"), 0755))

		changed, err := installSystemBinaryWithPolicy(
			context.Background(),
			newDeps(t),
			newPolicy(root),
		)
		require.NoError(t, err)
		assert.True(t, changed)

		targetPath := filepath.Join(root, "usr", "local", "bin", "mvm")
		source, err := os.ReadFile("/proc/self/exe")
		require.NoError(t, err)
		target, err := os.ReadFile(targetPath)
		require.NoError(t, err)
		assert.Equal(t, sha256.Sum256(source), sha256.Sum256(target))
		var targetStat unix.Stat_t
		require.NoError(t, unix.Stat(targetPath, &targetStat))
		assert.Equal(t, uint32(unix.S_IFREG|0755), targetStat.Mode)
		assert.Equal(t, uint32(os.Geteuid()), targetStat.Uid)
		assert.Equal(t, uint32(os.Getegid()), targetStat.Gid)
	})

	t.Run("rejects target symlink", func(t *testing.T) {
		root := t.TempDir()
		binDir := filepath.Join(root, "usr", "local", "bin")
		require.NoError(t, os.MkdirAll(binDir, 0755))
		require.NoError(t, os.Symlink("/tmp/untrusted-mvm", filepath.Join(binDir, "mvm")))

		changed, err := installSystemBinaryWithPolicy(
			context.Background(),
			newDeps(t),
			newPolicy(root),
		)
		require.Error(t, err)
		assert.False(t, changed)
		assert.ErrorContains(t, err, "regular file")
	})
}

func TestInstallSystemBinary_CancellationDuringCopyPreservesTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fixture := newSystemBinaryInstallFixture()
	fixture.maxRead = 2
	originalRead := fixture.deps.read
	fixture.deps.read = func(fd int, dst []byte) (int, error) {
		n, err := originalRead(fd, dst)
		cancel()
		return n, err
	}
	original := append([]byte(nil), fixture.targetData...)

	changed, err := installSystemBinary(ctx, fixture.deps)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, original, fixture.targetData)
	assert.False(t, fixture.renamed)
	assert.True(t, fixture.tempUnlinked)
	assertDomainErrorsOnly(t, err)
}

// Rationale: Cancellation after the file is durable but before rename must
// still leave the prior executable intact.
func TestInstallSystemBinary_CancellationImmediatelyBeforeRenamePreservesTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fixture := newSystemBinaryInstallFixture()
	originalClose := fixture.deps.close
	fixture.deps.close = func(fd int) error {
		err := originalClose(fd)
		if fd == installTempFD {
			cancel()
		}
		return err
	}
	original := append([]byte(nil), fixture.targetData...)

	changed, err := installSystemBinary(ctx, fixture.deps)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, original, fixture.targetData)
	assert.False(t, fixture.renamed)
	assert.True(t, fixture.tempUnlinked)
}

// Rationale: A premature 0,nil read must not promote a truncated executable;
// the pinned source descriptor's size is the independent completion check.
func TestInstallSystemBinary_RejectsPrematureSourceEOF(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	fixture.deps.read = func(int, []byte) (int, error) { return 0, nil }
	original := append([]byte(nil), fixture.targetData...)

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.Error(t, err)
	assert.False(t, changed)
	assert.ErrorContains(t, err, "size")
	assert.Equal(t, original, fixture.targetData)
	assert.False(t, fixture.renamed)
	assert.True(t, fixture.tempUnlinked)
}

// Rationale: Metadata checks followed by absolute-path reopen would re-create
// the symlink race this installer is intended to close.
func TestInstallSystemBinary_UsesOnlyPinnedDescriptorRelativeTargets(t *testing.T) {
	fixture := newSystemBinaryInstallFixture()
	fixture.targetExists = false

	changed, err := installSystemBinary(context.Background(), fixture.deps)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []installOpenCall{
		{path: "/", flags: directoryInstallFlags},
		{path: "/proc/self/exe", flags: sourceInstallFlags},
	}, fixture.openCalls)
	assert.Contains(t, fixture.openAtCalls, installOpenAtCall{
		dirFD: installBinFD, name: "mvm", flags: targetInspectFlags,
	})
	assert.Contains(t, fixture.openAtCalls, installOpenAtCall{
		dirFD: installBinFD, name: fixture.tempName, flags: tempInstallFlags, mode: 0600,
	})
	require.Len(t, fixture.renameCalls, 1)
	assert.Equal(t, installRenameCall{
		oldDirFD: installBinFD, oldName: fixture.tempName,
		newDirFD: installBinFD, newName: "mvm",
	}, fixture.renameCalls[0])
}

func assertDomainErrorsOnly(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	require.NotNil(t, errs.AsDomainError(err), "all installer errors must be DomainError")
}

type installOpenCall struct {
	path  string
	flags int
}

type installOpenAtCall struct {
	dirFD int
	name  string
	flags int
	mode  uint32
}

type installAtCall struct {
	dirFD int
	name  string
	mode  uint32
}

type installRenameCall struct {
	oldDirFD int
	oldName  string
	newDirFD int
	newName  string
}

type systemBinaryInstallFixture struct {
	deps systemBinaryInstallDeps

	usrExists    bool
	localExists  bool
	binExists    bool
	targetExists bool
	tempCreated  bool
	tempUnlinked bool
	renamed      bool

	stats      map[int]unix.Stat_t
	sourceData []byte
	targetData []byte
	tempData   []byte
	readOffset int
	maxRead    int
	maxWrite   int
	readCalls  int
	writeCalls int
	tempName   string

	failRead      bool
	failWrite     bool
	failRename    bool
	failUnlink    bool
	failChownFD   int
	failChmodFD   int
	failSyncFD    int
	failCloseFD   int
	mkdirRaceName string

	openCalls   []installOpenCall
	openAtCalls []installOpenAtCall
	mkdirCalls  []installAtCall
	renameCalls []installRenameCall
	unlinked    []installAtCall
	syncedFDs   []int
	closedFDs   []int
	chownedFDs  []int
	chmoddedFDs []int
}

func newSystemBinaryInstallFixture() *systemBinaryInstallFixture {
	fixture := &systemBinaryInstallFixture{
		usrExists: true, localExists: true, binExists: true, targetExists: true,
		stats: map[int]unix.Stat_t{
			installRootFD:   {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 1},
			installSourceFD: {Mode: unix.S_IFREG | 0700, Uid: 1000, Gid: 1000, Dev: 1, Ino: 2},
			installUsrFD:    {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 3},
			installLocalFD:  {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 4},
			installBinFD:    {Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 5},
			installTargetFD: {Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 6},
			installTempFD:   {Mode: unix.S_IFREG | 0600, Uid: 0, Gid: 0, Dev: 1, Ino: 7},
		},
		sourceData: []byte("new trusted mvm image"),
		targetData: []byte("old trusted mvm image"),
		tempName:   ".mvm-install-test.tmp",
	}
	sourceStat := fixture.stats[installSourceFD]
	sourceStat.Size = int64(len(fixture.sourceData))
	fixture.stats[installSourceFD] = sourceStat
	fixture.deps = systemBinaryInstallDeps{
		effectiveUID: func() int { return 0 },
		readSudoers:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		open: func(path string, flags int, _ uint32) (int, error) {
			fixture.openCalls = append(fixture.openCalls, installOpenCall{path: path, flags: flags})
			switch path {
			case "/":
				return installRootFD, nil
			case "/proc/self/exe":
				return installSourceFD, nil
			default:
				return -1, unix.ENOENT
			}
		},
		openAt: func(dirFD int, name string, flags int, mode uint32) (int, error) {
			fixture.openAtCalls = append(fixture.openAtCalls, installOpenAtCall{
				dirFD: dirFD, name: name, flags: flags, mode: mode,
			})
			switch {
			case dirFD == installRootFD && name == "usr":
				if !fixture.usrExists {
					return -1, unix.ENOENT
				}
				return installUsrFD, nil
			case dirFD == installUsrFD && name == "local":
				if !fixture.localExists {
					return -1, unix.ENOENT
				}
				return installLocalFD, nil
			case dirFD == installLocalFD && name == "bin":
				if !fixture.binExists {
					return -1, unix.ENOENT
				}
				return installBinFD, nil
			case dirFD == installBinFD && name == "mvm":
				if !fixture.targetExists {
					return -1, unix.ENOENT
				}
				return installTargetFD, nil
			case dirFD == installBinFD && name == fixture.tempName:
				if fixture.tempCreated {
					return -1, unix.EEXIST
				}
				fixture.tempCreated = true
				return installTempFD, nil
			default:
				return -1, unix.ENOENT
			}
		},
		mkdirAt: func(dirFD int, name string, mode uint32) error {
			fixture.mkdirCalls = append(fixture.mkdirCalls, installAtCall{dirFD: dirFD, name: name, mode: mode})
			switch {
			case dirFD == installUsrFD && name == "local":
				fixture.localExists = true
			case dirFD == installLocalFD && name == "bin":
				fixture.binExists = true
			default:
				return unix.EPERM
			}
			if name == fixture.mkdirRaceName {
				return unix.EEXIST
			}
			return nil
		},
		fstat: func(fd int, stat *unix.Stat_t) error {
			value, ok := fixture.stats[fd]
			if !ok {
				return unix.EBADF
			}
			*stat = value
			return nil
		},
		fchown: func(fd, uid, gid int) error {
			fixture.chownedFDs = append(fixture.chownedFDs, fd)
			if fd == fixture.failChownFD {
				return unix.EPERM
			}
			stat := fixture.stats[fd]
			stat.Uid = uint32(uid)
			stat.Gid = uint32(gid)
			fixture.stats[fd] = stat
			return nil
		},
		fchmod: func(fd int, mode uint32) error {
			fixture.chmoddedFDs = append(fixture.chmoddedFDs, fd)
			if fd == fixture.failChmodFD {
				return unix.EPERM
			}
			stat := fixture.stats[fd]
			stat.Mode = stat.Mode&unix.S_IFMT | mode
			fixture.stats[fd] = stat
			return nil
		},
		read: func(fd int, dst []byte) (int, error) {
			fixture.readCalls++
			if fixture.failRead {
				return 0, unix.EIO
			}
			if fd != installSourceFD {
				return 0, unix.EBADF
			}
			if fixture.readOffset == len(fixture.sourceData) {
				return 0, nil
			}
			limit := len(dst)
			if fixture.maxRead > 0 && fixture.maxRead < limit {
				limit = fixture.maxRead
			}
			n := copy(dst[:limit], fixture.sourceData[fixture.readOffset:])
			fixture.readOffset += n
			return n, nil
		},
		write: func(fd int, src []byte) (int, error) {
			fixture.writeCalls++
			if fixture.failWrite {
				return 0, unix.ENOSPC
			}
			if fd != installTempFD {
				return 0, unix.EBADF
			}
			limit := len(src)
			if fixture.maxWrite > 0 && fixture.maxWrite < limit {
				limit = fixture.maxWrite
			}
			fixture.tempData = append(fixture.tempData, src[:limit]...)
			stat := fixture.stats[installTempFD]
			stat.Size = int64(len(fixture.tempData))
			fixture.stats[installTempFD] = stat
			return limit, nil
		},
		fsync: func(fd int) error {
			fixture.syncedFDs = append(fixture.syncedFDs, fd)
			if fd == fixture.failSyncFD {
				return unix.EIO
			}
			return nil
		},
		close: func(fd int) error {
			fixture.closedFDs = append(fixture.closedFDs, fd)
			if fd == fixture.failCloseFD {
				return unix.EIO
			}
			return nil
		},
		renameAt: func(oldDirFD int, oldName string, newDirFD int, newName string) error {
			fixture.renameCalls = append(fixture.renameCalls, installRenameCall{
				oldDirFD: oldDirFD, oldName: oldName, newDirFD: newDirFD, newName: newName,
			})
			if fixture.failRename {
				return unix.EIO
			}
			fixture.renamed = true
			fixture.targetExists = true
			fixture.targetData = append(fixture.targetData[:0], fixture.tempData...)
			fixture.stats[installTargetFD] = fixture.stats[installTempFD]
			fixture.tempData = nil
			return nil
		},
		unlinkAt: func(dirFD int, name string, flags int) error {
			fixture.unlinked = append(fixture.unlinked, installAtCall{dirFD: dirFD, name: name, mode: uint32(flags)})
			if fixture.failUnlink {
				return unix.EIO
			}
			fixture.tempUnlinked = true
			fixture.tempData = nil
			return nil
		},
		randomName: func() (string, error) { return fixture.tempName, nil },
	}
	return fixture
}
