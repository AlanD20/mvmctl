package loopmount

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

const imageConversionWorkspace = ".mvmctl-conversion"

type imageConversionPhase string

const (
	imageConversionPrepared imageConversionPhase = "prepared"
	imageConversionSnapshot imageConversionPhase = "snapshot"
	imageConversionReady    imageConversionPhase = "ready"
	imageConversionComplete imageConversionPhase = "complete"
)

type imageConversionIdentity struct {
	Device uint64        `json:"device"`
	Inode  uint64        `json:"inode"`
	Size   int64         `json:"size"`
	Mtime  unix.Timespec `json:"mtime"`
	Ctime  unix.Timespec `json:"ctime"`
}

type imageConversionRecord struct {
	Schema       uint32                  `json:"schema"`
	UID          uint32                  `json:"uid"`
	Request      ImageOptimizeRequest    `json:"request"`
	Phase        imageConversionPhase    `json:"phase"`
	CacheMajor   uint32                  `json:"cache_major"`
	CacheMinor   uint32                  `json:"cache_minor"`
	CacheInode   uint64                  `json:"cache_inode"`
	Directory    imageConversionIdentity `json:"directory"`
	Workspace    imageConversionIdentity `json:"workspace"`
	Original     imageConversionIdentity `json:"original"`
	Snapshot     imageConversionIdentity `json:"snapshot"`
	Candidate    imageConversionIdentity `json:"candidate"`
	SourceDigest string                  `json:"source_digest"`
	ResultDigest string                  `json:"result_digest"`
}

// CRITICAL: Names are compiled constants beneath the pinned staging directory. Neither the
// record nor the request can nominate a host path. A ready record authorizes only its exact
// candidate inode; an interrupted exchange retains the displaced original for reconciliation.
type imageConversionTransaction struct {
	directoryFD int
	workspaceFD int
	snapshotFD  int
	candidateFD int
	leaf        string
	request     ImageOptimizeRequest
	caller      Caller
	record      *imageConversionRecord
}

func openImageConversionTransaction(ctx context.Context, caller Caller, request ImageOptimizeRequest) (
	_ *imageConversionTransaction, returnErr error,
) {
	fd, err := openImageTransactionDirectory(ctx, caller)
	if err != nil {
		return nil, err
	}
	transaction := &imageConversionTransaction{
		directoryFD: fd, workspaceFD: -1, snapshotFD: -1, candidateFD: -1,
		leaf: request.ImageID + ".convert.json", request: request, caller: caller,
	}
	if err := transaction.load(ctx); err != nil {
		return nil, appendRootfsCleanupError(err, "close rejected conversion transaction", transaction.close())
	}
	return transaction, nil
}

