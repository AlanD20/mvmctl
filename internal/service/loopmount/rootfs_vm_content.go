package loopmount

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxRootfsProvisionTextBytes = 128 << 10
	maxRootfsProvisionLineBytes = 8 << 10
	maxRootfsPasswdBytes        = 1 << 20
	maxRootfsAgentBytes         = 64 << 20
)

type rootfsGuestAccount struct {
	name string
	uid  uint32
	gid  uint32
	home string
}

type rootfsProvisionFile struct {
	path    string
	content []byte
	mode    uint32
	uid     uint32
	gid     uint32
}

func rewriteRootfsHosts(current []byte, hostname string) ([]byte, error) {
	lines, err := splitBoundedRootfsLines(current)
	if err != nil {
		return nil, rootfsAtomicError("rewrite managed guest hosts file: "+err.Error(), err)
	}
	result := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "127.0.1.1" {
			continue
		}
		result = append(result, line)
	}
	result = append(result, "127.0.1.1\t"+hostname)
	rendered, err := renderBoundedRootfsLines(result)
	if err != nil {
		return nil, rootfsAtomicError("rewrite managed guest hosts file: "+err.Error(), err)
	}
	return rendered, nil
}

// rewriteRootfsFstab applies the closed Firecracker root-disk contract: preserve unrelated rows, disable
// identity-, partition-, EFI-, and swap-based mounts, and add noatime to the unpartitioned /dev/vda root mount.
// Rationale: legacy image rows must not claim devices that the fixed microVM disk topology does not provide.
func rewriteRootfsFstab(current []byte) ([]byte, error) {
	lines, err := splitBoundedRootfsLines(current)
	if err != nil {
		return nil, rootfsAtomicError("rewrite managed guest fstab: "+err.Error(), err)
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return nil, rootfsAtomicError("managed guest fstab contains a malformed active row", nil)
		}
		source, mountpoint, filesystem := fields[0], fields[1], fields[2]
		if strings.HasPrefix(source, "PARTUUID=") || strings.HasPrefix(source, "UUID=") ||
			isRootfsPartitionDevice(source) || mountpoint == "/boot/efi" || filesystem == "swap" {
			lines[index] = "# mvmctl: " + line
			continue
		}
		if source == "/dev/vda" && mountpoint == "/" && len(fields) >= 4 {
			fields[3] = addRootfsMountOption(fields[3], "noatime")
			lines[index] = strings.Join(fields, " ")
		}
	}
	rendered, err := renderBoundedRootfsLines(lines)
	if err != nil {
		return nil, rootfsAtomicError("rewrite managed guest fstab: "+err.Error(), err)
	}
	return rendered, nil
}

func splitBoundedRootfsLines(content []byte) ([]string, error) {
	if len(content) > maxRootfsProvisionTextBytes {
		return nil, fmt.Errorf("content exceeds %d-byte limit", maxRootfsProvisionTextBytes)
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return nil, fmt.Errorf("content is not valid NUL-free UTF-8")
	}
	text := strings.TrimSuffix(string(content), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if len(line) > maxRootfsProvisionLineBytes {
			return nil, fmt.Errorf("line exceeds %d-byte limit", maxRootfsProvisionLineBytes)
		}
		if strings.HasSuffix(line, "\r") {
			return nil, fmt.Errorf("crlf content is unsupported")
		}
	}
	return lines, nil
}

func renderBoundedRootfsLines(lines []string) ([]byte, error) {
	for _, line := range lines {
		if len(line) > maxRootfsProvisionLineBytes {
			return nil, fmt.Errorf("line exceeds %d-byte limit", maxRootfsProvisionLineBytes)
		}
	}
	rendered := []byte(strings.Join(lines, "\n") + "\n")
	if len(rendered) > maxRootfsProvisionTextBytes {
		return nil, fmt.Errorf("content exceeds %d-byte limit", maxRootfsProvisionTextBytes)
	}
	return rendered, nil
}

