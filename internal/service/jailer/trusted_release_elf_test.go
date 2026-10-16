package jailer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestValidateTrustedReleaseELFHeaderAcceptsSelectedArchitecture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		architecture string
		machineLow   byte
	}{
		{name: "x86_64", architecture: "x86_64", machineLow: 62},
		{name: "aarch64", architecture: "aarch64", machineLow: 183},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(releaseSlot{
				version:      "1.16.1",
				architecture: tc.architecture,
			})
			require.NoError(t, err)
			header := auditedTrustedReleaseELFHeader()
			header[18] = tc.machineLow

			assert.NoError(t, validateTrustedReleaseELFHeader(header, 3_527_456, source))
		})
	}
}

// Rationale: Root admits executable bytes before they can become a trusted release. Every field below distinguishes the
// reviewed Firecracker/Jailer ELF shape from a truncated, wrong-architecture, or structurally different object.
func TestValidateTrustedReleaseELFHeaderRejectsUntrustedShape(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "truncated header", mutate: func(value []byte) []byte { return value[:63] }},
		{name: "extended header", mutate: func(value []byte) []byte { return append(value, 0) }},
		{name: "invalid magic", mutate: mutateTrustedReleaseELFByte(0, 0)},
		{name: "ELF32 class", mutate: mutateTrustedReleaseELFByte(4, 1)},
		{name: "big-endian encoding", mutate: mutateTrustedReleaseELFByte(5, 2)},
		{name: "old ident version", mutate: mutateTrustedReleaseELFByte(6, 0)},
		{name: "foreign OS ABI", mutate: mutateTrustedReleaseELFByte(7, 3)},
		{name: "non-zero ABI version", mutate: mutateTrustedReleaseELFByte(8, 1)},
		{name: "non-zero ident padding", mutate: mutateTrustedReleaseELFByte(15, 1)},
		{name: "executable object type", mutate: mutateTrustedReleaseELFByte(16, 2)},
		{name: "aarch64 machine for x86_64 slot", mutate: mutateTrustedReleaseELFByte(18, 183)},
		{name: "old object version", mutate: mutateTrustedReleaseELFByte(20, 0)},
		{name: "zero entry point", mutate: func(value []byte) []byte {
			for index := 24; index < 32; index++ {
				value[index] = 0
			}
			return value
		}},
		{name: "wrong program-header offset", mutate: mutateTrustedReleaseELFByte(32, 65)},
		{name: "wrong ELF header size", mutate: mutateTrustedReleaseELFByte(52, 63)},
		{name: "wrong program-header entry size", mutate: mutateTrustedReleaseELFByte(54, 55)},
		{name: "zero program headers", mutate: mutateTrustedReleaseELFByte(56, 0)},
		{name: "too many program headers", mutate: mutateTrustedReleaseELFByte(56, 65)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header := tc.mutate(auditedTrustedReleaseELFHeader())
			err := validateTrustedReleaseELFHeader(header, 3_527_456, source)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
		})
	}
}

// Rationale: Header fields are not meaningful admission bounds unless the complete declared program-header table fits
// within the separately measured file. These cases prove the exact inclusive file-size and table-extent boundaries.
func TestValidateTrustedReleaseELFHeaderBindsActualFileSize(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)

	tests := map[string]struct {
		sizeBytes uint64
		mutate    func([]byte)
		wantErr   bool
	}{
		"below executable minimum": {sizeBytes: 119, wantErr: true},
		"above executable maximum": {sizeBytes: 64*1024*1024 + 1, wantErr: true},
		// CONTRACT: audited header has e_phoff=64, e_phentsize=56, e_phnum=10; table end is byte 624.
		"truncated audited table": {sizeBytes: 623, wantErr: true},
		"exact audited table":     {sizeBytes: 624},
		// CONTRACT: maximum admitted e_phnum=64; table end is 64 + (56 * 64) = byte 3648.
		"truncated maximum table": {
			sizeBytes: 3647,
			mutate:    func(header []byte) { header[56] = 64 },
			wantErr:   true,
		},
		"exact maximum table": {
			sizeBytes: 3648,
			mutate:    func(header []byte) { header[56] = 64 },
		},
		"exact executable maximum": {sizeBytes: 64 * 1024 * 1024},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			header := auditedTrustedReleaseELFHeader()
			if tc.mutate != nil {
				tc.mutate(header)
			}
			err := validateTrustedReleaseELFHeader(header, tc.sizeBytes, source)
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateTrustedReleaseELFHeaderRejectsForgedSource(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	source.archiveRoot = "attacker-controlled"

	err = validateTrustedReleaseELFHeader(auditedTrustedReleaseELFHeader(), 3_527_456, source)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
}

func mutateTrustedReleaseELFByte(offset int, value byte) func([]byte) []byte {
	return func(header []byte) []byte {
		header[offset] = value
		return header
	}
}

// auditedTrustedReleaseELFHeader is the Firecracker v1.16.1 x86_64 header observed in the configured asset mirror. The
// validator contract is derived from the ELF specification and ADR-0016, not by calling the production parser.
func auditedTrustedReleaseELFHeader() []byte {
	return []byte{
		0x7f, 0x45, 0x4c, 0x46, 0x02, 0x01, 0x01, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x03, 0x00, 0x3e, 0x00, 0x01, 0x00, 0x00, 0x00,
		0x79, 0xe7, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x40, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xe0, 0xcb, 0x35, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x40, 0x00, 0x38, 0x00,
		0x0a, 0x00, 0x40, 0x00, 0x1d, 0x00, 0x1c, 0x00,
	}
}
