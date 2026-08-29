package jailer

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"mvmctl/pkg/errs"
)

const (
	// TrustedReleaseInstallAction selects the exact trusted-release install protocol.
	TrustedReleaseInstallAction = "jailer-release-install"
	// TrustedReleaseRemoveAction selects the exact trusted-release removal protocol.
	TrustedReleaseRemoveAction = "jailer-release-remove"
)

// ReleaseSlot identifies one canonical Firecracker release pair.
type ReleaseSlot struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}

// TrustedReleaseInstallRequest is the complete install authority request.
type TrustedReleaseInstallRequest struct {
	Slot             ReleaseSlot `json:"slot"`
	AllowReplacement bool        `json:"allow_replacement"`
}

// UnmarshalJSON rejects null or missing scalar fields that encoding/json would otherwise silently zero.
func (request *TrustedReleaseInstallRequest) UnmarshalJSON(raw []byte) error {
	type installRequestWire struct {
		Slot             *ReleaseSlot `json:"slot"`
		AllowReplacement *bool        `json:"allow_replacement"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire installRequestWire
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if wire.Slot == nil {
		return fmt.Errorf("trusted release install slot is required")
	}
	if wire.AllowReplacement == nil {
		return fmt.Errorf("trusted release install allow_replacement is required")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trusted release install request contains trailing JSON")
		}
		return err
	}
	request.Slot = *wire.Slot
	request.AllowReplacement = *wire.AllowReplacement
	return nil
}

// TrustedReleaseRemoveRequest is the complete removal authority request.
type TrustedReleaseRemoveRequest struct {
	Slot ReleaseSlot `json:"slot"`
}

// TrustedReleaseInstallOutcome is the closed install state transition.
type TrustedReleaseInstallOutcome string

const (
	// TrustedReleaseInstalled means an absent canonical slot was installed.
	TrustedReleaseInstalled TrustedReleaseInstallOutcome = "installed"
	// TrustedReleaseReplaced means a different unreferenced canonical slot was replaced.
	TrustedReleaseReplaced TrustedReleaseInstallOutcome = "replaced"
	// TrustedReleaseUnchanged means the canonical slot already contained the exact release.
	TrustedReleaseUnchanged TrustedReleaseInstallOutcome = "unchanged"
)

// TrustedReleaseExecutableResult is manifest-derived executable identity.
type TrustedReleaseExecutableResult struct {
	SHA256    string `json:"sha256"`
	SizeBytes uint64 `json:"size_bytes"`
}

// TrustedReleaseInstallResult is the complete authoritative install result.
type TrustedReleaseInstallResult struct {
	Outcome       TrustedReleaseInstallOutcome   `json:"outcome"`
	Slot          ReleaseSlot                    `json:"slot"`
	ArchiveSHA256 string                         `json:"archive_sha256"`
	Firecracker   TrustedReleaseExecutableResult `json:"firecracker"`
	Jailer        TrustedReleaseExecutableResult `json:"jailer"`
}

// TrustedReleaseRemoveResult is the complete authoritative removal result.
type TrustedReleaseRemoveResult struct {
	Removed bool `json:"removed"`
}

type trustedReleaseInstallEffect func(
	context.Context,
	releaseSlot,
	trustedReleaseInstallIntent,
	*trustedReleaseCallerArchive,
) (trustedReleaseInstallResult, error)

type trustedReleaseRemoveEffect func(context.Context, releaseSlot) (bool, error)

// TrustedReleaseServer owns the root-side trusted-release authority.
// CRITICAL: Its named methods accept only closed typed intent and never accept a path, URL, digest, or reference
// bypass.
type TrustedReleaseServer struct {
	authority *releaseAuthority
	install   trustedReleaseInstallEffect
	remove    trustedReleaseRemoveEffect
}

// NewTrustedReleaseServer constructs the concrete root-side trusted-release authority.
func NewTrustedReleaseServer() *TrustedReleaseServer {
	authority := newReleaseAuthority()
	return &TrustedReleaseServer{
		authority: authority,
		install:   authority.install,
		remove:    authority.removeInstalled,
	}
}

func newTrustedReleaseServerWithEffects(
	install trustedReleaseInstallEffect,
	remove trustedReleaseRemoveEffect,
) *TrustedReleaseServer {
	return &TrustedReleaseServer{
		authority: &releaseAuthority{},
		install:   install,
		remove:    remove,
	}
}

// Validate checks the receiver-facing release slot without deriving any path or source.
func (slot ReleaseSlot) Validate() error {
	if err := validateReleaseSlotValue(slot.private()); err != nil {
		return errs.New(errs.CodeValidationFailed, err.Error())
	}
	return nil
}

func (slot ReleaseSlot) private() releaseSlot {
	return releaseSlot{version: slot.Version, architecture: slot.Architecture}
}

// Validate checks the complete receiver-facing install request.
func (request TrustedReleaseInstallRequest) Validate() error {
	return request.Slot.Validate()
}

// Validate checks the complete receiver-facing removal request.
func (request TrustedReleaseRemoveRequest) Validate() error {
	return request.Slot.Validate()
}

// Validate checks that an install result is exact manifest-derived authority for request.
func (result TrustedReleaseInstallResult) Validate(request TrustedReleaseInstallRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	switch result.Outcome {
	case TrustedReleaseInstalled, TrustedReleaseReplaced, TrustedReleaseUnchanged:
	default:
		return errs.New(errs.CodeValidationFailed, "trusted release install outcome is invalid")
	}
	if err := result.Slot.Validate(); err != nil {
		return err
	}
	if result.Slot != request.Slot {
		return errs.New(errs.CodeValidationFailed, "trusted release install result slot does not match request")
	}
	if !authorityHashPattern.MatchString(result.ArchiveSHA256) {
		return errs.New(errs.CodeValidationFailed, "trusted release install archive hash is not lowercase SHA-256")
	}
	if err := validateTrustedReleaseExecutableResult("Firecracker", result.Firecracker); err != nil {
		return err
	}
	return validateTrustedReleaseExecutableResult("Jailer", result.Jailer)
}

func validateTrustedReleaseExecutableResult(name string, result TrustedReleaseExecutableResult) error {
	if !authorityHashPattern.MatchString(result.SHA256) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("trusted release install %s hash is not lowercase SHA-256", name),
		)
	}
	if result.SizeBytes < trustedReleaseExecutableMinBytes || result.SizeBytes > trustedReleaseExecutableMaxBytes {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("trusted release install %s size is outside policy", name),
		)
	}
	return nil
}

// Install applies one exact trusted-release install request.
// CRITICAL: A nonzero caller payload is forwarded once as the raw reader. The private authority alone owns exact
// length, EOF, digest, extraction, and publication admission. Zero length selects the independent root-origin fetch.
func (server *TrustedReleaseServer) Install(
	ctx context.Context,
	request TrustedReleaseInstallRequest,
	payload io.Reader,
	payloadLength uint64,
) (TrustedReleaseInstallResult, error) {
	if ctx == nil {
		return TrustedReleaseInstallResult{}, errs.New(errs.CodeValidationFailed, "trusted release context is required")
	}
	if err := request.Validate(); err != nil {
		return TrustedReleaseInstallResult{}, err
	}
	if server == nil || server.authority == nil || server.install == nil {
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeBinaryTrustedInstallFailed,
			"trusted release server is not active for installation",
			errs.WithClass(errs.ClassInternal),
		)
	}
	if payloadLength > trustedReleaseArchiveMaxBytes || (payloadLength == 0) != (payload == nil) {
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeValidationFailed,
			"trusted release install payload reader and length are inconsistent",
		)
	}

	intent := trustedReleaseInstallAbsentOnly
	if request.AllowReplacement {
		intent = trustedReleaseInstallAllowReplacement
	}
	var archive *trustedReleaseCallerArchive
	if payloadLength > 0 {
		archive = &trustedReleaseCallerArchive{reader: payload, sizeBytes: payloadLength}
	}
	privateResult, effectErr := server.install(ctx, request.Slot.private(), intent, archive)
	if privateResult == (trustedReleaseInstallResult{}) {
		if effectErr != nil {
			return TrustedReleaseInstallResult{}, effectErr
		}
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeInternal,
			"trusted release authority returned neither an install result nor an error",
			errs.WithClass(errs.ClassInternal),
		)
	}
	result, projectionErr := projectTrustedReleaseInstallResult(request, privateResult)
	if projectionErr != nil {
		return TrustedReleaseInstallResult{}, errs.WrapMsg(
			errs.CodeInternal,
			"project trusted release install result",
			errors.Join(effectErr, projectionErr),
			errs.WithClass(errs.ClassInternal),
		)
	}
	return result, effectErr
}

// Remove applies one exact trusted-release removal request.
func (server *TrustedReleaseServer) Remove(
	ctx context.Context,
	request TrustedReleaseRemoveRequest,
) (TrustedReleaseRemoveResult, error) {
	if ctx == nil {
		return TrustedReleaseRemoveResult{}, errs.New(errs.CodeValidationFailed, "trusted release context is required")
	}
	if err := request.Validate(); err != nil {
		return TrustedReleaseRemoveResult{}, err
	}
	if server == nil || server.authority == nil || server.remove == nil {
		return TrustedReleaseRemoveResult{}, errs.New(
			errs.CodeBinaryRemoveFailed,
			"trusted release server is not active for removal",
			errs.WithClass(errs.ClassInternal),
		)
	}
	removed, err := server.remove(ctx, request.Slot.private())
	return TrustedReleaseRemoveResult{Removed: removed}, err
}

func projectTrustedReleaseInstallResult(
	request TrustedReleaseInstallRequest,
	private trustedReleaseInstallResult,
) (TrustedReleaseInstallResult, error) {
	if err := validateTrustedReleaseManifest(private.manifest); err != nil {
		return TrustedReleaseInstallResult{}, err
	}
	var outcome TrustedReleaseInstallOutcome
	switch private.outcome {
	case trustedReleaseInstallInstalled:
		outcome = TrustedReleaseInstalled
	case trustedReleaseInstallReplaced:
		outcome = TrustedReleaseReplaced
	case trustedReleaseInstallUnchanged:
		outcome = TrustedReleaseUnchanged
	default:
		return TrustedReleaseInstallResult{}, errs.New(
			errs.CodeInternal,
			"trusted release authority returned an invalid install outcome",
			errs.WithClass(errs.ClassInternal),
		)
	}
	result := TrustedReleaseInstallResult{
		Outcome: outcome,
		Slot: ReleaseSlot{
			Version:      private.manifest.slot.version,
			Architecture: private.manifest.slot.architecture,
		},
		ArchiveSHA256: hex.EncodeToString(private.manifest.archiveDigest[:]),
		Firecracker: TrustedReleaseExecutableResult{
			SHA256:    hex.EncodeToString(private.manifest.firecracker.digest[:]),
			SizeBytes: private.manifest.firecracker.sizeBytes,
		},
		Jailer: TrustedReleaseExecutableResult{
			SHA256:    hex.EncodeToString(private.manifest.jailer.digest[:]),
			SizeBytes: private.manifest.jailer.sizeBytes,
		},
	}
	if err := result.Validate(request); err != nil {
		return TrustedReleaseInstallResult{}, err
	}
	return result, nil
}