func isRootfsPartitionDevice(source string) bool {
	if !strings.HasPrefix(source, "/dev/vda") || len(source) == len("/dev/vda") {
		return false
	}
	for _, character := range source[len("/dev/vda"):] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func addRootfsMountOption(options, option string) string {
	for _, existing := range strings.Split(options, ",") {
		if existing == option {
			return options
		}
	}
	return option + "," + options
}

func parseRootfsGuestAccount(passwd []byte, username string) (rootfsGuestAccount, error) {
	if len(passwd) == 0 || len(passwd) > maxRootfsPasswdBytes || bytes.IndexByte(passwd, 0) >= 0 ||
		!utf8.Valid(passwd) {
		return rootfsGuestAccount{}, rootfsAtomicError("managed guest passwd file is malformed or unbounded", nil)
	}
	lines, err := splitRootfsPasswdLines(passwd)
	if err != nil {
		return rootfsGuestAccount{}, err
	}
	var matched rootfsGuestAccount
	matches := 0
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[0] == "" {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest passwd row is malformed", nil)
		}
		if fields[0] != username {
			continue
		}
		uid, parseErr := strconv.ParseUint(fields[2], 10, 32)
		if parseErr != nil || strconv.FormatUint(uid, 10) != fields[2] {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest account UID is not canonical", parseErr)
		}
		// CRITICAL identity invariant: MaxUint32 is chown(2)'s "do not change" sentinel, not an account identity.
		if uid == math.MaxUint32 {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest account UID is reserved", nil)
		}
		gid, parseErr := strconv.ParseUint(fields[3], 10, 32)
		if parseErr != nil || strconv.FormatUint(gid, 10) != fields[3] {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest account GID is not canonical", parseErr)
		}
		if gid == math.MaxUint32 {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest account GID is reserved", nil)
		}
		home, homeErr := canonicalRootfsGuestPath(fields[5])
		if homeErr != nil {
			return rootfsGuestAccount{}, rootfsAtomicError("managed guest account home is invalid", homeErr)
		}
		matched = rootfsGuestAccount{name: username, uid: uint32(uid), gid: uint32(gid), home: home}
		matches++
	}
	if matches == 0 {
		return rootfsGuestAccount{}, rootfsAtomicError("managed guest account does not exist", nil)
	}
	if matches != 1 {
		return rootfsGuestAccount{}, rootfsAtomicError("managed guest account is duplicated", nil)
	}
	if username == "root" && (matched.uid != 0 || matched.gid != 0 || matched.home != "root") {
		return rootfsGuestAccount{}, rootfsAtomicError("managed root account identity is invalid", nil)
	}
	if username != "root" && matched.uid == 0 {
		return rootfsGuestAccount{}, rootfsAtomicError("managed non-root account has root authority", nil)
	}
	return matched, nil
}

func splitRootfsPasswdLines(passwd []byte) ([]string, error) {
	if passwd[len(passwd)-1] != '\n' {
		return nil, rootfsAtomicError("managed guest passwd file lacks a final newline", nil)
	}
	lines := strings.Split(string(passwd[:len(passwd)-1]), "\n")
	for _, line := range lines {
		if line == "" || len(line) > maxRootfsProvisionLineBytes || strings.ContainsRune(line, '\r') {
			return nil, rootfsAtomicError("managed guest passwd row is malformed", nil)
		}
	}
	return lines, nil
}

func canonicalRootfsGuestPath(path string) (string, error) {
	if len(path) < 2 || path[0] != '/' || len(path) > 4096 || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("path is not a bounded absolute guest path")
	}
	components := strings.Split(path[1:], "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." || len(component) > 255 {
			return "", fmt.Errorf("path contains an invalid component")
		}
	}
	return strings.Join(components, "/"), nil
}

func rootfsCapacityConverged(capacity, target, blockSize uint64) bool {
	return blockSize != 0 && capacity <= target && target-capacity < blockSize
}
