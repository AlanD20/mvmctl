package loopmount

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

const (
	maxRootfsOSReleaseBytes     = 64 << 10
	maxRootfsOSReleaseLineBytes = 4 << 10
	maxRootfsOSReleaseFields    = 256
)

// parseRootfsOSRelease accepts the bounded assignment subset needed for the standardized ID field. It rejects shell
// expansion syntax instead of attempting to evaluate guest-controlled input. os-release specifies "linux" when ID
// is absent.
func parseRootfsOSRelease(content []byte) (string, error) {
	if len(content) > maxRootfsOSReleaseBytes {
		return "", rootfsAtomicError("os-release content exceeds the supported byte bound", nil)
	}
	if !utf8.Valid(content) {
		return "", rootfsAtomicError("os-release content is not valid UTF-8", nil)
	}
	for _, character := range content {
		if character == '\r' || character == 0 || (character < ' ' && character != '\n' && character != '\t') {
			return "", rootfsAtomicError("os-release content contains a control character", nil)
		}
	}

	seen := make(map[string]struct{})
	osType := "linux"
	lines := bytes.Split(content, []byte{'\n'})
	fields := 0
	for lineNumber, rawLine := range lines {
		if len(rawLine) > maxRootfsOSReleaseLineBytes {
			return "", rootfsAtomicError("os-release line exceeds the supported byte bound", nil)
		}
		line := string(rawLine)
		if line == "" || line[0] == '#' {
			continue
		}
		fields++
		if fields > maxRootfsOSReleaseFields {
			return "", rootfsAtomicError("os-release assignment count exceeds the supported bound", nil)
		}
		separator := bytes.IndexByte(rawLine, '=')
		if separator <= 0 {
			return "", rootfsAtomicError(fmt.Sprintf("os-release line %d is not an assignment", lineNumber+1), nil)
		}
		key := line[:separator]
		if !validRootfsOSReleaseKey(key) {
			return "", rootfsAtomicError(fmt.Sprintf("os-release line %d has an invalid key", lineNumber+1), nil)
		}
		if _, duplicate := seen[key]; duplicate {
			return "", rootfsAtomicError("os-release key is duplicated", nil)
		}
		seen[key] = struct{}{}
		value, err := parseRootfsOSReleaseValue(line[separator+1:])
		if err != nil {
			return "", rootfsAtomicError(fmt.Sprintf("parse os-release line %d: %v", lineNumber+1, err), err)
		}
		if key == "ID" {
			if !validOSIdentifier(value) {
				return "", rootfsAtomicError("os-release ID is not a valid OS identifier", nil)
			}
			osType = value
		}
	}
	return osType, nil
}

func validRootfsOSReleaseKey(value string) bool {
	if value == "" || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func parseRootfsOSReleaseValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return "", fmt.Errorf("quoted value is not terminated exactly")
		}
		body := value[1 : len(value)-1]
		if quote == '\'' {
			if bytes.IndexByte([]byte(body), quote) >= 0 {
				return "", fmt.Errorf("quoted value contains its delimiter")
			}
			return body, nil
		}
		decoded := make([]byte, 0, len(body))
		for index := 0; index < len(body); index++ {
			character := body[index]
			if character == '"' {
				return "", fmt.Errorf("quoted value contains an unescaped delimiter")
			}
			if character != '\\' {
				decoded = append(decoded, character)
				continue
			}
			index++
			if index >= len(body) {
				return "", fmt.Errorf("quoted value ends with an escape")
			}
			escaped := body[index]
			if escaped != '\\' && escaped != '"' && escaped != '$' && escaped != '`' {
				return "", fmt.Errorf("quoted value has an unsupported escape")
			}
			decoded = append(decoded, escaped)
		}
		return string(decoded), nil
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return "", fmt.Errorf("unquoted value contains an unsupported character")
	}
	return value, nil
}
