package loopmount

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	rootfsGuestGroupPath   = "/etc/group"
	rootfsGuestShadowPath  = "/etc/shadow"
	rootfsGuestGShadowPath = "/etc/gshadow"
	rootfsGuestFirstID     = 1000
	rootfsGuestLastID      = 60000
)

// ensureRootfsGuestAccount publishes the passwd row last: a crash can leave an unused
// group or locked shadow row, but cannot expose an account before its credentials exist.
// All writes are to the pinned guest filesystem; no guest account tool runs on the host.
func ensureRootfsGuestAccount(
	ctx context.Context,
	tree rootfsGuestTree,
	username string,
) (rootfsGuestAccount, uint32, error) {
	passwd, missing, err := tree.readOptional(ctx, rootfsPasswdPath, maxRootfsPasswdBytes)
	if err != nil || missing {
		return rootfsGuestAccount{}, 0, rootfsAtomicError("read managed guest account database", err)
	}
	rows, err := parseRootfsAccountRows(passwd, 7)
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	if _, exists := rows[username]; exists {
		account, err := parseRootfsGuestAccount(passwd, username)
		if err != nil {
			return rootfsGuestAccount{}, 0, err
		}
		err = ensureRootfsExistingHome(ctx, tree, account)
		return account, 0, err
	}
	if username == "root" {
		return rootfsGuestAccount{}, 0, rootfsAtomicError("managed guest root account is missing", nil)
	}
	group, missing, err := tree.readOptional(ctx, rootfsGuestGroupPath, maxRootfsPasswdBytes)
	if err != nil || missing {
		return rootfsGuestAccount{}, 0, rootfsAtomicError("read managed guest group database", err)
	}
	groups, err := parseRootfsAccountRows(group, 4)
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	uid, err := allocateRootfsGuestID(rows)
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	gid := uid
	if existing, exists := groups[username]; exists {
		gid, err = parseRootfsGuestID(existing[2])
		if err != nil || gid == 0 {
			return rootfsGuestAccount{}, 0, rootfsAtomicError("managed guest primary group is invalid", err)
		}
	} else {
		for _, fields := range groups {
			id, parseErr := parseRootfsGuestID(fields[2])
			if parseErr != nil {
				return rootfsGuestAccount{}, 0, parseErr
			}
			if id == gid {
				gid, err = allocateRootfsGuestID(groups)
				if err != nil {
					return rootfsGuestAccount{}, 0, err
				}
				break
			}
		}
		group = append(group, []byte(fmt.Sprintf("%s:x:%d:\n", username, gid))...)
	}
	shadow, _, err := tree.readOptional(ctx, rootfsGuestShadowPath, maxRootfsPasswdBytes)
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	shadow, err = appendRootfsAccountRow(shadow, username, 9,
		fmt.Sprintf("%s:!:%d:0:99999:7:::\n", username, time.Now().Unix()/86400))
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	gshadow, gshadowMissing, err := tree.readOptional(ctx, rootfsGuestGShadowPath, maxRootfsPasswdBytes)
	if err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	if !gshadowMissing {
		gshadow, err = appendRootfsAccountRow(gshadow, username, 4, username+":!::\n")
		if err != nil {
			return rootfsGuestAccount{}, 0, err
		}
	}
	account := rootfsGuestAccount{name: username, uid: uid, gid: gid, home: "home/" + username}
	passwd = append(passwd, []byte(fmt.Sprintf("%s:x:%d:%d::/%s:/bin/sh\n", username, uid, gid, account.home))...)
	for _, content := range [][]byte{passwd, group, shadow, gshadow} {
		if len(content) > maxRootfsPasswdBytes {
			return rootfsGuestAccount{}, 0, rootfsAtomicError("managed guest account database exceeds its bound", nil)
		}
	}
	if _, err := tree.ensureDirectory(ctx, "/home", 0755, 0, 0); err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	if _, err := tree.ensureDirectory(ctx, "/"+account.home, 0755, uid, gid); err != nil {
		return rootfsGuestAccount{}, 0, err
	}
	written, err := copyRootfsAccountSkeleton(ctx, tree, account)
	if err != nil {
		return rootfsGuestAccount{}, written, err
	}
	files := []rootfsProvisionFile{
		{path: rootfsGuestGroupPath, content: group, mode: 0644},
		{path: rootfsGuestShadowPath, content: shadow, mode: 0600},
	}
	if !gshadowMissing {
		files = append(files, rootfsProvisionFile{path: rootfsGuestGShadowPath, content: gshadow, mode: 0600})
	}
	files = append(files, rootfsProvisionFile{path: rootfsPasswdPath, content: passwd, mode: 0644})
	for _, file := range files {
		changed, err := tree.replaceFile(ctx, file)
		if changed {
			written++
		}
		if err != nil {
			return rootfsGuestAccount{}, written, err
		}
	}
	return account, written, nil
}

