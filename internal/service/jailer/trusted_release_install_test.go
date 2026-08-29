package jailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestReleaseAuthorityInstallRejectsInvalidInputBeforeExternalEffects(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseInstallFixture(t)
	externalEffects := 0
	fixture.authority.checksumAuthority.client.Transport = trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			externalEffects++
			return nil, assert.AnError
		},
	)

	tests := map[string]struct {
		slot          releaseSlot
		intent        trustedReleaseInstallIntent
		callerArchive *trustedReleaseCallerArchive
	}{
		"invalid slot": {
			slot:   fixture.slot,
			intent: trustedReleaseInstallAbsentOnly,
		},
		"invalid intent": {
			slot:   fixture.store.slot,
			intent: trustedReleaseInstallIntent(255),
		},
		"caller archive without reader": {
			slot:   fixture.store.slot,
			intent: trustedReleaseInstallAbsentOnly,
			callerArchive: &trustedReleaseCallerArchive{
				sizeBytes: 1,
			},
		},
		"caller archive without bytes": {
			slot:   fixture.store.slot,
			intent: trustedReleaseInstallAbsentOnly,
			callerArchive: &trustedReleaseCallerArchive{
				reader: bytes.NewReader(nil),
			},
		},
		"caller archive exceeds hard limit": {
			slot:   fixture.store.slot,
			intent: trustedReleaseInstallAbsentOnly,
			callerArchive: &trustedReleaseCallerArchive{
				reader:    bytes.NewReader(nil),
				sizeBytes: trustedReleaseArchiveMaxBytes + 1,
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := fixture.authority.install(t.Context(), tc.slot, tc.intent, tc.callerArchive)
			require.Error(t, err)
			if diff := cmp.Diff(
				trustedReleaseInstallResult{},
				result,
				cmp.AllowUnexported(
					trustedReleaseInstallResult{},
					trustedReleaseManifest{},
					releaseSlot{},
					trustedReleaseExecutable{},
				),
			); diff != "" {
				t.Errorf("install rejection result mismatch (-want +got):\n%s", diff)
			}
		})
	}
	assert.Zero(t, externalEffects)
}

