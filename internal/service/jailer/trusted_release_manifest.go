package jailer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"mvmctl/pkg/errs"
)

const (
	trustedReleaseManifestSchemaVersion = uint32(1)
	maxTrustedReleaseManifestBytes      = 4 * 1024
	trustedReleaseManifestJSONMaxDepth  = 1
)

type trustedReleaseExecutableDigest [sha256.Size]byte

type trustedReleaseExecutable struct {
	digest    trustedReleaseExecutableDigest
	sizeBytes uint64
}

type trustedReleaseManifest struct {
	schemaVersion uint32
	slot          releaseSlot
	archiveDigest trustedReleaseArchiveDigest
	firecracker   trustedReleaseExecutable
	jailer        trustedReleaseExecutable
}

type trustedReleaseManifestWire struct {
	SchemaVersion uint32                         `json:"schema_version"`
	Release       trustedReleaseManifestSlotWire `json:"release"`
	ArchiveSHA256 string                         `json:"archive_sha256"`
	Firecracker   trustedReleaseExecutableWire   `json:"firecracker"`
	Jailer        trustedReleaseExecutableWire   `json:"jailer"`
}

type trustedReleaseManifestSlotWire struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}

type trustedReleaseExecutableWire struct {
	SHA256    string `json:"sha256"`
	SizeBytes uint64 `json:"size_bytes"`
}

var trustedReleaseManifestObjectFields = map[string]map[string]struct{}{
	"": {
		"schema_version": {},
		"release":        {},
		"archive_sha256": {},
		"firecracker":    {},
		"jailer":         {},
	},
	"release": {
		"version":      {},
		"architecture": {},
	},
	"firecracker": {
		"sha256":     {},
		"size_bytes": {},
	},
	"jailer": {
		"sha256":     {},
		"size_bytes": {},
	},
}

func encodeTrustedReleaseManifest(manifest trustedReleaseManifest) ([]byte, error) {
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(trustedReleaseManifestToWire(manifest))
	if err != nil {
		return nil, trustedReleaseManifestError("encode trusted release manifest", err)
	}
	if len(raw) > maxTrustedReleaseManifestBytes {
		return nil, trustedReleaseManifestError("encoded trusted release manifest exceeds size limit", nil)
	}
	return raw, nil
}

func decodeTrustedReleaseManifest(raw []byte) (trustedReleaseManifest, error) {
	if len(raw) == 0 {
		return trustedReleaseManifest{}, trustedReleaseManifestError("trusted release manifest is empty", nil)
	}
	if len(raw) > maxTrustedReleaseManifestBytes {
		return trustedReleaseManifest{}, trustedReleaseManifestError(
			"trusted release manifest exceeds size limit",
			nil,
		)
	}
	if err := validateTrustedReleaseManifestJSON(raw); err != nil {
		return trustedReleaseManifest{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire trustedReleaseManifestWire
	if err := decoder.Decode(&wire); err != nil {
		return trustedReleaseManifest{}, trustedReleaseManifestError("decode trusted release manifest", err)
	}
	if err := requireTrustedReleaseManifestJSONEOF(decoder); err != nil {
		return trustedReleaseManifest{}, trustedReleaseManifestError(
			"decode trailing trusted release manifest input",
			err,
		)
	}

	manifest, err := trustedReleaseManifestFromWire(wire)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return trustedReleaseManifest{}, err
	}
	return manifest, nil
}

func validateTrustedReleaseManifest(manifest trustedReleaseManifest) error {
	if manifest.schemaVersion != trustedReleaseManifestSchemaVersion {
		return trustedReleaseManifestError("unsupported trusted release manifest schema", nil)
	}
	if err := validateReleaseSlotValue(manifest.slot); err != nil {
		return trustedReleaseManifestError("trusted release manifest slot is invalid", err)
	}
	if err := validateTrustedReleaseExecutable("Firecracker", manifest.firecracker); err != nil {
		return err
	}
	return validateTrustedReleaseExecutable("Jailer", manifest.jailer)
}

func validateTrustedReleaseExecutable(name string, executable trustedReleaseExecutable) error {
	if executable.sizeBytes < trustedReleaseExecutableMinBytes ||
		executable.sizeBytes > trustedReleaseExecutableMaxBytes {
		return trustedReleaseManifestError(
			fmt.Sprintf("trusted release %s size is outside the admitted range", name),
			nil,
		)
	}
	return nil
}

func (manifest trustedReleaseManifest) releaseIdentity() (releaseIdentity, error) {
	if err := validateTrustedReleaseManifest(manifest); err != nil {
		return releaseIdentity{}, err
	}
	identity := releaseIdentity{
		version:           manifest.slot.version,
		architecture:      manifest.slot.architecture,
		firecrackerSHA256: hex.EncodeToString(manifest.firecracker.digest[:]),
		jailerSHA256:      hex.EncodeToString(manifest.jailer.digest[:]),
	}
	if err := validateReleaseIdentityValue(identity); err != nil {
		return releaseIdentity{}, trustedReleaseManifestError(err.Error(), nil)
	}
	return identity, nil
}

func validateTrustedReleaseManifestJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanTrustedReleaseManifestJSONValue(decoder, "", 0); err != nil {
		return trustedReleaseManifestError("decode strict trusted release manifest", err)
	}
	if err := requireTrustedReleaseManifestJSONEOF(decoder); err != nil {
		return trustedReleaseManifestError("decode trailing trusted release manifest input", err)
	}
	return nil
}

