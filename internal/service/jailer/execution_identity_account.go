package jailer

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

const executionIdentityAccountFileLimit = 4 * 1024 * 1024

func requireExecutionIdentityHostTools(ctx context.Context) error {
	// Fixed host dependencies are required before durable allocation intent. Missing shadow-utils
	// or nologin must not leave a permanently pending reservation when no account mutation began.
	for _, name := range []string{"/usr/sbin/useradd", "/usr/bin/getent", "/usr/sbin/nologin"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Stat(name, &stat); err != nil {
			return instanceAtomicError("required host execution-account tool is unavailable: "+name, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Mode&0022 != 0 || stat.Mode&0111 == 0 {
			return instanceAtomicError("host execution-account tool is not a root-owned executable: "+name, nil)
		}
	}
	return nil
}

func requireExecutionIdentityNameAbsent(ctx context.Context, name string) error {
	for _, database := range []string{"passwd", "group"} {
		_, found, err := lookupExecutionIdentityAccount(ctx, database, name)
		if err != nil {
			return err
		}
		if found {
			return instanceAtomicError("refusing to adopt an existing execution account or group", nil)
		}
		raw, err := readExecutionIdentityAccountFile(ctx, database, false)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, name+":") {
				return instanceAtomicError("execution account name already exists in the local account database", nil)
			}
		}
	}
	return nil
}

// createExecutionIdentityAccount is the only account mutation in this module. The username
// and comment derive from root's durable pending reservation; no caller-selected argv is used.
// CRITICAL: useradd owns the host's serialized UID/GID allocation and collision policy. Its
// finite system-ID exhaustion and every ambiguous failure preserve the pending reservation.
func createExecutionIdentityAccount(
	ctx context.Context, record executionIdentityRecord, caller instanceCaller,
) (vmExecutionIdentity, error) {
	if record.Phase != executionIdentityPending || record.OwnerUID != caller.uid {
		return vmExecutionIdentity{}, instanceStateError(
			record.VMID,
			"execution account creation lacks pending authority",
		)
	}
	if err := validateExecutionIdentityRecord(record); err != nil {
		return vmExecutionIdentity{}, err
	}
	result, err := system.DefaultRunner.Run(ctx, []string{
		"/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--no-log-init",
		"--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--password", "!",
		"--comment", record.comment(), "--key", "CREATE_MAIL_SPOOL=no", "--", record.Name,
	}, executionIdentityCommandOptions())
	if err != nil || result == nil || !result.Success() {
		return vmExecutionIdentity{}, instanceAtomicError(
			"create isolated VM execution account; allocation intent retained",
			err,
		)
	}
	row, found, err := lookupExecutionIdentityAccount(ctx, "passwd", record.Name)
	if err != nil || !found || len(row) != 7 {
		return vmExecutionIdentity{}, instanceAtomicError(
			"observe created execution account; allocation intent retained",
			err,
		)
	}
	uid, uidErr := parseCanonicalProcessID(row[2])
	gid, gidErr := parseCanonicalProcessID(row[3])
	if err := errors.Join(uidErr, gidErr); err != nil {
		return vmExecutionIdentity{}, instanceAtomicError("created execution account credentials are invalid", err)
	}
	record.Phase, record.UID, record.GID = executionIdentityReady, &uid, &gid
	if err := requireExecutionIdentityAccount(ctx, record, caller); err != nil {
		return vmExecutionIdentity{}, err
	}
	if err := requireExecutionIdentityProcessAbsence(ctx, vmExecutionIdentity{uid: uid, gid: gid}); err != nil {
		return vmExecutionIdentity{}, err
	}
	return vmExecutionIdentity{uid: uid, gid: gid}, nil
}

