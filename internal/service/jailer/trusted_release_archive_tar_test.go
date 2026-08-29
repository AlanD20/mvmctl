package jailer

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestParseTrustedReleaseTarHeaderAcceptsAuditedGNURegularHeader(t *testing.T) {
	t.Parallel()

	raw := trustedReleaseTarHeaderForTest(t, trustedReleaseTarHeader{
		name:      "release-v1.16.1-x86_64/firecracker-v1.16.1-x86_64",
		mode:      0755,
		uid:       0,
		gid:       100,
		sizeBytes: 3527456,
		modTime:   1782726985,
		typeFlag:  trustedReleaseTarTypeRegular,
		userName:  "jaehoc",
		groupName: "amazon",
	})

	got, err := parseTrustedReleaseTarHeader(raw)
	require.NoError(t, err)
	want := trustedReleaseTarHeader{
		name:      "release-v1.16.1-x86_64/firecracker-v1.16.1-x86_64",
		mode:      0755,
		uid:       0,
		gid:       100,
		sizeBytes: 3527456,
		modTime:   1782726985,
		typeFlag:  trustedReleaseTarTypeRegular,
		userName:  "jaehoc",
		groupName: "amazon",
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(trustedReleaseTarHeader{})); diff != "" {
		t.Errorf("parsed trusted release tar header mismatch (-want +got):\n%s", diff)
	}
}

func TestParseTrustedReleaseTarHeaderRejectsNonCanonicalEncoding(t *testing.T) {
	t.Parallel()

	valid := trustedReleaseTarHeaderForTest(t, trustedReleaseTarHeader{
		name:      "././@PaxHeader",
		sizeBytes: 28,
		typeFlag:  trustedReleaseTarTypePAX,
	})
	tests := map[string]func([]byte){
		"invalid checksum": func(raw []byte) { raw[0] ^= 1 },
		"noncanonical checksum field": func(raw []byte) {
			copy(raw[trustedReleaseTarChecksumOffset:trustedReleaseTarTypeOffset], []byte(" 10215\x00 "))
		},
		"wrong magic":       func(raw []byte) { raw[trustedReleaseTarMagicOffset] = 'x' },
		"wrong version":     func(raw []byte) { raw[trustedReleaseTarVersionOffset] = '1' },
		"base-256 size":     func(raw []byte) { raw[trustedReleaseTarSizeOffset] = 0x80 },
		"space-padded mode": func(raw []byte) { raw[trustedReleaseTarModeOffset] = ' ' },
		"unterminated name": func(raw []byte) {
			for index := trustedReleaseTarNameOffset; index < trustedReleaseTarModeOffset; index++ {
				raw[index] = 'a'
			}
		},
		"nonzero name padding":  func(raw []byte) { raw[99] = 'x' },
		"nonzero GNU extension": func(raw []byte) { raw[trustedReleaseTarGNUExtensionOffset] = '1' },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw := bytes.Clone(valid)
			mutate(raw)
			if name != "invalid checksum" && name != "noncanonical checksum field" {
				writeTrustedReleaseTarChecksumForTest(t, raw)
			}
			got, err := parseTrustedReleaseTarHeader(raw)
			assert.Empty(t, got)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestParseTrustedReleaseTarHeaderRejectsWrongBlockLength(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, trustedReleaseTarBlockBytes - 1, trustedReleaseTarBlockBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			got, err := parseTrustedReleaseTarHeader(make([]byte, size))
			assert.Empty(t, got)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestValidateTrustedReleasePAXRecordsAcceptsAuditedKeyVariants(t *testing.T) {
	t.Parallel()

	tests := map[string][]trustedReleasePAXRecordForTest{
		"mtime only": {
			{key: "mtime", value: "1731494783.8975384"},
		},
		"uid and mtime": {
			{key: "uid", value: "29852511"},
			{key: "mtime", value: "1782726985.0"},
		},
		"all keys in alternate order": {
			{key: "gid", value: "600260513"},
			{key: "mtime", value: "1772127146.0"},
			{key: "uid", value: "645722313"},
		},
	}
	for name, records := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.NoError(t, validateTrustedReleasePAXRecords(trustedReleasePAXForTest(records...)))
		})
	}
}

