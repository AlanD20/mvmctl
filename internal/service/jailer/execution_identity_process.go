package jailer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// requireExecutionIdentityProcessAbsence detects credentials still held by an existing task,
// including threads with distinct credentials. It runs only for a newly allocated account;
// reopening an existing VM reservation must allow that VM's authenticated live process.
func requireExecutionIdentityProcessAbsence(ctx context.Context, identity vmExecutionIdentity) (returnErr error) {
	fd, err := unix.Open("/proc", instanceDirectoryFlags, 0)
	if err != nil {
		return instanceAtomicError("open process filesystem for execution identity admission", err)
	}
	root := os.NewFile(uintptr(fd), "execution-identity-process-root")
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return instanceAtomicError("execution identity process root is not procfs", err)
	}
	for {
		names, err := root.Readdirnames(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return instanceAtomicError("enumerate processes for execution identity admission", err)
		}
		for _, name := range names {
			if _, err := strconv.ParseUint(name, 10, 31); err != nil {
				continue
			}
			if err := requireExecutionIdentityTaskAbsence(ctx, fd, name, identity); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func requireExecutionIdentityTaskAbsence(
	ctx context.Context,
	rootFD int,
	pid string,
	identity vmExecutionIdentity,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	processFD, err := unix.Openat(rootFD, pid, instanceDirectoryFlags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open process for execution identity admission", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(processFD)) }()
	tasksFD, err := unix.Openat(processFD, "task", instanceDirectoryFlags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open process tasks for execution identity admission", err)
	}
	tasks := os.NewFile(uintptr(tasksFD), "execution-identity-task-directory")
	defer func() { returnErr = errors.Join(returnErr, tasks.Close()) }()
	for {
		names, err := tasks.Readdirnames(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return instanceAtomicError("enumerate process tasks for execution identity admission", err)
		}
		for _, name := range names {
			if _, err := strconv.ParseUint(name, 10, 31); err != nil {
				return instanceAtomicError("process task identity is malformed", err)
			}
			if err := requireExecutionIdentityThreadAbsence(ctx, tasksFD, name, identity); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func requireExecutionIdentityThreadAbsence(
	ctx context.Context,
	tasksFD int,
	tid string,
	identity vmExecutionIdentity,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	threadFD, err := unix.Openat(tasksFD, tid, instanceDirectoryFlags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open thread for execution identity admission", err)
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(threadFD)) }()
	fd, err := unix.Openat(threadFD, "status", instanceReadFlags, 0)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open thread credentials for execution identity admission", err)
	}
	file := os.NewFile(uintptr(fd), "execution-identity-thread-status")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	raw, err := io.ReadAll(io.LimitReader(file, maxProcessStatusBytes+1))
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil || len(raw) == 0 || len(raw) > maxProcessStatusBytes || bytes.IndexByte(raw, 0) >= 0 {
		return instanceAtomicError("read thread credentials for execution identity admission", err)
	}
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxProcessStatusBytes)
	for scanner.Scan() {
		field, value, found := strings.Cut(scanner.Text(), ":")
		if !found || (field != "Uid" && field != "Gid" && field != "Groups") {
			continue
		}
		if seen[field] {
			return instanceAtomicError("thread credentials contain duplicate fields", nil)
		}
		seen[field] = true
		values := strings.Fields(value)
		if field != "Groups" && len(values) != 4 {
			return instanceAtomicError("thread credential tuple is incomplete", nil)
		}
		for _, value := range values {
			id, err := parseCanonicalProcessID(value)
			if err != nil {
				return instanceAtomicError("thread credential is malformed", err)
			}
			if (field == "Uid" && id == identity.uid) || (field != "Uid" && id == identity.gid) {
				return instanceAtomicError("new execution identity is still held by an existing host task", nil)
			}
		}
	}
	if err := scanner.Err(); err != nil || len(seen) != 3 {
		return instanceAtomicError("thread credential admission is incomplete", err)
	}
	return nil
}