func ensureRootfsExistingHome(ctx context.Context, tree rootfsGuestTree, account rootfsGuestAccount) (returnErr error) {
	mode := uint32(0755)
	if account.name == "root" {
		mode = 0700
	}
	fd, err := tree.deps.openAt2(ctx, tree.rootFD, account.home, &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
	})
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return rootfsAtomicError("inspect existing guest account home", err)
	}
	if err == nil {
		defer func() {
			returnErr = appendRootfsCleanupError(returnErr, "close existing guest account home", tree.deps.close(context.WithoutCancel(ctx), fd))
		}()
		var stat unix.Stat_t
		if err := tree.deps.fstat(ctx, fd, &stat); err != nil {
			return rootfsAtomicError("read existing guest account home permissions", err)
		}
		// Ownership repair must not widen an existing account's home access.
		mode = stat.Mode & 0777
	}
	_, err = tree.ensureDirectory(ctx, "/"+account.home, mode, account.uid, account.gid)
	return err
}

func copyRootfsAccountSkeleton(
	ctx context.Context,
	tree rootfsGuestTree,
	account rootfsGuestAccount,
) (_ uint32, returnErr error) {
	fd, err := tree.deps.openAt2(ctx, tree.rootFD, "etc/skel", &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return 0, nil
	}
	if err != nil {
		return 0, rootfsAtomicError("open fixed guest account skeleton", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close guest account skeleton", tree.deps.close(context.WithoutCancel(ctx), fd))
	}()
	remaining := int(maxResultCount)
	return copyRootfsSkeletonDirectory(ctx, tree, account, fd, "/"+account.home, 0, &remaining)
}

func copyRootfsSkeletonDirectory(
	ctx context.Context,
	tree rootfsGuestTree,
	account rootfsGuestAccount,
	source int,
	destination string,
	depth int,
	remaining *int,
) (uint32, error) {
	if depth >= 64 {
		return 0, rootfsAtomicError("guest account skeleton nesting exceeds the bound", nil)
	}
	var written uint32
	buffer := make([]byte, 8192)
	for {
		if err := ctx.Err(); err != nil {
			return written, rootfsAtomicError("copy guest account skeleton context ended", err)
		}
		n, err := tree.deps.readDirent(ctx, source, buffer)
		if err != nil || n < 0 || n > len(buffer) {
			return written, rootfsAtomicError("read bounded guest account skeleton directory", err)
		}
		if n == 0 {
			return written, nil
		}
		consumed, _, names := unix.ParseDirent(buffer[:n], -1, nil)
		if consumed != n {
			return written, rootfsAtomicError("guest account skeleton directory entries are malformed", nil)
		}
		for _, name := range names {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || *remaining <= 0 {
				return written, rootfsAtomicError("guest account skeleton entry exceeds the closed traversal bound", nil)
			}
			*remaining = *remaining - 1
			count, err := copyRootfsSkeletonEntry(ctx, tree, account, source, name, destination+"/"+name, depth, remaining)
			written += count
			if err != nil {
				return written, err
			}
		}
	}
}

func copyRootfsSkeletonEntry(
	ctx context.Context,
	tree rootfsGuestTree,
	account rootfsGuestAccount,
	source int,
	name, destination string,
	depth int,
	remaining *int,
) (_ uint32, returnErr error) {
	var stat unix.Stat_t
	if err := tree.deps.fstatAt(ctx, source, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, rootfsAtomicError("inspect guest skeleton entry", err)
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		fd, err := tree.deps.openAt2(ctx, source, name, &unix.OpenHow{
			Flags: rootfsGuestDirectoryFlags, Resolve: rootfsGuestResolveFlags,
		})
		if err != nil {
			return 0, rootfsAtomicError("pin guest skeleton directory", err)
		}
		defer func() {
			returnErr = appendRootfsCleanupError(returnErr, "close guest skeleton directory", tree.deps.close(context.WithoutCancel(ctx), fd))
		}()
		if _, err := tree.ensureDirectory(ctx, destination, stat.Mode&0777, account.uid, account.gid); err != nil {
			return 0, err
		}
		return copyRootfsSkeletonDirectory(ctx, tree, account, fd, destination, depth+1, remaining)
	}
	parent, leaf, err := tree.openParent(ctx, destination)
	if err != nil {
		return 0, err
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close guest skeleton destination parent", tree.deps.close(context.WithoutCancel(ctx), parent))
	}()
	var existing unix.Stat_t
	if err := tree.deps.fstatAt(ctx, parent, leaf, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return 0, nil // Preserve pre-existing home content and completed leaves from an interrupted copy.
	} else if !errors.Is(err, unix.ENOENT) {
		return 0, rootfsAtomicError("inspect guest skeleton destination", err)
	}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		fd, err := tree.deps.openAt2(ctx, source, name, &unix.OpenHow{
			Flags: rootfsGuestReadFlags, Resolve: rootfsGuestResolveFlags,
		})
		if err != nil {
			return 0, rootfsAtomicError("pin guest skeleton file", err)
		}
		defer func() {
			returnErr = appendRootfsCleanupError(returnErr, "close guest skeleton file", tree.deps.close(context.WithoutCancel(ctx), fd))
		}()
		content, err := readStableRootfsGuestFile(ctx, tree.deps, fd, maxRootfsAgentBytes)
		if err != nil {
			return 0, err
		}
		changed, err := tree.replaceFile(ctx, rootfsProvisionFile{
			path: destination, content: content, mode: stat.Mode & 0777, uid: account.uid, gid: account.gid,
		})
		if changed {
			return 1, err
		}
		return 0, err
	case unix.S_IFLNK:
		buffer := make([]byte, maxRootfsGuestSymlinkTargetBytes)
		n, err := tree.deps.readlinkAt(ctx, source, name, buffer)
		if err != nil || n <= 0 || n >= len(buffer) {
			return 0, rootfsAtomicError("read bounded guest skeleton symlink", err)
		}
		// The target is copied as guest metadata and never followed by this privileged operation.
		if err := tree.deps.symlinkAt(ctx, string(buffer[:n]), parent, leaf); err != nil {
			return 0, rootfsAtomicError("copy guest skeleton symlink", err)
		}
		tree.tracker.markModified()
		if err := tree.deps.fsync(ctx, parent); err != nil {
			return 1, rootfsCleanupError("sync guest skeleton symlink", err)
		}
		return 1, nil
	default:
		return 0, rootfsAtomicError("guest account skeleton contains an unsupported special file", nil)
	}
}

