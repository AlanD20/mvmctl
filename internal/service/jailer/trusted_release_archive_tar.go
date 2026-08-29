package jailer

import (
	"bytes"
	"math"
	"strconv"
	"strings"
)

const (
	trustedReleaseTarBlockBytes         = 512
	trustedReleaseTarNameOffset         = 0
	trustedReleaseTarModeOffset         = 100
	trustedReleaseTarUIDOffset          = 108
	trustedReleaseTarGIDOffset          = 116
	trustedReleaseTarSizeOffset         = 124
	trustedReleaseTarModTimeOffset      = 136
	trustedReleaseTarChecksumOffset     = 148
	trustedReleaseTarTypeOffset         = 156
	trustedReleaseTarLinkNameOffset     = 157
	trustedReleaseTarMagicOffset        = 257
	trustedReleaseTarVersionOffset      = 263
	trustedReleaseTarUserNameOffset     = 265
	trustedReleaseTarGroupNameOffset    = 297
	trustedReleaseTarDeviceMajorOffset  = 329
	trustedReleaseTarDeviceMinorOffset  = 337
	trustedReleaseTarGNUExtensionOffset = 345
	trustedReleaseTarTypeRegular        = byte('0')
	trustedReleaseTarTypePAX            = byte('x')
)

var trustedReleaseTarMagic = [8]byte{'u', 's', 't', 'a', 'r', 0, '0', '0'}

type trustedReleaseTarHeader struct {
	name        string
	mode        uint64
	uid         uint64
	gid         uint64
	sizeBytes   uint64
	modTime     uint64
	typeFlag    byte
	linkName    string
	userName    string
	groupName   string
	deviceMajor uint64
	deviceMinor uint64
}