func scanTrustedReleaseManifestJSONValue(decoder *json.Decoder, objectName string, depth int) error {
	if depth > trustedReleaseManifestJSONMaxDepth {
		return fmt.Errorf("JSON nesting exceeds %d levels", trustedReleaseManifestJSONMaxDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("JSON object %q must be an object", objectName)
	}
	allowed, ok := trustedReleaseManifestObjectFields[objectName]
	if !ok {
		return fmt.Errorf("unexpected JSON object %q", objectName)
	}

	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		fieldToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("JSON object field is not a string")
		}
		if _, exists := allowed[field]; !exists {
			return fmt.Errorf("unknown JSON field %q", field)
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return fmt.Errorf("duplicate JSON field %q conflicts with %q", field, existing)
			}
		}
		seen[field] = struct{}{}

		switch field {
		case "release", "firecracker", "jailer":
			if err := scanTrustedReleaseManifestJSONValue(decoder, field, depth+1); err != nil {
				return err
			}
		default:
			if err := scanTrustedReleaseManifestJSONScalar(decoder); err != nil {
				return err
			}
		}
	}
	endToken, err := decoder.Token()
	if err != nil {
		return err
	}
	if endToken != json.Delim('}') {
		return fmt.Errorf("JSON object %q has an invalid terminator", objectName)
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("JSON object %q is missing required fields", objectName)
	}
	return nil
}

func scanTrustedReleaseManifestJSONScalar(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return fmt.Errorf("JSON field must not be null")
	}
	if _, ok := token.(json.Delim); ok {
		return fmt.Errorf("unexpected nested JSON value")
	}
	return nil
}

func requireTrustedReleaseManifestJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("trusted release manifest contains trailing input")
	}
	return err
}

func trustedReleaseManifestToWire(manifest trustedReleaseManifest) trustedReleaseManifestWire {
	return trustedReleaseManifestWire{
		SchemaVersion: manifest.schemaVersion,
		Release: trustedReleaseManifestSlotWire{
			Version:      manifest.slot.version,
			Architecture: manifest.slot.architecture,
		},
		ArchiveSHA256: hex.EncodeToString(manifest.archiveDigest[:]),
		Firecracker: trustedReleaseExecutableWire{
			SHA256:    hex.EncodeToString(manifest.firecracker.digest[:]),
			SizeBytes: manifest.firecracker.sizeBytes,
		},
		Jailer: trustedReleaseExecutableWire{
			SHA256:    hex.EncodeToString(manifest.jailer.digest[:]),
			SizeBytes: manifest.jailer.sizeBytes,
		},
	}
}

func trustedReleaseManifestFromWire(wire trustedReleaseManifestWire) (trustedReleaseManifest, error) {
	archiveDigest, err := decodeTrustedReleaseArchiveDigest(wire.ArchiveSHA256)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	firecrackerDigest, err := decodeTrustedReleaseExecutableDigest("Firecracker", wire.Firecracker.SHA256)
	if err != nil {
		return trustedReleaseManifest{}, err
	}
	jailerDigest, err := decodeTrustedReleaseExecutableDigest("Jailer", wire.Jailer.SHA256)
	if err != nil {
		return trustedReleaseManifest{}, err
	}

	return trustedReleaseManifest{
		schemaVersion: wire.SchemaVersion,
		slot: releaseSlot{
			version:      wire.Release.Version,
			architecture: wire.Release.Architecture,
		},
		archiveDigest: archiveDigest,
		firecracker: trustedReleaseExecutable{
			digest:    firecrackerDigest,
			sizeBytes: wire.Firecracker.SizeBytes,
		},
		jailer: trustedReleaseExecutable{
			digest:    jailerDigest,
			sizeBytes: wire.Jailer.SizeBytes,
		},
	}, nil
}

func decodeTrustedReleaseArchiveDigest(raw string) (trustedReleaseArchiveDigest, error) {
	if !authorityHashPattern.MatchString(raw) {
		return trustedReleaseArchiveDigest{}, trustedReleaseManifestError(
			"trusted release manifest archive digest is not lowercase SHA-256",
			nil,
		)
	}
	var digest trustedReleaseArchiveDigest
	if _, err := hex.Decode(digest[:], []byte(raw)); err != nil {
		return trustedReleaseArchiveDigest{}, trustedReleaseManifestError(
			"decode trusted release manifest archive digest",
			err,
		)
	}
	return digest, nil
}

func decodeTrustedReleaseExecutableDigest(
	name string,
	raw string,
) (trustedReleaseExecutableDigest, error) {
	if !authorityHashPattern.MatchString(raw) {
		return trustedReleaseExecutableDigest{}, trustedReleaseManifestError(
			fmt.Sprintf("trusted release manifest %s digest is not lowercase SHA-256", name),
			nil,
		)
	}
	var digest trustedReleaseExecutableDigest
	if _, err := hex.Decode(digest[:], []byte(raw)); err != nil {
		return trustedReleaseExecutableDigest{}, trustedReleaseManifestError(
			fmt.Sprintf("decode trusted release manifest %s digest", name),
			err,
		)
	}
	return digest, nil
}

func trustedReleaseManifestError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeBinaryUntrusted, message)
	}
	return errs.WrapMsg(errs.CodeBinaryUntrusted, message, cause, errs.WithClass(errs.ClassValidation))
}
