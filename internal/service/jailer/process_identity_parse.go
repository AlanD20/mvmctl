package jailer

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
)

func parseProcessStat(raw []byte, expectedPID int) (uint64, error) {
	// Linux proc_pid_stat(5) defines starttime as field 22. The command field may contain spaces and closing
	// parentheses, so the parser anchors the last ") " delimiter before counting fields.
	// See https://www.man7.org/linux/man-pages/man5/proc_pid_stat.5.html
	line, err := parseSingleProcessMetadataLine(raw, maxProcessStatBytes)
	if err != nil {
		return 0, fmt.Errorf("runtime process stat is malformed: %w", err)
	}
	opening := strings.IndexByte(line, '(')
	closing := strings.LastIndex(line, ") ")
	if opening < 2 || closing <= opening || line[opening-1] != ' ' {
		return 0, fmt.Errorf("runtime process stat is malformed")
	}
	pid, err := parseCanonicalPositiveInt(line[:opening-1])
	if err != nil {
		return 0, fmt.Errorf("runtime process stat PID is invalid")
	}
	if pid != expectedPID {
		return 0, fmt.Errorf("runtime process stat PID does not match the launch locator")
	}
	fields := strings.Fields(line[closing+2:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("runtime process stat is incomplete")
	}
	if !liveProcessState(fields[0]) {
		return 0, fmt.Errorf("runtime process is not live")
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || startTicks == 0 || strconv.FormatUint(startTicks, 10) != fields[19] {
		return 0, fmt.Errorf("runtime process start ticks are invalid")
	}
	return startTicks, nil
}

func liveProcessState(state string) bool {
	switch state {
	case "R", "S", "D", "T", "t", "W", "P", "I":
		return true
	default:
		return false
	}
}

func parseProcessStatus(raw []byte) (
	processCredentialIdentity,
	processCredentialIdentity,
	[]uint32,
	error,
) {
	// proc_pid_status(5) defines Uid/Gid as real, effective, saved-set, and filesystem IDs and Groups as the
	// supplementary group list. The receiver canonicalizes that kernel list before comparison.
	// See https://man7.org/linux/man-pages/man5/proc_pid_status.5.html
	if len(raw) == 0 || len(raw) > maxProcessStatusBytes || bytes.IndexByte(raw, 0) >= 0 {
		return processCredentialIdentity{}, processCredentialIdentity{}, nil,
			fmt.Errorf("runtime process status is malformed")
	}
	var userIDs, groupIDs processCredentialIdentity
	groups := make([]uint32, 0)
	foundUID, foundGID, foundGroups := false, false, false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4*1024), maxProcessStatusBytes)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Uid:"):
			if foundUID {
				return userIDs, groupIDs, nil, fmt.Errorf("runtime process status repeats Uid")
			}
			foundUID = true
			parsed, err := parseProcessCredentialTuple(strings.TrimPrefix(line, "Uid:"))
			if err != nil {
				return userIDs, groupIDs, nil, err
			}
			userIDs = parsed
		case strings.HasPrefix(line, "Gid:"):
			if foundGID {
				return userIDs, groupIDs, nil, fmt.Errorf("runtime process status repeats Gid")
			}
			foundGID = true
			parsed, err := parseProcessCredentialTuple(strings.TrimPrefix(line, "Gid:"))
			if err != nil {
				return userIDs, groupIDs, nil, err
			}
			groupIDs = parsed
		case strings.HasPrefix(line, "Groups:"):
			if foundGroups {
				return userIDs, groupIDs, nil, fmt.Errorf("runtime process status repeats Groups")
			}
			foundGroups = true
			fields := strings.Fields(strings.TrimPrefix(line, "Groups:"))
			if len(fields) > maxProcessSupplementaryGroups {
				return userIDs, groupIDs, nil, fmt.Errorf("runtime supplementary group count exceeds limit")
			}
			for _, field := range fields {
				groupID, parseErr := parseCanonicalProcessID(field)
				if parseErr != nil || groupID == 0 || groupID == math.MaxUint32 {
					return userIDs, groupIDs, nil, fmt.Errorf("runtime supplementary group is invalid")
				}
				groups = append(groups, groupID)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return userIDs, groupIDs, nil, fmt.Errorf("scan runtime process status: %w", err)
	}
	if !foundUID || !foundGID || !foundGroups {
		return userIDs, groupIDs, nil, fmt.Errorf("runtime process credentials are incomplete")
	}
	slices.Sort(groups)
	for index := 1; index < len(groups); index++ {
		if groups[index-1] == groups[index] {
			return userIDs, groupIDs, nil, fmt.Errorf("runtime supplementary groups contain duplicates")
		}
	}
	return userIDs, groupIDs, groups, nil
}

func parseProcessCredentialTuple(raw string) (processCredentialIdentity, error) {
	fields := strings.Fields(raw)
	if len(fields) != 4 {
		return processCredentialIdentity{}, fmt.Errorf("runtime process credential identity is incomplete")
	}
	values := [4]uint32{}
	for index, field := range fields {
		value, err := parseCanonicalProcessID(field)
		if err != nil {
			return processCredentialIdentity{}, fmt.Errorf("runtime process credential identity is noncanonical")
		}
		values[index] = value
	}
	return processCredentialIdentity{
		real: values[0], effective: values[1], saved: values[2], filesystem: values[3],
	}, nil
}

func parseCanonicalProcessID(raw string) (uint32, error) {
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("process identity is not canonical")
	}
	return uint32(value), nil
}

func parseCanonicalPositiveInt(raw string) (int, error) {
	value, err := strconv.ParseUint(raw, 10, 31)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("process PID is not canonical")
	}
	return int(value), nil
}

func parseProcessCgroup(raw []byte) (string, error) {
	// The cgroup-v2 ABI specifies exactly "0::$PATH" for the unified hierarchy. A deleted suffix or another
	// hierarchy is ambiguous authority and is rejected.
	// See https://docs.kernel.org/admin-guide/cgroup-v2.html#processes
	line, err := parseSingleProcessMetadataLine(raw, maxProcessCgroupBytes)
	if err != nil {
		return "", fmt.Errorf("runtime process cgroup must contain exactly one membership: %w", err)
	}
	if !strings.HasPrefix(line, "0::/") || strings.ContainsAny(line, " \t") {
		return "", fmt.Errorf("runtime process cgroup membership is invalid")
	}
	cgroupPath := strings.TrimPrefix(line, "0::")
	if path.Clean(cgroupPath) != cgroupPath || strings.Contains(cgroupPath, "//") {
		return "", fmt.Errorf("runtime process cgroup membership is invalid")
	}
	return infra.CgroupV2Root + cgroupPath, nil
}

func parseSingleProcessMetadataLine(raw []byte, limit int) (string, error) {
	if len(raw) == 0 || len(raw) > limit || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
		return "", fmt.Errorf("metadata is empty, oversized, or contains invalid bytes")
	}
	line := string(raw)
	if strings.HasSuffix(line, "\n") {
		line = strings.TrimSuffix(line, "\n")
	}
	if line == "" || strings.ContainsRune(line, '\n') || strings.TrimSpace(line) != line {
		return "", fmt.Errorf("metadata is not one canonical line")
	}
	return line, nil
}
