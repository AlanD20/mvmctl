package jailer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseWireValuesValidateClosedContract(t *testing.T) {
	validRequest := TrustedReleaseInstallRequest{
		Slot:             ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
		AllowReplacement: true,
	}
	validResult := TrustedReleaseInstallResult{
		Outcome:       TrustedReleaseInstalled,
		Slot:          validRequest.Slot,
		ArchiveSHA256: strings.Repeat("a", 64),
		Firecracker: TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("b", 64), SizeBytes: trustedReleaseExecutableMinBytes,
		},
		Jailer: TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("c", 64), SizeBytes: trustedReleaseExecutableMaxBytes,
		},
	}

	tests := map[string]struct {
		request TrustedReleaseInstallRequest
		result  TrustedReleaseInstallResult
		wantErr string
	}{
		"invalid_request_slot": {
			request: TrustedReleaseInstallRequest{Slot: ReleaseSlot{Version: "../1.16.1", Architecture: "x86_64"}},
			result:  validResult,
			wantErr: "release version is invalid",
		},
		"invalid_outcome": {
			request: validRequest,
			result: func() TrustedReleaseInstallResult {
				result := validResult
				result.Outcome = "repaired"
				return result
			}(),
			wantErr: "outcome is invalid",
		},
		"slot_mismatch": {
			request: validRequest,
			result: func() TrustedReleaseInstallResult {
				result := validResult
				result.Slot.Version = "1.16.0"
				return result
			}(),
			wantErr: "slot does not match",
		},
		"uppercase_archive_hash": {
			request: validRequest,
			result: func() TrustedReleaseInstallResult {
				result := validResult
				result.ArchiveSHA256 = strings.Repeat("A", 64)
				return result
			}(),
			wantErr: "archive hash",
		},
		"short_executable_hash": {
			request: validRequest,
			result: func() TrustedReleaseInstallResult {
				result := validResult
				result.Firecracker.SHA256 = strings.Repeat("b", 63)
				return result
			}(),
			wantErr: "Firecracker hash",
		},
		"executable_too_small": {
			request: validRequest,
			result: func() TrustedReleaseInstallResult {
				result := validResult
				result.Jailer.SizeBytes = trustedReleaseExecutableMinBytes - 1
				return result
			}(),
			wantErr: "Jailer size",
		},
		"valid": {request: validRequest, result: validResult},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.request.Validate()
			if tc.wantErr != "" && strings.Contains(tc.wantErr, "release version") {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)

			err = tc.result.Validate(tc.request)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTrustedReleaseServerProjectsEveryInstallOutcome(t *testing.T) {
	slot := releaseSlot{version: "1.16.1", architecture: "x86_64"}
	manifest := trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          slot,
		archiveDigest: trustedReleaseArchiveDigest{0xaa},
		firecracker: trustedReleaseExecutable{
			digest: trustedReleaseExecutableDigest{0xbb}, sizeBytes: trustedReleaseExecutableMinBytes,
		},
		jailer: trustedReleaseExecutable{
			digest: trustedReleaseExecutableDigest{0xcc}, sizeBytes: trustedReleaseExecutableMaxBytes,
		},
	}
	request := TrustedReleaseInstallRequest{
		Slot: ReleaseSlot{Version: slot.version, Architecture: slot.architecture},
	}
	tests := map[string]struct {
		privateOutcome trustedReleaseInstallOutcome
		wantOutcome    TrustedReleaseInstallOutcome
	}{
		"installed": {privateOutcome: trustedReleaseInstallInstalled, wantOutcome: TrustedReleaseInstalled},
		"replaced":  {privateOutcome: trustedReleaseInstallReplaced, wantOutcome: TrustedReleaseReplaced},
		"unchanged": {privateOutcome: trustedReleaseInstallUnchanged, wantOutcome: TrustedReleaseUnchanged},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gotPayload io.Reader
			var gotLength uint64
			server := newTrustedReleaseServerWithEffects(
				func(
					_ context.Context,
					gotSlot releaseSlot,
					intent trustedReleaseInstallIntent,
					archive *trustedReleaseCallerArchive,
				) (trustedReleaseInstallResult, error) {
					assert.Equal(t, slot, gotSlot)
					assert.Equal(t, trustedReleaseInstallAbsentOnly, intent)
					require.NotNil(t, archive)
					gotPayload = archive.reader
					gotLength = archive.sizeBytes
					return trustedReleaseInstallResult{outcome: tc.privateOutcome, manifest: manifest}, nil
				},
				func(context.Context, releaseSlot) (bool, error) { return false, nil },
			)
			payload := bytes.NewBufferString("archive")

			got, err := server.Install(t.Context(), request, payload, uint64(payload.Len()))

			require.NoError(t, err)
			assert.Same(t, payload, gotPayload)
			assert.Equal(t, uint64(7), gotLength)
			want := TrustedReleaseInstallResult{
				Outcome:       tc.wantOutcome,
				Slot:          request.Slot,
				ArchiveSHA256: "aa" + strings.Repeat("00", 31),
				Firecracker: TrustedReleaseExecutableResult{
					SHA256: "bb" + strings.Repeat("00", 31), SizeBytes: trustedReleaseExecutableMinBytes,
				},
				Jailer: TrustedReleaseExecutableResult{
					SHA256: "cc" + strings.Repeat("00", 31), SizeBytes: trustedReleaseExecutableMaxBytes,
				},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("trusted release install projection mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseServerUsesClosedPayloadAndRemoveSemantics(t *testing.T) {
	request := TrustedReleaseInstallRequest{
		Slot:             ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
		AllowReplacement: true,
	}
	sentinel := errs.New(
		errs.CodeBinaryRemoveFailed,
		"trusted release removal committed but cleanup failed",
		errs.WithDetails(map[string]any{"release_removed": true}),
	)
	installCalls := 0
	removeCalls := 0
	server := newTrustedReleaseServerWithEffects(
		func(
			_ context.Context,
			_ releaseSlot,
			intent trustedReleaseInstallIntent,
			archive *trustedReleaseCallerArchive,
		) (trustedReleaseInstallResult, error) {
			installCalls++
			assert.Equal(t, trustedReleaseInstallAllowReplacement, intent)
			assert.Nil(t, archive)
			return trustedReleaseInstallResult{}, errors.New("root fetch stopped")
		},
		func(_ context.Context, got releaseSlot) (bool, error) {
			removeCalls++
			assert.Equal(t, releaseSlot{version: "1.16.1", architecture: "x86_64"}, got)
			return true, sentinel
		},
	)

	_, err := server.Install(t.Context(), request, nil, 0)
	require.Error(t, err)
	assert.ErrorContains(t, err, "root fetch stopped")
	assert.Equal(t, 1, installCalls)

	removed, err := server.Remove(t.Context(), TrustedReleaseRemoveRequest{Slot: request.Slot})
	require.Error(t, err)
	assert.Same(t, sentinel, err)
	assert.Equal(t, TrustedReleaseRemoveResult{Removed: true}, removed)
	assert.Equal(t, 1, removeCalls)
}

func TestTrustedReleaseServerRequiresEitherInstallResultOrError(t *testing.T) {
	request := TrustedReleaseInstallRequest{
		Slot: ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
	}
	privateResult := trustedReleaseInstallResult{
		outcome: trustedReleaseInstallInstalled,
		manifest: trustedReleaseManifest{
			schemaVersion: trustedReleaseManifestSchemaVersion,
			slot:          request.Slot.private(),
			archiveDigest: trustedReleaseArchiveDigest{0xaa},
			firecracker: trustedReleaseExecutable{
				digest: trustedReleaseExecutableDigest{0xbb}, sizeBytes: trustedReleaseExecutableMinBytes,
			},
			jailer: trustedReleaseExecutable{
				digest: trustedReleaseExecutableDigest{0xcc}, sizeBytes: trustedReleaseExecutableMaxBytes,
			},
		},
	}
	partialErr := errs.New(
		errs.CodeBinaryTrustedInstallFailed,
		"trusted release install committed with uncertain durability",
		errs.WithDetails(map[string]any{"release_installed": true}),
	)

	tests := map[string]struct {
		privateResult trustedReleaseInstallResult
		effectErr     error
		wantResult    bool
		wantSameError bool
	}{
		"zero_result_without_error_fails_closed": {},
		"zero_result_preserves_effect_error": {
			effectErr:     partialErr,
			wantSameError: true,
		},
		"projected_result_preserves_partial_error": {
			privateResult: privateResult,
			effectErr:     partialErr,
			wantResult:    true,
			wantSameError: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			server := newTrustedReleaseServerWithEffects(
				func(
					context.Context,
					releaseSlot,
					trustedReleaseInstallIntent,
					*trustedReleaseCallerArchive,
				) (trustedReleaseInstallResult, error) {
					return tc.privateResult, tc.effectErr
				},
				func(context.Context, releaseSlot) (bool, error) { return false, nil },
			)

			got, err := server.Install(t.Context(), request, nil, 0)

			require.Error(t, err)
			if tc.wantSameError {
				assert.Same(t, tc.effectErr, err)
			} else {
				domainErr := errs.AsDomainError(err)
				require.NotNil(t, domainErr)
				assert.Equal(t, errs.CodeInternal, domainErr.Code)
				assert.Equal(t, errs.ClassInternal, domainErr.Class)
			}
			if tc.wantResult {
				want := TrustedReleaseInstallResult{
					Outcome:       TrustedReleaseInstalled,
					Slot:          request.Slot,
					ArchiveSHA256: "aa" + strings.Repeat("00", 31),
					Firecracker: TrustedReleaseExecutableResult{
						SHA256: "bb" + strings.Repeat("00", 31), SizeBytes: trustedReleaseExecutableMinBytes,
					},
					Jailer: TrustedReleaseExecutableResult{
						SHA256: "cc" + strings.Repeat("00", 31), SizeBytes: trustedReleaseExecutableMaxBytes,
					},
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("trusted release partial result mismatch (-want +got):\n%s", diff)
				}
				return
			}
			assert.Equal(t, TrustedReleaseInstallResult{}, got)
		})
	}
}

func TestTrustedReleaseServerRejectsInvalidInputsBeforeEffect(t *testing.T) {
	effectCalls := 0
	server := newTrustedReleaseServerWithEffects(
		func(
			context.Context,
			releaseSlot,
			trustedReleaseInstallIntent,
			*trustedReleaseCallerArchive,
		) (trustedReleaseInstallResult, error) {
			effectCalls++
			return trustedReleaseInstallResult{}, nil
		},
		func(context.Context, releaseSlot) (bool, error) {
			effectCalls++
			return false, nil
		},
	)
	valid := TrustedReleaseInstallRequest{Slot: ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"}}
	tests := map[string]func() error{
		"zero_length_with_reader": func() error {
			_, err := server.Install(t.Context(), valid, bytes.NewReader(nil), 0)
			return err
		},
		"nonzero_length_without_reader": func() error {
			_, err := server.Install(t.Context(), valid, nil, 1)
			return err
		},
		"oversized_payload": func() error {
			_, err := server.Install(t.Context(), valid, bytes.NewReader(nil), trustedReleaseArchiveMaxBytes+1)
			return err
		},
		"invalid_remove_slot": func() error {
			_, err := server.Remove(
				t.Context(),
				TrustedReleaseRemoveRequest{Slot: ReleaseSlot{Version: "1.16.1", Architecture: "riscv64"}},
			)
			return err
		},
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			err := run()
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
	assert.Zero(t, effectCalls)
}