func parseRootfsAccountRows(content []byte, fields int) (map[string][]string, error) {
	if len(content) > maxRootfsPasswdBytes || bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return nil, rootfsAtomicError("managed guest account database is malformed or unbounded", nil)
	}
	rows := make(map[string][]string)
	if len(content) == 0 {
		return rows, nil
	}
	lines, err := splitRootfsPasswdLines(content)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		row := strings.Split(line, ":")
		if len(row) != fields || row[0] == "" {
			return nil, rootfsAtomicError("managed guest account database row is malformed", nil)
		}
		if _, exists := rows[row[0]]; exists {
			return nil, rootfsAtomicError("managed guest account database name is duplicated", nil)
		}
		rows[row[0]] = row
	}
	return rows, nil
}

func parseRootfsGuestID(value string) (uint32, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || strconv.FormatUint(id, 10) != value || id == 1<<32-1 {
		return 0, rootfsAtomicError("managed guest numeric identity is invalid", err)
	}
	return uint32(id), nil
}

func allocateRootfsGuestID(rows map[string][]string) (uint32, error) {
	used := make(map[uint32]bool, len(rows))
	for _, fields := range rows {
		id, err := parseRootfsGuestID(fields[2])
		if err != nil {
			return 0, err
		}
		used[id] = true
	}
	for id := uint32(rootfsGuestFirstID); id <= rootfsGuestLastID; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, rootfsAtomicError("managed guest normal account identity range is exhausted", nil)
}

func appendRootfsAccountRow(content []byte, username string, fields int, row string) ([]byte, error) {
	rows, err := parseRootfsAccountRows(content, fields)
	if err != nil {
		return nil, err
	}
	if existing, exists := rows[username]; exists {
		// Only an unused locked row is an admissible interrupted account creation.
		if existing[1] != "!" && existing[1] != "!!" && existing[1] != "*" {
			return nil, rootfsAtomicError("managed guest account has an unexpected orphan credential row", nil)
		}
		return content, nil
	}
	return append(content, []byte(row)...), nil
}