func (transaction *imageConversionTransaction) load(ctx context.Context) (returnErr error) {
	fd, err := unix.Openat2(transaction.directoryFD, transaction.leaf, &unix.OpenHow{
		Flags: rootfsReadOnlyOpenFlags, Resolve: rootfsDescendantResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return rootfsAtomicError("open image conversion receipt", err)
	}
	defer func() { returnErr = appendRootfsCleanupError(returnErr, "close conversion receipt", unix.Close(fd)) }()
	before, err := inspectImageConversionFile(ctx, fd, 0, 0, maxImageTransactionBytes)
	if err != nil {
		return err
	}
	data := make([]byte, int(before.Size))
	if n, err := unix.Pread(fd, data, 0); err != nil || n != len(data) {
		return rootfsAtomicError("read complete conversion receipt", errors.Join(err, io.ErrUnexpectedEOF))
	}
	var record imageConversionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return rootfsAtomicError("decode image conversion receipt", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(data, canonical) {
		return rootfsAtomicError("image conversion receipt is not canonical", err)
	}
	after, err := inspectImageConversionFile(ctx, fd, 0, 0, maxImageTransactionBytes)
	if err != nil || after != before {
		return rootfsAtomicError("image conversion receipt changed during admission", err)
	}
	wanted, err := json.Marshal(transaction.request)
	if err != nil {
		return rootfsAtomicError("encode conversion request identity", err)
	}
	recorded, err := json.Marshal(record.Request)
	if err != nil || !bytes.Equal(wanted, recorded) || record.Schema != 1 || record.UID != transaction.caller.UID ||
		record.CacheInode == 0 || record.Directory.Inode == 0 || record.Workspace.Inode == 0 ||
		record.Original.Inode == 0 || record.Snapshot.Inode == 0 || record.Candidate.Inode == 0 ||
		record.Original.Size != int64(transaction.request.ExpectedImageBytes) {
		return rootfsAtomicError("conversion replay does not match durable intent", err)
	}
	switch record.Phase {
	case imageConversionPrepared:
		if record.SourceDigest != "" || record.ResultDigest != "" {
			return rootfsAtomicError("prepared conversion receipt contains a result", nil)
		}
	case imageConversionSnapshot, imageConversionReady, imageConversionComplete:
		if !validLowerHexID(record.SourceDigest, sha256.Size*2) || record.Snapshot.Size != record.Original.Size {
			return rootfsAtomicError("conversion snapshot receipt is invalid", nil)
		}
		if record.Phase != imageConversionSnapshot && (!validLowerHexID(record.ResultDigest, sha256.Size*2) ||
			record.Candidate.Size < int64(minimumImageBytes) || uint64(record.Candidate.Size) > maximumImageBytes) {
			return rootfsAtomicError("conversion candidate receipt is invalid", nil)
		}
	default:
		return rootfsAtomicError("conversion receipt phase is unknown", nil)
	}
	transaction.record = &record
	return nil
}

func (transaction *imageConversionTransaction) persist(ctx context.Context) error {
	data, err := json.Marshal(transaction.record)
	if err != nil {
		return rootfsAtomicError("encode image conversion receipt", err)
	}
	return persistImageTransaction(ctx, transaction.directoryFD, transaction.leaf, data)
}

func (transaction *imageConversionTransaction) bind(ctx context.Context, lease *rootfsLease) error {
	var directory unix.Stat_t
	if err := unix.Fstat(lease.stagingFD, &directory); err != nil {
		return rootfsAtomicError("inspect conversion staging directory", err)
	}
	if transaction.record != nil {
		record := transaction.record
		if record.Directory.Device != uint64(directory.Dev) || record.Directory.Inode != directory.Ino ||
			record.CacheMajor != lease.cacheIdentity.deviceMajor || record.CacheMinor != lease.cacheIdentity.deviceMinor ||
			record.CacheInode != lease.cacheIdentity.inode {
			return rootfsAtomicError("conversion replay staging identity changed", nil)
		}
	} else {
		original, err := inspectImageConversionFile(ctx, lease.rootfsFD, transaction.caller.UID, transaction.caller.GID, maximumImageBytes)
		if err != nil {
			return err
		}
		// An existing workspace without its receipt is deliberately not adopted or deleted.
		if err := unix.Mkdirat(lease.stagingFD, imageConversionWorkspace, 0700); err != nil {
			return rootfsAtomicError("create exclusive conversion workspace; any previous workspace is retained", err)
		}
		transaction.record = &imageConversionRecord{
			Schema: 1, UID: transaction.caller.UID, Request: transaction.request, Phase: imageConversionPrepared,
			CacheMajor: lease.cacheIdentity.deviceMajor, CacheMinor: lease.cacheIdentity.deviceMinor,
			CacheInode: lease.cacheIdentity.inode, Directory: imageConversionStatIdentity(directory), Original: original,
		}
	}
	fd, err := unix.Openat2(lease.stagingFD, imageConversionWorkspace, &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: rootfsDescendantResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) && transaction.record.Phase == imageConversionComplete {
		return nil
	}
	if err != nil {
		return rootfsAtomicError("open retained conversion workspace", err)
	}
	transaction.workspaceFD = fd
	var workspace unix.Stat_t
	if err := unix.Fstat(fd, &workspace); err != nil {
		return rootfsAtomicError("inspect conversion workspace", err)
	}
	if workspace.Mode != unix.S_IFDIR|0700 || workspace.Uid != 0 || workspace.Gid != 0 ||
		uint64(workspace.Dev) != transaction.record.Directory.Device ||
		transaction.record.Workspace.Inode != 0 && !sameImageConversionInode(transaction.record.Workspace, imageConversionStatIdentity(workspace)) {
		return rootfsAtomicError("conversion workspace authority changed", nil)
	}
	if transaction.record.Workspace.Inode == 0 {
		transaction.record.Workspace = imageConversionStatIdentity(workspace)
		for _, item := range []struct {
			name     string
			fd       *int
			identity *imageConversionIdentity
		}{{"source.img", &transaction.snapshotFD, &transaction.record.Snapshot},
			{"candidate.img", &transaction.candidateFD, &transaction.record.Candidate}} {
			created, err := unix.Openat(fd, item.name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
			if err != nil {
				return rootfsAtomicError("create exclusive conversion backing inode", err)
			}
			*item.fd = created
			var stat unix.Stat_t
			if err := unix.Fstat(created, &stat); err != nil {
				return rootfsAtomicError("inspect new conversion backing inode", err)
			}
			*item.identity = imageConversionStatIdentity(stat)
			if err := unix.Fsync(created); err != nil {
				return rootfsCleanupError("sync new conversion backing inode", err)
			}
		}
		if err := unix.Fsync(fd); err != nil {
			return rootfsCleanupError("sync conversion workspace", err)
		}
		if err := syncImageConversionDirectory(ctx, lease.stagingFD); err != nil {
			return err
		}
		return transaction.persist(ctx)
	}
	return nil
}

func imageConversionStatIdentity(stat unix.Stat_t) imageConversionIdentity {
	return imageConversionIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size, Mtime: stat.Mtim, Ctime: stat.Ctim}
}

func sameImageConversionInode(first, second imageConversionIdentity) bool {
	return first.Device == second.Device && first.Inode == second.Inode
}

func inspectImageConversionFile(ctx context.Context, fd int, uid, gid uint32, limit uint64) (imageConversionIdentity, error) {
	if err := ctx.Err(); err != nil {
		return imageConversionIdentity{}, rootfsAtomicError("image conversion context ended", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return imageConversionIdentity{}, rootfsAtomicError("inspect retained conversion inode", err)
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != uid || stat.Gid != gid || stat.Nlink != 1 ||
		stat.Size < 0 || uint64(stat.Size) > limit {
		return imageConversionIdentity{}, rootfsAtomicError("conversion inode metadata is outside authority", nil)
	}
	return imageConversionStatIdentity(stat), nil
}

func imageConversionDigest(ctx context.Context, fd int, size int64) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return "", rootfsAtomicError("conversion digest context ended", err)
		}
		part := buffer[:min(int64(len(buffer)), size-offset)]
		n, err := unix.Pread(fd, part, offset)
		if err != nil || n != len(part) {
			return "", rootfsAtomicError("read complete conversion digest chunk", errors.Join(err, io.ErrUnexpectedEOF))
		}
		_, _ = hash.Write(part)
		offset += int64(n)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (transaction *imageConversionTransaction) close() error {
	var err error
	for _, fd := range []*int{&transaction.candidateFD, &transaction.snapshotFD, &transaction.workspaceFD, &transaction.directoryFD} {
		if *fd >= 0 {
			err = appendRootfsCleanupError(err, "close retained conversion descriptor", unix.Close(*fd))
			*fd = -1
		}
	}
	return err
}
