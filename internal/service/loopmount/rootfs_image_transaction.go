package loopmount

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/sys/unix"
)

const maxImageTransactionBytes = 16 << 10

type imageShrinkPhase string

const (
	imageShrinkPrepared imageShrinkPhase = "prepared"
	imageShrinkSized    imageShrinkPhase = "sized"
	imageShrinkComplete imageShrinkPhase = "complete"
)

type imageShrinkRecord struct {
	Schema      uint32               `json:"schema"`
	UID         uint32               `json:"uid"`
	Request     ImageOptimizeRequest `json:"request"`
	CacheMajor  uint32               `json:"cache_major"`
	CacheMinor  uint32               `json:"cache_minor"`
	CacheInode  uint64               `json:"cache_inode"`
	DeviceMajor uint32               `json:"device_major"`
	DeviceMinor uint32               `json:"device_minor"`
	Inode       uint64               `json:"inode"`
	Phase       imageShrinkPhase     `json:"phase"`
	TargetBytes uint64               `json:"target_bytes"`
}

// imageShrinkTransaction retains root-owned durable authority while the caller's image lock is held.
// The record never supplies a path to an effect; admission still walks the fixed resource namespace.
type imageShrinkTransaction struct {
	directoryFD int
	leaf        string
	record      *imageShrinkRecord
	caller      Caller
	request     ImageOptimizeRequest
}