func executionIdentityCommandOptions() system.RunCmdOpts {
	return system.RunCmdOpts{
		Capture: true, Timeout: 30 * time.Second, MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
		Cwd: "/", ReplaceEnv: true,
		Env: map[string]string{"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "HOME": "/root", "LANG": "C", "LC_ALL": "C"},
	}
}

func lookupExecutionIdentityAccount(ctx context.Context, database, key string) ([]string, bool, error) {
	if database != "passwd" && database != "group" {
		return nil, false, instanceValidationError("execution identity account database is invalid")
	}
	result, err := system.DefaultRunner.Run(ctx,
		[]string{"/usr/bin/getent", database, key}, executionIdentityCommandOptions())
	if err != nil || result == nil {
		return nil, false, instanceAtomicError("resolve execution account through host NSS", err)
	}
	if result.ExitCode == 2 && result.Stdout == "" && result.Stderr == "" {
		return nil, false, nil
	}
	if !result.Success() || result.Stderr != "" || !strings.HasSuffix(result.Stdout, "\n") {
		return nil, false, instanceAtomicError("execution account NSS result is unavailable", nil)
	}
	line := strings.TrimSuffix(result.Stdout, "\n")
	if strings.ContainsAny(line, "\r\n\x00") {
		return nil, false, instanceAtomicError("execution account NSS result is malformed", nil)
	}
	row := strings.Split(line, ":")
	fields := 7
	if database == "group" {
		fields = 4
	}
	if len(row) != fields {
		return nil, false, instanceAtomicError("execution account NSS field count is invalid", nil)
	}
	return row, true, nil
}

func requireExecutionIdentityAccount(ctx context.Context, record executionIdentityRecord, caller instanceCaller) error {
	if err := validateExecutionIdentityRecord(record); err != nil {
		return err
	}
	if record.Phase != executionIdentityReady || *record.UID == caller.uid || *record.GID == caller.gid {
		return instanceStateError(record.VMID, "execution credentials overlap operator credentials")
	}
	return requireExecutionIdentityAccountRecord(ctx, record)
}

func requireExecutionIdentityAccountRecord(ctx context.Context, record executionIdentityRecord) error {
	if err := validateExecutionIdentityRecord(record); err != nil {
		return err
	}
	if record.Phase != executionIdentityReady && record.Phase != executionIdentityRetiring {
		return instanceStateError(record.VMID, "execution account reservation is not allocated")
	}
	for _, query := range []struct{ database, key string }{
		{"passwd", record.Name}, {"passwd", strconv.FormatUint(uint64(*record.UID), 10)},
		{"group", record.Name}, {"group", strconv.FormatUint(uint64(*record.GID), 10)},
	} {
		row, found, err := lookupExecutionIdentityAccount(ctx, query.database, query.key)
		if err != nil || !found {
			return instanceAtomicError("execution account reservation no longer resolves through NSS", err)
		}
		if err := requireExecutionIdentityAccountRow(record, query.database, row); err != nil {
			return err
		}
	}
	for _, database := range []string{"passwd", "group", "shadow"} {
		raw, err := readExecutionIdentityAccountFile(ctx, database, false)
		if err != nil {
			return err
		}
		matches := 0
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" {
				continue
			}
			row := strings.Split(line, ":")
			if row[0] == record.Name {
				matches++
				if err := requireExecutionIdentityAccountRow(record, database, row); err != nil {
					return err
				}
				continue
			}
			if database == "passwd" && len(row) == 7 {
				if row[2] == strconv.FormatUint(uint64(*record.UID), 10) ||
					row[3] == strconv.FormatUint(uint64(*record.GID), 10) {
					return instanceAtomicError("execution credentials overlap another local account", nil)
				}
			} else if database == "group" && len(row) == 4 {
				if row[2] == strconv.FormatUint(uint64(*record.GID), 10) {
					return instanceAtomicError("execution group overlaps another local group", nil)
				}
				for _, member := range strings.Split(row[3], ",") {
					if member == record.Name {
						return instanceAtomicError("execution account has supplemental group membership", nil)
					}
				}
			}
		}
		if matches != 1 {
			return instanceAtomicError("execution account local identity is missing or duplicated", nil)
		}
	}
	return requireExecutionIdentitySubordinateExclusion(ctx, *record.UID, *record.GID)
}

func requireExecutionIdentityAccountRow(record executionIdentityRecord, database string, row []string) error {
	valid := len(row) > 0 && row[0] == record.Name
	switch database {
	case "passwd":
		valid = valid && len(row) == 7 && row[1] == "x" &&
			row[2] == strconv.FormatUint(
				uint64(*record.UID),
				10,
			) && row[3] == strconv.FormatUint(uint64(*record.GID), 10) &&
			row[4] == record.comment() && row[5] == "/nonexistent" && row[6] == "/usr/sbin/nologin"
	case "group":
		valid = valid && len(row) == 4 && row[2] == strconv.FormatUint(uint64(*record.GID), 10) && row[3] == ""
	case "shadow":
		valid = valid && len(row) == 9 && row[1] == "!"
	default:
		valid = false
	}
	if !valid {
		return instanceAtomicError("execution account does not match its locked root-owned reservation", nil)
	}
	return nil
}

func readExecutionIdentityAccountFile(ctx context.Context, name string, optional bool) (_ []byte, returnErr error) {
	switch name {
	case "passwd", "group", "shadow", "subuid", "subgid":
	default:
		return nil, instanceValidationError("execution identity account file is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := unix.Open("/", instanceDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(root)) }()
	etc, err := openInstanceDirectory(ctx, realInstanceAuthorityDeps(), root, "etc", false,
		instanceAncestorDirectory, productionInstanceAuthorityPolicy())
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(etc)) }()
	fd, err := unix.Openat(etc, name, instanceReadFlags|unix.O_NONBLOCK, 0)
	if optional && errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, instanceAtomicError("open host execution-account database", err)
	}
	file := os.NewFile(uintptr(fd), "execution-account-database")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 || before.Mode&0022 != 0 || before.Nlink != 1 ||
		before.Size < 0 || before.Size > executionIdentityAccountFileLimit {
		return nil, instanceAtomicError("host execution-account database metadata is unsafe", nil)
	}
	raw, err := io.ReadAll(io.LimitReader(file, executionIdentityAccountFileLimit+1))
	if err != nil || len(raw) > executionIdentityAccountFileLimit {
		return nil, instanceAtomicError("read bounded host execution-account database", err)
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid ||
		before.Gid != after.Gid || before.Nlink != after.Nlink || before.Size != after.Size ||
		before.Mtim != after.Mtim || before.Ctim != after.Ctim || strings.ContainsRune(string(raw), 0) {
		return nil, instanceAtomicError("host execution-account database changed during admission", nil)
	}
	return raw, nil
}

func requireExecutionIdentitySubordinateExclusion(ctx context.Context, uid, gid uint32) error {
	for _, database := range []struct {
		name string
		id   uint32
	}{{"subuid", uid}, {"subgid", gid}} {
		raw, err := readExecutionIdentityAccountFile(ctx, database.name, true)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Split(line, ":")
			if len(fields) != 3 || fields[0] == "" {
				return instanceAtomicError("subordinate identity database is malformed", nil)
			}
			start, startErr := strconv.ParseUint(fields[1], 10, 32)
			count, countErr := strconv.ParseUint(fields[2], 10, 32)
			if startErr != nil || countErr != nil || count == 0 || start+count > 1<<32 {
				return instanceAtomicError("subordinate identity range is invalid", errors.Join(startErr, countErr))
			}
			if uint64(database.id) >= start && uint64(database.id) < start+count {
				return instanceAtomicError("execution credentials overlap a subordinate identity allocation", nil)
			}
		}
	}
	return nil
}
