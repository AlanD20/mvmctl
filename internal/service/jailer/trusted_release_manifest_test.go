package jailer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseManifestCodecRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []releaseSlot{
		{version: "1.16.1", architecture: "x86_64"},
		{version: "0.22.0-beta5", architecture: "aarch64"},
	}
	for _, slot := range tests {
		t.Run(slot.architecture, func(t *testing.T) {
			t.Parallel()

			want := testTrustedReleaseManifest(slot)
			raw, err := encodeTrustedReleaseManifest(want)
			require.NoError(t, err)
			assert.LessOrEqual(t, len(raw), maxTrustedReleaseManifestBytes)

			got, err := decodeTrustedReleaseManifest(raw)
			require.NoError(t, err)
			if diff := cmp.Diff(
				want,
				got,
				cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
			); diff != "" {
				t.Errorf("trusted release manifest mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseManifestCodecUsesCanonicalClosedSchema(t *testing.T) {
	t.Parallel()

	manifest := testTrustedReleaseManifest(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	raw, err := encodeTrustedReleaseManifest(manifest)
	require.NoError(t, err)

	want := fmt.Sprintf(
		`{"schema_version":%d,"release":{"version":"%s","architecture":"%s"},`+
			`"archive_sha256":"%s","firecracker":{"sha256":"%s","size_bytes":%d},`+
			`"jailer":{"sha256":"%s","size_bytes":%d}}`,
		manifest.schemaVersion,
		manifest.slot.version,
		manifest.slot.architecture,
		hex.EncodeToString(manifest.archiveDigest[:]),
		hex.EncodeToString(manifest.firecracker.digest[:]),
		manifest.firecracker.sizeBytes,
		hex.EncodeToString(manifest.jailer.digest[:]),
		manifest.jailer.sizeBytes,
	)
	assert.Equal(t, want, string(raw))
	assert.NotContains(t, string(raw), "url")
	assert.NotContains(t, string(raw), "path")
	assert.NotContains(t, string(raw), "member")
}

func TestDecodeTrustedReleaseManifestRejectsMalformedSchema(t *testing.T) {
	t.Parallel()

	manifest := testTrustedReleaseManifest(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	validBytes, err := encodeTrustedReleaseManifest(manifest)
	require.NoError(t, err)
	valid := string(validBytes)
	archiveHash := hex.EncodeToString(manifest.archiveDigest[:])
	firecrackerHash := hex.EncodeToString(manifest.firecracker.digest[:])

	tests := map[string]string{
		"empty":              "",
		"whitespace_only":    " \n\t",
		"trailing":           valid + `{}`,
		"root_array":         `[]`,
		"unknown_top_level":  strings.Replace(valid, `"schema_version":`, `"unknown":1,"schema_version":`, 1),
		"case_variant_field": strings.Replace(valid, `"schema_version":`, `"SCHEMA_VERSION":`, 1),
		"duplicate_top_level": strings.Replace(
			valid,
			`"schema_version":`,
			`"schema_version":1,"schema_version":`,
			1,
		),
		"case_conflicting_field": strings.Replace(
			valid,
			`"schema_version":`,
			`"SCHEMA_VERSION":1,"schema_version":`,
			1,
		),
		"missing_top_level": strings.Replace(
			valid,
			`"archive_sha256":"`+archiveHash+`",`,
			"",
			1,
		),
		"release_null": strings.Replace(
			valid,
			`"release":{"version":"1.16.1","architecture":"x86_64"}`,
			`"release":null`,
			1,
		),
		"release_array": strings.Replace(
			valid,
			`"release":{"version":"1.16.1","architecture":"x86_64"}`,
			`"release":[]`,
			1,
		),
		"unknown_release_field": strings.Replace(valid, `"version":`, `"unknown":1,"version":`, 1),
		"duplicate_release_field": strings.Replace(
			valid,
			`"version":`,
			`"version":"1.16.1","version":`,
			1,
		),
		"missing_release_field": strings.Replace(valid, `"version":"1.16.1",`, "", 1),
		"invalid_version":       strings.Replace(valid, `"version":"1.16.1"`, `"version":"../1.16.1"`, 1),
		"invalid_architecture":  strings.Replace(valid, `"architecture":"x86_64"`, `"architecture":"arm64"`, 1),
		"executable_null": strings.Replace(
			valid,
			`"firecracker":{"sha256":"`+firecrackerHash+`","size_bytes":3527456}`,
			`"firecracker":null`,
			1,
		),
		"unknown_executable_field": strings.Replace(
			valid,
			`"sha256":"`+firecrackerHash+`"`,
			`"unknown":1,"sha256":"`+firecrackerHash+`"`,
			1,
		),
		"duplicate_executable_field": strings.Replace(
			valid,
			`"size_bytes":3527456`,
			`"size_bytes":3527456,"size_bytes":3527456`,
			1,
		),
		"missing_executable_field": strings.Replace(valid, `,"size_bytes":3527456`, "", 1),
		"unsupported_schema":       strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"schema_boolean":           strings.Replace(valid, `"schema_version":1`, `"schema_version":true`, 1),
		"uppercase_archive_hash": strings.Replace(
			valid,
			archiveHash,
			strings.ToUpper(archiveHash),
			1,
		),
		"short_archive_hash": strings.Replace(valid, archiveHash, archiveHash[:len(archiveHash)-1], 1),
		"non_hex_executable_hash": strings.Replace(
			valid,
			firecrackerHash,
			"g"+firecrackerHash[1:],
			1,
		),
		"size_below_minimum": strings.Replace(
			valid,
			`"size_bytes":3527456`,
			fmt.Sprintf(`"size_bytes":%d`, trustedReleaseExecutableMinBytes-1),
			1,
		),
		"size_above_maximum": strings.Replace(
			valid,
			`"size_bytes":3527456`,
			fmt.Sprintf(`"size_bytes":%d`, trustedReleaseExecutableMaxBytes+1),
			1,
		),
		"negative_size": strings.Replace(valid, `"size_bytes":3527456`, `"size_bytes":-1`, 1),
		"fractional_size": strings.Replace(
			valid,
			`"size_bytes":3527456`,
			`"size_bytes":3527456.5`,
			1,
		),
		"string_size": strings.Replace(valid, `"size_bytes":3527456`, `"size_bytes":"3527456"`, 1),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, decodeErr := decodeTrustedReleaseManifest([]byte(raw))
			require.Error(t, decodeErr)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(decodeErr).Code)
		})
	}
}

func TestDecodeTrustedReleaseManifestRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	_, err := decodeTrustedReleaseManifest([]byte(strings.Repeat(" ", maxTrustedReleaseManifestBytes+1)))
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

func TestEncodeTrustedReleaseManifestRejectsInvalidTypedValue(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseManifest){
		"unsupported_schema": func(manifest *trustedReleaseManifest) {
			manifest.schemaVersion++
		},
		"invalid_version": func(manifest *trustedReleaseManifest) {
			manifest.slot.version = "../1.16.1"
		},
		"invalid_architecture": func(manifest *trustedReleaseManifest) {
			manifest.slot.architecture = "arm64"
		},
		"firecracker_too_small": func(manifest *trustedReleaseManifest) {
			manifest.firecracker.sizeBytes = trustedReleaseExecutableMinBytes - 1
		},
		"jailer_too_large": func(manifest *trustedReleaseManifest) {
			manifest.jailer.sizeBytes = trustedReleaseExecutableMaxBytes + 1
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			manifest := testTrustedReleaseManifest(releaseSlot{version: "1.16.1", architecture: "x86_64"})
			mutate(&manifest)
			_, err := encodeTrustedReleaseManifest(manifest)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestTrustedReleaseManifestExecutableSizeBoundaries(t *testing.T) {
	t.Parallel()

	want := testTrustedReleaseManifest(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	want.firecracker.sizeBytes = trustedReleaseExecutableMinBytes
	want.jailer.sizeBytes = trustedReleaseExecutableMaxBytes

	raw, err := encodeTrustedReleaseManifest(want)
	require.NoError(t, err)
	padded := append(raw, bytes.Repeat([]byte{' '}, maxTrustedReleaseManifestBytes-len(raw))...)
	require.Len(t, padded, maxTrustedReleaseManifestBytes)
	got, err := decodeTrustedReleaseManifest(padded)
	require.NoError(t, err)
	if diff := cmp.Diff(
		want,
		got,
		cmp.AllowUnexported(trustedReleaseManifest{}, releaseSlot{}, trustedReleaseExecutable{}),
	); diff != "" {
		t.Errorf("trusted release manifest boundaries mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseManifestDerivesInstanceReleaseIdentity(t *testing.T) {
	t.Parallel()

	manifest := testTrustedReleaseManifest(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	want := releaseIdentity{
		version:           manifest.slot.version,
		architecture:      manifest.slot.architecture,
		firecrackerSHA256: hex.EncodeToString(manifest.firecracker.digest[:]),
		jailerSHA256:      hex.EncodeToString(manifest.jailer.digest[:]),
	}

	got, err := manifest.releaseIdentity()
	require.NoError(t, err)
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(releaseIdentity{})); diff != "" {
		t.Errorf("release identity mismatch (-want +got):\n%s", diff)
	}

	manifest.schemaVersion++
	_, err = manifest.releaseIdentity()
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

func testTrustedReleaseManifest(slot releaseSlot) trustedReleaseManifest {
	archiveDigest := sha256.Sum256([]byte("trusted release archive"))
	firecrackerDigest := sha256.Sum256([]byte("trusted Firecracker executable"))
	jailerDigest := sha256.Sum256([]byte("trusted Jailer executable"))

	return trustedReleaseManifest{
		schemaVersion: trustedReleaseManifestSchemaVersion,
		slot:          slot,
		archiveDigest: trustedReleaseArchiveDigest(archiveDigest),
		firecracker: trustedReleaseExecutable{
			digest:    trustedReleaseExecutableDigest(firecrackerDigest),
			sizeBytes: 3_527_456,
		},
		jailer: trustedReleaseExecutable{
			digest:    trustedReleaseExecutableDigest(jailerDigest),
			sizeBytes: 2_181_264,
		},
	}
}