func openImageShrinkTransaction(
	ctx context.Context,
	caller Caller,
	request ImageOptimizeRequest,
) (_ *imageShrinkTransaction, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("image shrink transaction context ended", err)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, rootfsAtomicError("open image transaction filesystem root", err)
	}
	defer func() {
		if fd >= 0 {
			returnErr = appendRootfsCleanupError(returnErr, "close image transaction hierarchy", unix.Close(fd))
		}
	}()
	for index, component := range []string{"var", "lib", "mvmctl", "loopmount", strconv.FormatUint(uint64(caller.UID), 10)} {
		if index >= 2 {
			if err := unix.Mkdirat(fd, component, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, rootfsAtomicError("create fixed image transaction directory", err)
			}
			if err := unix.Fsync(fd); err != nil {
				return nil, rootfsCleanupError("sync image transaction directory parent", err)
			}
		}
		next, err := unix.Openat2(fd, component, &unix.OpenHow{
			Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if err != nil {
			return nil, rootfsAtomicError("open fixed image transaction directory", err)
		}
		previous := fd
		fd = next
		if err := unix.Close(previous); err != nil {
			return nil, rootfsCleanupError("close image transaction ancestor", err)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, rootfsAtomicError("inspect image transaction directory", err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 ||
			stat.Mode&0022 != 0 || stat.Mode&07000 != 0 || index >= 3 && stat.Mode&0777 != 0700 {
			return nil, rootfsAtomicError("image transaction directory has unsafe ownership or mode", nil)
		}
	}
	transaction := &imageShrinkTransaction{
		directoryFD: fd, leaf: request.ImageID + ".json", caller: caller, request: request,
	}
	if err := transaction.load(ctx); err != nil {
		return nil, err
	}
	fd = -1
	return transaction, nil
}

func (transaction *imageShrinkTransaction) load(ctx context.Context) (returnErr error) {
	fd, err := unix.Openat2(transaction.directoryFD, transaction.leaf, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: rootfsDescendantResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return rootfsAtomicError("open durable image shrink record", err)
	}
	defer func() { returnErr = appendRootfsCleanupError(returnErr, "close image shrink record", unix.Close(fd)) }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return rootfsAtomicError("inspect durable image shrink record", err)
	}
	if before.Mode != unix.S_IFREG|0600 || before.Uid != 0 || before.Gid != 0 || before.Nlink != 1 ||
		before.Size <= 0 || before.Size > maxImageTransactionBytes {
		return rootfsAtomicError("durable image shrink record has unsafe metadata", nil)
	}
	data := make([]byte, int(before.Size))
	n, err := unix.Pread(fd, data, 0)
	if err != nil || n != len(data) {
		return rootfsAtomicError("read complete durable image shrink record", errors.Join(err, io.ErrUnexpectedEOF))
	}
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("image shrink record read context ended", err)
	}
	var record imageShrinkRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return rootfsAtomicError("decode durable image shrink record", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(data, canonical) {
		return rootfsAtomicError("durable image shrink record is not canonical", err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino ||
		before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid ||
		before.Nlink != after.Nlink || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return rootfsAtomicError("durable image shrink record changed during admission", err)
	}
	request, err := json.Marshal(transaction.request)
	if err != nil {
		return rootfsAtomicError("encode image shrink request identity", err)
	}
	recordedRequest, err := json.Marshal(record.Request)
	if err != nil || !bytes.Equal(request, recordedRequest) || record.Schema != 1 ||
		record.UID != transaction.caller.UID || record.Inode == 0 || record.CacheInode == 0 {
		return rootfsAtomicError("image shrink replay does not match durable intent", err)
	}
	switch record.Phase {
	case imageShrinkPrepared:
		if record.TargetBytes != 0 {
			return rootfsAtomicError("prepared image shrink record has an unexpected target", nil)
		}
	case imageShrinkSized, imageShrinkComplete:
		if record.TargetBytes < minimumImageBytes || record.TargetBytes > record.Request.ExpectedImageBytes {
			return rootfsAtomicError("durable image shrink target exceeds admitted bounds", nil)
		}
	default:
		return rootfsAtomicError("durable image shrink phase is unknown", nil)
	}
	transaction.record = &record
	return nil
}

func (transaction *imageShrinkTransaction) bind(ctx context.Context, lease *rootfsLease) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	metadata, cache := lease.metadata, lease.cacheIdentity
	if transaction.record != nil {
		record := transaction.record
		if record.DeviceMajor != metadata.deviceMajor || record.DeviceMinor != metadata.deviceMinor ||
			record.Inode != metadata.inode || record.CacheMajor != cache.deviceMajor ||
			record.CacheMinor != cache.deviceMinor || record.CacheInode != cache.inode {
			return rootfsAtomicError("image shrink replay resource identity changed", nil)
		}
	} else {
		transaction.record = &imageShrinkRecord{
			Schema: 1, UID: transaction.caller.UID, Request: transaction.request,
			CacheMajor: cache.deviceMajor, CacheMinor: cache.deviceMinor, CacheInode: cache.inode,
			DeviceMajor: metadata.deviceMajor, DeviceMinor: metadata.deviceMinor, Inode: metadata.inode,
			Phase: imageShrinkPrepared,
		}
		if err := transaction.persist(ctx); err != nil {
			return err
		}
	}
	lease.imageShrink = transaction
	return nil
}

func (transaction *imageShrinkTransaction) persist(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("image shrink journal context ended", err)
	}
	data, err := json.Marshal(transaction.record)
	if err != nil || len(data) > maxImageTransactionBytes {
		return rootfsAtomicError("encode bounded image shrink record", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return rootfsAtomicError("allocate image shrink journal temporary name", err)
	}
	name := ".image-shrink-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(transaction.directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return rootfsAtomicError("create image shrink journal temporary file", err)
	}
	renamed := false
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close image shrink journal temporary file", unix.Close(fd))
		if !renamed {
			returnErr = appendRootfsCleanupError(returnErr, "remove unpublished image shrink journal", unix.Unlinkat(transaction.directoryFD, name, 0))
		}
	}()
	if err := unix.Fchmod(fd, 0600); err != nil {
		return rootfsAtomicError("set image shrink journal mode", err)
	}
	if n, err := unix.Write(fd, data); err != nil || n != len(data) {
		return rootfsAtomicError("write complete image shrink record", errors.Join(err, io.ErrShortWrite))
	}
	if err := unix.Fsync(fd); err != nil {
		return rootfsCleanupError("sync image shrink record", err)
	}
	if err := unix.Renameat(transaction.directoryFD, name, transaction.directoryFD, transaction.leaf); err != nil {
		return rootfsAtomicError("publish image shrink record", err)
	}
	renamed = true
	if err := unix.Fsync(transaction.directoryFD); err != nil {
		return rootfsCleanupError("sync image shrink journal publication", err)
	}
	return nil
}

func (transaction *imageShrinkTransaction) close() error {
	if transaction == nil || transaction.directoryFD < 0 {
		return nil
	}
	fd := transaction.directoryFD
	transaction.directoryFD = -1
	if err := unix.Close(fd); err != nil {
		return rootfsCleanupError(fmt.Sprintf("close image shrink journal for %s", transaction.request.ImageID), err)
	}
	return nil
}