func TestReleaseAuthorityInstallRejectsInactiveAuthority(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseInstallFixture(t)
	tests := map[string]*releaseAuthority{
		"nil authority":  nil,
		"zero authority": {},
		"missing instance authority": {
			checksumAuthority: fixture.authority.checksumAuthority,
			archiveFetcher:    fixture.authority.archiveFetcher,
		},
		"missing checksum authority": {instances: fixture.instances, archiveFetcher: fixture.authority.archiveFetcher},
		"missing archive fetcher": {
			instances:         fixture.instances,
			checksumAuthority: fixture.authority.checksumAuthority,
		},
	}
	for name, authority := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := authority.install(
				t.Context(), fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
			)
			require.Error(t, err)
			if diff := cmp.Diff(
				trustedReleaseInstallResult{},
				result,
				trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("inactive authority result mismatch (-want +got):\n%s", diff)
			}
		})
	}
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestReleaseAuthorityInstallCallerAndRootArchiveModes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		callerArchive bool
	}{
		"caller archive": {callerArchive: true},
		"root archive":   {callerArchive: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			var caller *trustedReleaseCallerArchive
			if tc.callerArchive {
				caller = &trustedReleaseCallerArchive{
					reader:    bytes.NewReader(fixture.archive),
					sizeBytes: uint64(len(fixture.archive)),
				}
			}

			result, err := fixture.authority.install(
				t.Context(),
				fixture.store.slot,
				trustedReleaseInstallAbsentOnly,
				caller,
			)
			require.NoError(t, err)
			want := trustedReleaseInstallResult{
				outcome:  trustedReleaseInstallInstalled,
				manifest: fixture.manifest,
			}
			if diff := cmp.Diff(
				want,
				result,
				cmp.AllowUnexported(
					trustedReleaseInstallResult{},
					trustedReleaseManifest{},
					releaseSlot{},
					trustedReleaseExecutable{},
				),
			); diff != "" {
				t.Errorf("install result mismatch (-want +got):\n%s", diff)
			}
			assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, fixture.manifest)
			assertInstalledTrustedReleaseMatchesArchive(t, fixture)
			assert.Equal(t, 1, fixture.checksumRequests)
			source, sourceErr := newTrustedReleaseSource(fixture.store.slot)
			require.NoError(t, sourceErr)
			if diff := cmp.Diff([]string{source.checksumURL}, fixture.checksumURLs); diff != "" {
				t.Errorf("checksum source mismatch (-want +got):\n%s", diff)
			}
			if tc.callerArchive {
				assert.Zero(t, fixture.archiveRequests)
				assert.Empty(t, fixture.archiveURLs)
			} else {
				assert.Equal(t, 1, fixture.archiveRequests)
				if diff := cmp.Diff([]string{source.archiveURL}, fixture.archiveURLs); diff != "" {
					t.Errorf("archive source mismatch (-want +got):\n%s", diff)
				}
			}
			gotIdentity, identityErr := result.manifest.releaseIdentity()
			require.NoError(t, identityErr)
			wantIdentity, identityErr := fixture.manifest.releaseIdentity()
			require.NoError(t, identityErr)
			if diff := cmp.Diff(wantIdentity, gotIdentity, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
				t.Errorf("result release identity mismatch (-want +got):\n%s", diff)
			}
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallIdenticalReleaseDoesNotScanReferenceAuthority(t *testing.T) {
	t.Parallel()

	intents := []trustedReleaseInstallIntent{
		trustedReleaseInstallAbsentOnly,
		trustedReleaseInstallAllowReplacement,
	}
	for _, intent := range intents {
		t.Run(installIntentNameForTest(intent), func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			fixture.writeInstalled(t, fixture.manifest, fixture.firecracker, fixture.jailer)
			directory := fixture.instanceRoot + "/var/lib/mvmctl/instances/1000"
			require.NoError(t, os.MkdirAll(directory, 0700))
			require.NoError(t, os.WriteFile(directory+"/"+testVMID+".json", []byte(`{}`), 0600))

			result, err := fixture.authority.install(
				t.Context(), fixture.store.slot, intent, fixture.callerArchive(),
			)
			require.NoError(t, err)
			want := trustedReleaseInstallResult{
				outcome:  trustedReleaseInstallUnchanged,
				manifest: fixture.manifest,
			}
			if diff := cmp.Diff(want, result, trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("identical install result mismatch (-want +got):\n%s", diff)
			}
			assertInstalledTrustedReleaseMatchesArchive(t, fixture)
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallIntentAndCanonicalStateOutcomes(t *testing.T) {
	t.Parallel()

	type canonicalState uint8
	const (
		canonicalAbsent canonicalState = iota
		canonicalIdentical
		canonicalDifferent
	)
	tests := map[string]struct {
		intent           trustedReleaseInstallIntent
		canonical        canonicalState
		wantOutcome      trustedReleaseInstallOutcome
		wantErr          bool
		wantInstalledNew bool
	}{
		"absent-only installs absent slot": {
			intent: trustedReleaseInstallAbsentOnly, canonical: canonicalAbsent,
			wantOutcome: trustedReleaseInstallInstalled, wantInstalledNew: true,
		},
		"replacement permission installs absent slot": {
			intent: trustedReleaseInstallAllowReplacement, canonical: canonicalAbsent,
			wantOutcome: trustedReleaseInstallInstalled, wantInstalledNew: true,
		},
		"absent-only leaves identical slot unchanged": {
			intent: trustedReleaseInstallAbsentOnly, canonical: canonicalIdentical,
			wantOutcome: trustedReleaseInstallUnchanged, wantInstalledNew: true,
		},
		"replacement permission leaves identical slot unchanged": {
			intent: trustedReleaseInstallAllowReplacement, canonical: canonicalIdentical,
			wantOutcome: trustedReleaseInstallUnchanged, wantInstalledNew: true,
		},
		"absent-only rejects different slot": {
			intent: trustedReleaseInstallAbsentOnly, canonical: canonicalDifferent,
			wantErr: true,
		},
		"replacement permission replaces different slot": {
			intent: trustedReleaseInstallAllowReplacement, canonical: canonicalDifferent,
			wantOutcome: trustedReleaseInstallReplaced, wantInstalledNew: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			oldManifest := trustedReleaseManifest{}
			switch tc.canonical {
			case canonicalIdentical:
				fixture.writeInstalled(t, fixture.manifest, fixture.firecracker, fixture.jailer)
				oldManifest = fixture.manifest
			case canonicalDifferent:
				oldManifest = fixture.writeDifferentInstalled(t)
			}
			result, err := fixture.authority.install(
				t.Context(),
				fixture.store.slot,
				tc.intent,
				&trustedReleaseCallerArchive{
					reader:    bytes.NewReader(fixture.archive),
					sizeBytes: uint64(len(fixture.archive)),
				},
			)
			if tc.wantErr {
				require.Error(t, err)
				assertZeroTrustedReleaseInstallResult(t, result, "conflicting install")
				assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, oldManifest)
				assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
				return
			}
			require.NoError(t, err)
			want := trustedReleaseInstallResult{outcome: tc.wantOutcome, manifest: fixture.manifest}
			if diff := cmp.Diff(want, result, trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("install outcome mismatch (-want +got):\n%s", diff)
			}
			if tc.wantInstalledNew {
				assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, fixture.manifest)
			}
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallRejectsReferencedOrCorruptAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare  func(*testing.T, *trustedReleaseInstallFixture, trustedReleaseManifest)
		wantCode errs.Code
	}{
		"referenced release": {
			prepare: func(t *testing.T, fixture *trustedReleaseInstallFixture, old trustedReleaseManifest) {
				identity, err := old.releaseIdentity()
				require.NoError(t, err)
				record := testRegisteredInstanceRecord()
				record.release = identity
				writeTestAuthorityRecord(t, fixture.instanceRoot, record)
			},
			wantCode: errs.CodeVMStateInvalid,
		},
		"corrupt reference authority": {
			prepare: func(t *testing.T, fixture *trustedReleaseInstallFixture, _ trustedReleaseManifest) {
				directory := fixture.instanceRoot + "/var/lib/mvmctl/instances/1000"
				require.NoError(t, os.MkdirAll(directory, 0700))
				require.NoError(t, os.WriteFile(directory+"/"+testVMID+".json", []byte(`{}`), 0600))
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			old := fixture.writeDifferentInstalled(t)
			tc.prepare(t, fixture, old)
			result, err := fixture.authority.install(
				t.Context(),
				fixture.store.slot,
				trustedReleaseInstallAllowReplacement,
				fixture.callerArchive(),
			)
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, errs.AsDomainError(err).Code)
			if diff := cmp.Diff(
				trustedReleaseInstallResult{},
				result,
				trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("rejected replacement result mismatch (-want +got):\n%s", diff)
			}
			assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, old)
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallRejectsCorruptCanonicalWithoutMutation(t *testing.T) {
	t.Parallel()

	intents := []trustedReleaseInstallIntent{
		trustedReleaseInstallAbsentOnly,
		trustedReleaseInstallAllowReplacement,
	}
	for _, intent := range intents {
		t.Run(installIntentNameForTest(intent), func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			fixture.writeDifferentInstalled(t)
			manifestPath := fixture.store.slotPath + "/" + trustedReleaseManifestLeaf
			require.NoError(t, os.WriteFile(manifestPath, []byte(`{"invalid":true}`), 0600))
			result, err := fixture.authority.install(
				t.Context(), fixture.store.slot, intent, fixture.callerArchive(),
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assertZeroTrustedReleaseInstallResult(t, result, "corrupt canonical rejection")
			raw, readErr := os.ReadFile(manifestPath)
			require.NoError(t, readErr)
			assert.Equal(t, `{"invalid":true}`, string(raw))
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallFailsClosedForUnsafeInstalledSlotMetadata(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseInstallFixture(t)
	old := fixture.writeDifferentInstalled(t)
	require.NoError(t, os.Chmod(fixture.store.slotPath, 0755))

	result, err := fixture.authority.install(
		t.Context(), fixture.store.slot, trustedReleaseInstallAllowReplacement, fixture.callerArchive(),
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	if diff := cmp.Diff(trustedReleaseInstallResult{}, result, trustedReleaseInstallCmpOptions()...); diff != "" {
		t.Errorf("unsafe installed slot result mismatch (-want +got):\n%s", diff)
	}
	assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, old)
	assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
}

func TestReleaseAuthorityInstallRejectsCancellationAndUnsupportedPolicyBeforeEffects(t *testing.T) {
	t.Parallel()

	t.Run("pre-cancelled", func(t *testing.T) {
		fixture := newTrustedReleaseInstallFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result, err := fixture.authority.install(
			ctx, fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assertZeroTrustedReleaseInstallResult(t, result, "pre-cancelled install")
		assert.Zero(t, fixture.checksumRequests)
		assert.Zero(t, fixture.archiveRequests)
		assert.NoDirExists(t, fixture.store.slotPath)
	})

	t.Run("unaudited architecture", func(t *testing.T) {
		fixture := newTrustedReleaseInstallFixture(t)
		result, err := fixture.authority.install(
			t.Context(),
			releaseSlot{version: fixture.store.slot.version, architecture: "aarch64"},
			trustedReleaseInstallAbsentOnly,
			fixture.callerArchive(),
		)
		require.Error(t, err)
		assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		assertZeroTrustedReleaseInstallResult(t, result, "unsupported policy rejection")
		assert.Zero(t, fixture.checksumRequests)
		assert.Zero(t, fixture.archiveRequests)
	})
}

func TestAppendTrustedReleaseInstallErrorPreservesFirstDomainError(t *testing.T) {
	t.Parallel()

	cause := errs.New(
		errs.CodeVMStateInvalid,
		"release cleanup authority failed",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("vm-1"),
		errs.WithDetails(map[string]any{"existing": true}),
	)
	got := appendTrustedReleaseInstallError(nil, "release install slot lease", cause)
	assert.Same(t, cause, got)
	assert.Equal(t, errs.CodeVMStateInvalid, cause.Code)
	assert.Equal(t, errs.ClassConflict, cause.Class)
	assert.Equal(t, "vm-1", cause.Entity)
	assert.Equal(t, true, cause.Details["existing"])
}

func TestReleaseAuthorityInstallRetainsCompletedResultWhenSlotReleaseFails(t *testing.T) {
	t.Parallel()

	type canonicalState uint8
	const (
		canonicalAbsent canonicalState = iota
		canonicalIdentical
		canonicalDifferent
	)
	tests := map[string]struct {
		canonical canonicalState
		intent    trustedReleaseInstallIntent
		want      trustedReleaseInstallOutcome
		commitKey string
	}{
		"installed": {
			canonical: canonicalAbsent, intent: trustedReleaseInstallAbsentOnly,
			want: trustedReleaseInstallInstalled, commitKey: "release_installed",
		},
		"unchanged": {
			canonical: canonicalIdentical, intent: trustedReleaseInstallAbsentOnly,
			want: trustedReleaseInstallUnchanged,
		},
		"replaced": {
			canonical: canonicalDifferent, intent: trustedReleaseInstallAllowReplacement,
			want: trustedReleaseInstallReplaced, commitKey: "release_replaced",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseInstallFixture(t)
			switch tc.canonical {
			case canonicalIdentical:
				fixture.writeInstalled(t, fixture.manifest, fixture.firecracker, fixture.jailer)
			case canonicalDifferent:
				fixture.writeDifferentInstalled(t)
			}
			fixture.failReleaseSlotUnlock(t)

			result, err := fixture.authority.install(
				t.Context(), fixture.store.slot, tc.intent, fixture.callerArchive(),
			)
			require.Error(t, err)
			want := trustedReleaseInstallResult{outcome: tc.want, manifest: fixture.manifest}
			if diff := cmp.Diff(want, result, trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("post-cleanup result mismatch (-want +got):\n%s", diff)
			}
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.ErrorIs(t, err, unix.EIO)
			if tc.commitKey == "" {
				assert.NotContains(t, domainErr.Details, "release_installed")
				assert.NotContains(t, domainErr.Details, "release_replaced")
			} else {
				assert.Equal(t, true, domainErr.Details[tc.commitKey])
			}
			assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, fixture.manifest)
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

type trustedReleaseInstallFixture struct {
	instances        *instanceAuthority
	instanceRoot     string
	store            *trustedReleaseStoreFixture
	authority        *releaseAuthority
	slot             releaseSlot
	archive          []byte
	manifest         trustedReleaseManifest
	firecracker      []byte
	jailer           []byte
	checksumRequests int
	archiveRequests  int
	checksumURLs     []string
	archiveURLs      []string
}

func newTrustedReleaseInstallFixture(t *testing.T) *trustedReleaseInstallFixture {
	t.Helper()

	instances, instanceRoot := newTestInstanceAuthority(t)
	store := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.Remove(store.slotPath))
	store.deps.linkAnonymousLeaf = linkAnonymousTrustedReleaseLeafForTest
	prefix, err := trustedReleaseCandidatePrefix(store.slot)
	require.NoError(t, err)
	store.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
		return prefix + "00000000000000000000000000000000.tmp", nil
	}

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	firecracker := trustedReleaseTestELF("install Firecracker")
	jailer := trustedReleaseTestELF("install Jailer")
	setTrustedReleaseSelectedArchiveBytesForTest(t, archiveFixture, firecracker, jailer)
	archive := archiveFixture.compressed(t)
	archiveDigest := sha256.Sum256(archive)
	firecrackerDigest := sha256.Sum256(firecracker)
	jailerDigest := sha256.Sum256(jailer)
	manifest := trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          store.slot,
		archiveDigest: archiveDigest,
		firecracker: trustedReleaseExecutable{
			digest:    firecrackerDigest,
			sizeBytes: uint64(len(firecracker)),
		},
		jailer: trustedReleaseExecutable{
			digest:    jailerDigest,
			sizeBytes: uint64(len(jailer)),
		},
	}

	var authorityFixture *trustedReleaseInstallFixture
	authority := newReleaseAuthorityWithPolicy(instances, store.deps, store.policy)
	source, err := newTrustedReleaseSource(store.slot)
	require.NoError(t, err)
	checksumBody := hex.EncodeToString(archiveDigest[:]) + "  " + source.archiveName + "\n"
	authority.checksumAuthority.client.Transport = trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			authorityFixture.checksumRequests++
			authorityFixture.checksumURLs = append(authorityFixture.checksumURLs, request.URL.String())
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(bytes.NewBufferString(checksumBody)),
				ContentLength: int64(len(checksumBody)),
				Header:        make(http.Header),
			}, nil
		},
	)
	authority.archiveFetcher.client.Transport = trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			authorityFixture.archiveRequests++
			authorityFixture.archiveURLs = append(authorityFixture.archiveURLs, request.URL.String())
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(bytes.NewReader(archive)),
				ContentLength: int64(len(archive)),
				Header: http.Header{
					"Content-Length": []string{strconv.Itoa(len(archive))},
				},
			}, nil
		},
	)

	authorityFixture = &trustedReleaseInstallFixture{
		instances:    instances,
		instanceRoot: instanceRoot,
		store:        store,
		authority:    authority,
		slot:         releaseSlot{version: "../1.16.1", architecture: "x86_64"},
		archive:      archive,
		manifest:     manifest,
		firecracker:  firecracker,
		jailer:       jailer,
	}
	return authorityFixture
}

func (fixture *trustedReleaseInstallFixture) callerArchive() *trustedReleaseCallerArchive {
	return &trustedReleaseCallerArchive{
		reader:    bytes.NewReader(fixture.archive),
		sizeBytes: uint64(len(fixture.archive)),
	}
}

func (fixture *trustedReleaseInstallFixture) failReleaseSlotUnlock(t *testing.T) {
	t.Helper()

	realFlock := fixture.instances.deps.flock
	releaseLockFD := -1
	failed := false
	fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
		err := realFlock(ctx, fd, how)
		if err == nil && how == unix.LOCK_EX|unix.LOCK_NB && releaseLockFD < 0 {
			releaseLockFD = fd
		}
		if err == nil && how == unix.LOCK_UN && fd == releaseLockFD && !failed {
			failed = true
			return unix.EIO
		}
		return err
	}
}

func installIntentNameForTest(intent trustedReleaseInstallIntent) string {
	if intent == trustedReleaseInstallAbsentOnly {
		return "absent-only"
	}
	return "allow-replacement"
}

func (fixture *trustedReleaseInstallFixture) writeInstalled(
	t *testing.T,
	manifest trustedReleaseManifest,
	firecracker []byte,
	jailer []byte,
) {
	t.Helper()

	require.NoError(t, os.Mkdir(fixture.store.slotPath, os.FileMode(trustedReleaseStoreDirectoryMode)))
	require.NoError(t, os.WriteFile(
		fixture.store.slotPath+"/"+trustedReleaseFirecrackerLeaf,
		firecracker,
		os.FileMode(trustedReleaseStoreExecutableMode),
	))
	require.NoError(t, os.WriteFile(
		fixture.store.slotPath+"/"+trustedReleaseJailerLeaf,
		jailer,
		os.FileMode(trustedReleaseStoreExecutableMode),
	))
	writeTrustedReleaseManifestFile(t, fixture.store.slotPath, manifest)
}

func (fixture *trustedReleaseInstallFixture) writeDifferentInstalled(t *testing.T) trustedReleaseManifest {
	t.Helper()

	firecracker := trustedReleaseTestELF("previous install Firecracker")
	jailer := trustedReleaseTestELF("previous install Jailer")
	archiveDigest := sha256.Sum256([]byte("previous install archive"))
	firecrackerDigest := sha256.Sum256(firecracker)
	jailerDigest := sha256.Sum256(jailer)
	manifest := trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          fixture.store.slot,
		archiveDigest: archiveDigest,
		firecracker: trustedReleaseExecutable{
			digest:    firecrackerDigest,
			sizeBytes: uint64(len(firecracker)),
		},
		jailer: trustedReleaseExecutable{
			digest:    jailerDigest,
			sizeBytes: uint64(len(jailer)),
		},
	}
	fixture.writeInstalled(t, manifest, firecracker, jailer)
	return manifest
}

func trustedReleaseInstallCmpOptions() []cmp.Option {
	return []cmp.Option{cmp.AllowUnexported(
		trustedReleaseInstallResult{},
		trustedReleaseManifest{},
		releaseSlot{},
		trustedReleaseExecutable{},
	)}
}

func assertZeroTrustedReleaseInstallResult(
	t *testing.T,
	got trustedReleaseInstallResult,
	description string,
) {
	t.Helper()

	if diff := cmp.Diff(trustedReleaseInstallResult{}, got, trustedReleaseInstallCmpOptions()...); diff != "" {
		t.Errorf("%s result mismatch (-want +got):\n%s", description, diff)
	}
}

func assertInstalledTrustedReleaseMatchesManifest(
	t *testing.T,
	slotPath string,
	manifest trustedReleaseManifest,
) {
	t.Helper()

	raw, err := os.ReadFile(slotPath + "/" + trustedReleaseManifestLeaf)
	require.NoError(t, err)
	got, err := decodeTrustedReleaseManifest(raw)
	require.NoError(t, err)
	if diff := cmp.Diff(
		manifest,
		got,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("installed manifest mismatch (-want +got):\n%s", diff)
	}
}

func assertInstalledTrustedReleaseMatchesArchive(t *testing.T, fixture *trustedReleaseInstallFixture) {
	t.Helper()

	for leaf, want := range map[string][]byte{
		trustedReleaseFirecrackerLeaf: fixture.firecracker,
		trustedReleaseJailerLeaf:      fixture.jailer,
	} {
		got, err := os.ReadFile(fixture.store.slotPath + "/" + leaf)
		require.NoError(t, err)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("installed %s mismatch (-want +got):\n%s", leaf, diff)
		}
	}
	assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, fixture.manifest)
}