// CRITICAL: The accepted release archives use one narrow GNU header encoding. Parsing the raw block keeps hidden tar
// extension records, base-256 numbers, sparse metadata, and non-canonical padding visible to the policy instead of
// allowing a permissive archive reader to normalize them first.
func parseTrustedReleaseTarHeader(raw []byte) (trustedReleaseTarHeader, error) {
	if len(raw) != trustedReleaseTarBlockBytes {
		return trustedReleaseTarHeader{}, trustedReleaseArchiveFormatError(
			"trusted release tar header has invalid length",
		)
	}
	checksum, err := parseTrustedReleaseTarChecksum(raw[trustedReleaseTarChecksumOffset:trustedReleaseTarTypeOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	var actualChecksum uint64
	for index, value := range raw {
		if index >= trustedReleaseTarChecksumOffset && index < trustedReleaseTarTypeOffset {
			actualChecksum += uint64(' ')
			continue
		}
		actualChecksum += uint64(value)
	}
	if actualChecksum != checksum {
		return trustedReleaseTarHeader{}, trustedReleaseArchiveFormatError(
			"trusted release tar header checksum does not match",
		)
	}
	if !bytes.Equal(raw[trustedReleaseTarMagicOffset:trustedReleaseTarUserNameOffset], trustedReleaseTarMagic[:]) {
		return trustedReleaseTarHeader{}, trustedReleaseArchiveFormatError(
			"trusted release tar header is not in the audited GNU format",
		)
	}
	if !allTrustedReleaseArchiveBytesZero(raw[trustedReleaseTarGNUExtensionOffset:]) {
		return trustedReleaseTarHeader{}, trustedReleaseArchiveFormatError(
			"trusted release tar header contains a GNU extension",
		)
	}

	name, err := parseTrustedReleaseTarString(raw[trustedReleaseTarNameOffset:trustedReleaseTarModeOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	mode, err := parseTrustedReleaseTarOctal(raw[trustedReleaseTarModeOffset:trustedReleaseTarUIDOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	uid, err := parseTrustedReleaseTarOctal(raw[trustedReleaseTarUIDOffset:trustedReleaseTarGIDOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	gid, err := parseTrustedReleaseTarOctal(raw[trustedReleaseTarGIDOffset:trustedReleaseTarSizeOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	sizeBytes, err := parseTrustedReleaseTarOctal(raw[trustedReleaseTarSizeOffset:trustedReleaseTarModTimeOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	modTime, err := parseTrustedReleaseTarOctal(raw[trustedReleaseTarModTimeOffset:trustedReleaseTarChecksumOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	linkName, err := parseTrustedReleaseTarString(raw[trustedReleaseTarLinkNameOffset:trustedReleaseTarMagicOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	userName, err := parseTrustedReleaseTarString(raw[trustedReleaseTarUserNameOffset:trustedReleaseTarGroupNameOffset])
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	groupName, err := parseTrustedReleaseTarString(
		raw[trustedReleaseTarGroupNameOffset:trustedReleaseTarDeviceMajorOffset],
	)
	if err != nil {
		return trustedReleaseTarHeader{}, err
	}
	if !allTrustedReleaseArchiveBytesZero(
		raw[trustedReleaseTarDeviceMajorOffset:trustedReleaseTarGNUExtensionOffset],
	) {
		return trustedReleaseTarHeader{}, trustedReleaseArchiveFormatError(
			"trusted release tar header contains a device field",
		)
	}

	return trustedReleaseTarHeader{
		name:        name,
		mode:        mode,
		uid:         uid,
		gid:         gid,
		sizeBytes:   sizeBytes,
		modTime:     modTime,
		typeFlag:    raw[trustedReleaseTarTypeOffset],
		linkName:    linkName,
		userName:    userName,
		groupName:   groupName,
		deviceMajor: 0,
		deviceMinor: 0,
	}, nil
}

func parseTrustedReleaseTarString(raw []byte) (string, error) {
	terminator := bytes.IndexByte(raw, 0)
	if terminator < 0 || !allTrustedReleaseArchiveBytesZero(raw[terminator+1:]) {
		return "", trustedReleaseArchiveFormatError(
			"trusted release tar header contains a non-canonical string field",
		)
	}
	return string(raw[:terminator]), nil
}

func parseTrustedReleaseTarOctal(raw []byte) (uint64, error) {
	if len(raw) < 2 || raw[len(raw)-1] != 0 {
		return 0, trustedReleaseArchiveFormatError(
			"trusted release tar header contains a non-canonical numeric field",
		)
	}
	for _, value := range raw[:len(raw)-1] {
		if value < '0' || value > '7' {
			return 0, trustedReleaseArchiveFormatError(
				"trusted release tar header contains a non-octal numeric field",
			)
		}
	}
	value, err := strconv.ParseUint(string(raw[:len(raw)-1]), 8, 64)
	if err != nil {
		return 0, trustedReleaseArchiveFormatError(
			"trusted release tar header numeric field is outside policy",
		)
	}
	return value, nil
}

func parseTrustedReleaseTarChecksum(raw []byte) (uint64, error) {
	if len(raw) != trustedReleaseTarTypeOffset-trustedReleaseTarChecksumOffset || raw[6] != 0 || raw[7] != ' ' {
		return 0, trustedReleaseArchiveFormatError(
			"trusted release tar header contains a non-canonical checksum field",
		)
	}
	for _, value := range raw[:6] {
		if value < '0' || value > '7' {
			return 0, trustedReleaseArchiveFormatError(
				"trusted release tar header contains a non-octal checksum field",
			)
		}
	}
	value, err := strconv.ParseUint(string(raw[:6]), 8, 64)
	if err != nil {
		return 0, trustedReleaseArchiveFormatError(
			"trusted release tar header checksum is outside policy",
		)
	}
	return value, nil
}

// CRITICAL: PAX metadata is syntax only. It never grants path, size, ownership, mode, or time authority.
func validateTrustedReleasePAXRecords(raw []byte) error {
	if len(raw) == 0 || len(raw) > trustedReleaseArchiveMaxPAXBytes {
		return trustedReleaseArchiveFormatError("trusted release PAX payload size is outside policy")
	}
	seen := make(map[string]struct{}, 3)
	for offset := 0; offset < len(raw); {
		space := bytes.IndexByte(raw[offset:], ' ')
		if space <= 0 {
			return trustedReleaseArchiveFormatError("trusted release PAX record length is malformed")
		}
		lengthField := string(raw[offset : offset+space])
		declaredLength, err := parseTrustedReleaseCanonicalUint(lengthField, 16)
		if err != nil || declaredLength > uint64(len(raw)-offset) {
			return trustedReleaseArchiveFormatError("trusted release PAX record length is outside policy")
		}
		if strconv.FormatUint(declaredLength, 10) != lengthField || declaredLength <= uint64(space+1) {
			return trustedReleaseArchiveFormatError("trusted release PAX record length is not canonical")
		}
		recordEnd := offset + int(declaredLength)
		record := raw[offset:recordEnd]
		if record[len(record)-1] != '\n' {
			return trustedReleaseArchiveFormatError("trusted release PAX record is not newline terminated")
		}
		assignment := record[space+1 : len(record)-1]
		equals := bytes.IndexByte(assignment, '=')
		if equals <= 0 || equals == len(assignment)-1 {
			return trustedReleaseArchiveFormatError("trusted release PAX record assignment is malformed")
		}
		key := string(assignment[:equals])
		value := string(assignment[equals+1:])
		if _, duplicate := seen[key]; duplicate {
			return trustedReleaseArchiveFormatError("trusted release PAX record key is duplicated")
		}
		seen[key] = struct{}{}
		if err := validateTrustedReleasePAXValue(key, value); err != nil {
			return err
		}
		offset = recordEnd
	}
	if _, present := seen["mtime"]; !present {
		return trustedReleaseArchiveFormatError("trusted release PAX payload is missing mtime")
	}
	return nil
}

func validateTrustedReleasePAXValue(key, value string) error {
	switch key {
	case "uid", "gid":
		if _, err := parseTrustedReleaseCanonicalUint(value, 32); err != nil {
			return trustedReleaseArchiveFormatError("trusted release PAX identity value is not canonical")
		}
	case "mtime":
		parts := strings.Split(value, ".")
		if len(parts) != 2 {
			return trustedReleaseArchiveFormatError("trusted release PAX mtime is not a decimal fraction")
		}
		seconds, err := parseTrustedReleaseCanonicalUint(parts[0], 64)
		if err != nil || seconds > math.MaxInt64 || len(parts[1]) == 0 || len(parts[1]) > 9 {
			return trustedReleaseArchiveFormatError("trusted release PAX mtime is outside policy")
		}
		for _, value := range []byte(parts[1]) {
			if value < '0' || value > '9' {
				return trustedReleaseArchiveFormatError("trusted release PAX mtime is not canonical")
			}
		}
	default:
		return trustedReleaseArchiveFormatError("trusted release PAX payload contains an unexpected key")
	}
	return nil
}

func parseTrustedReleaseCanonicalUint(value string, bitSize int) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, strconv.ErrSyntax
	}
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	return strconv.ParseUint(value, 10, bitSize)
}

func allTrustedReleaseArchiveBytesZero(raw []byte) bool {
	for _, value := range raw {
		if value != 0 {
			return false
		}
	}
	return true
}

func trustedReleaseArchiveFormatError(message string) error {
	return trustedReleaseStoreUntrusted(message, nil)
}