func TestValidateTrustedReleasePAXRecordsRejectsMalformedOrExpandedMetadata(t *testing.T) {
	t.Parallel()

	validMtime := trustedReleasePAXForTest(trustedReleasePAXRecordForTest{key: "mtime", value: "1782726985.0"})
	tests := map[string][]byte{
		"empty":                 nil,
		"missing mtime":         trustedReleasePAXForTest(trustedReleasePAXRecordForTest{key: "uid", value: "1"}),
		"duplicate mtime":       append(bytes.Clone(validMtime), validMtime...),
		"unexpected key":        trustedReleasePAXForTest(trustedReleasePAXRecordForTest{key: "path", value: "root"}),
		"leading-zero length":   append([]byte{'0'}, validMtime...),
		"short declared record": bytes.Replace(validMtime, []byte("22 mtime"), []byte("21 mtime"), 1),
		"long declared record":  bytes.Replace(validMtime, []byte("22 mtime"), []byte("23 mtime"), 1),
		"CRLF":                  bytes.Replace(validMtime, []byte{'\n'}, []byte{'\r', '\n'}, 1),
		"mtime without fraction": trustedReleasePAXForTest(
			trustedReleasePAXRecordForTest{key: "mtime", value: "1782726985"},
		),
		"negative mtime": trustedReleasePAXForTest(
			trustedReleasePAXRecordForTest{key: "mtime", value: "-1.0"},
		),
		"oversized uid": trustedReleasePAXForTest(
			trustedReleasePAXRecordForTest{key: "uid", value: "4294967296"},
			trustedReleasePAXRecordForTest{key: "mtime", value: "1782726985.0"},
		),
		"leading-zero gid": trustedReleasePAXForTest(
			trustedReleasePAXRecordForTest{key: "gid", value: "01"},
			trustedReleasePAXRecordForTest{key: "mtime", value: "1782726985.0"},
		),
		"too many fractional digits": trustedReleasePAXForTest(
			trustedReleasePAXRecordForTest{key: "mtime", value: "1782726985.1234567890"},
		),
		"over policy bound": bytes.Repeat([]byte{'x'}, trustedReleaseArchiveMaxPAXBytes+1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validateTrustedReleasePAXRecords(raw)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

type trustedReleasePAXRecordForTest struct {
	key   string
	value string
}

func trustedReleasePAXForTest(records ...trustedReleasePAXRecordForTest) []byte {
	var result []byte
	for _, record := range records {
		body := " " + record.key + "=" + record.value + "\n"
		length := len(body) + 1
		for {
			encoded := strconv.Itoa(length) + body
			if len(encoded) == length {
				result = append(result, encoded...)
				break
			}
			length = len(encoded)
		}
	}
	return result
}

func trustedReleaseTarHeaderForTest(t *testing.T, header trustedReleaseTarHeader) []byte {
	t.Helper()

	raw := make([]byte, trustedReleaseTarBlockBytes)
	writeTrustedReleaseTarStringForTest(t, raw[0:100], header.name)
	writeTrustedReleaseTarOctalForTest(t, raw[100:108], header.mode)
	writeTrustedReleaseTarOctalForTest(t, raw[108:116], header.uid)
	writeTrustedReleaseTarOctalForTest(t, raw[116:124], header.gid)
	writeTrustedReleaseTarOctalForTest(t, raw[124:136], header.sizeBytes)
	writeTrustedReleaseTarOctalForTest(t, raw[136:148], header.modTime)
	raw[156] = header.typeFlag
	writeTrustedReleaseTarStringForTest(t, raw[157:257], header.linkName)
	copy(raw[257:263], []byte{'u', 's', 't', 'a', 'r', 0})
	copy(raw[263:265], []byte{'0', '0'})
	writeTrustedReleaseTarStringForTest(t, raw[265:297], header.userName)
	writeTrustedReleaseTarStringForTest(t, raw[297:329], header.groupName)
	writeTrustedReleaseTarOctalForTest(t, raw[329:337], header.deviceMajor)
	writeTrustedReleaseTarOctalForTest(t, raw[337:345], header.deviceMinor)
	writeTrustedReleaseTarChecksumForTest(t, raw)
	return raw
}

func writeTrustedReleaseTarStringForTest(t *testing.T, destination []byte, value string) {
	t.Helper()
	require.Less(t, len(value), len(destination))
	copy(destination, value)
}

func writeTrustedReleaseTarOctalForTest(t *testing.T, destination []byte, value uint64) {
	t.Helper()
	encoded := fmt.Sprintf("%0*o", len(destination)-1, value)
	require.Len(t, encoded, len(destination)-1)
	copy(destination, encoded)
}

func writeTrustedReleaseTarChecksumForTest(t *testing.T, raw []byte) {
	t.Helper()
	require.Len(t, raw, trustedReleaseTarBlockBytes)
	for index := trustedReleaseTarChecksumOffset; index < trustedReleaseTarTypeOffset; index++ {
		raw[index] = ' '
	}
	var sum uint64
	for _, value := range raw {
		sum += uint64(value)
	}
	encoded := fmt.Sprintf("%06o\x00 ", sum)
	require.Len(t, encoded, trustedReleaseTarTypeOffset-trustedReleaseTarChecksumOffset)
	copy(raw[trustedReleaseTarChecksumOffset:trustedReleaseTarTypeOffset], encoded)
}
