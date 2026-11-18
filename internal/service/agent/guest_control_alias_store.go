package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"mvmctl/internal/lib/guestcontrolwire"
)

const guestControlAliasRecordLimit = 4096
const guestControlAliasDirectory = "control-aliases"

type guestControlAliasReceipt struct {
	Lifetime   guestcontrolwire.Lifetime `json:"lifetime"`
	Schema     uint32                    `json:"schema"`
	Resource   string                    `json:"resource"`
	Generation uint64                    `json:"generation"`
	Revision   uint64                    `json:"revision"`
	Session    string                    `json:"session"`
	BootID     string                    `json:"boot_id"`
	Deadline   uint64                    `json:"deadline_boottime_ns"`
	Link       guestControlAliasLink     `json:"link"`
	Primary    guestControlAliasAddress  `json:"primary"`
	Address    guestControlAliasAddress  `json:"address"`
	Installed  bool                      `json:"installed"`
}

func validGuestControlAliasReceipt(record guestControlAliasReceipt) bool {
	resource, err := hex.DecodeString(record.Resource)
	session, sessionErr := uuid.Parse(record.Session)
	boot, bootErr := uuid.Parse(record.BootID)
	return record.Schema == 2 && err == nil && len(resource) == 16 && hex.EncodeToString(resource) == record.Resource &&
		sessionErr == nil && session.String() == record.Session && bootErr == nil && boot.String() == record.BootID &&
		len(record.Link.Name) > 0 && len(record.Link.Name) <= 6 && record.Primary.Prefix <= 32 &&
		record.Generation > 0 && record.Revision > 0 &&
		(record.Lifetime == guestcontrolwire.Finite && record.Deadline > 0 || record.Lifetime == guestcontrolwire.Persistent && record.Deadline == 0) && record.Link.Index > 0 &&
		record.Link.MAC != ([6]byte{}) &&
		record.Address.Index == record.Link.Index && record.Address.IP.Is4() && record.Address.IP.IsPrivate() &&
		record.Address.Prefix == 32 && record.Address.Scope == unix.RT_SCOPE_UNIVERSE &&
		strings.HasPrefix(record.Address.Label, record.Link.Name+":") &&
		len(record.Address.Label) > len(record.Link.Name)+1 && len(record.Address.Label) <= 15 &&
		record.Primary.Index == record.Link.Index && record.Primary.IP.Is4() && record.Primary.IP != record.Address.IP
}
func encodeGuestControlAliasReceipt(record guestControlAliasReceipt) ([]byte, error) {
	if !validGuestControlAliasReceipt(record) {
		return nil, errors.New("invalid guest alias receipt")
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > guestControlAliasRecordLimit {
		return nil, errors.New("invalid bounded guest alias receipt")
	}
	return raw, nil
}
func decodeGuestControlAliasReceipt(raw []byte) (guestControlAliasReceipt, error) {
	var record guestControlAliasReceipt
	if len(raw) == 0 || len(raw) > guestControlAliasRecordLimit {
		return record, errors.New("invalid guest alias receipt size")
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, errors.New("invalid guest alias receipt encoding")
	}
	canonical, err := encodeGuestControlAliasReceipt(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return guestControlAliasReceipt{}, errors.New("guest alias receipt is not canonical")
	}
	return record, nil
}

type guestControlAliasStore struct{ directory int }

func openGuestControlAliasStore() (*guestControlAliasStore, error) {
	directory, err := unix.Openat2(unix.AT_FDCWD, "/run/mvm-agent", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	defer unix.Close(directory)
	return openGuestControlAliasStoreAt(directory)
}
func openGuestControlAliasStoreAt(parent int) (*guestControlAliasStore, error) {
	var info unix.Stat_t
	if err := unix.Fstat(parent, &info); err != nil || info.Uid != 0 || info.Gid != 0 || info.Mode&0777 != 0700 {
		return nil, errors.New("guest alias parent is not root-private")
	}
	if err := unix.Mkdirat(parent, guestControlAliasDirectory, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	directory, err := unix.Openat2(parent, guestControlAliasDirectory, &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, err
	}
	if err := unix.Fstat(directory, &info); err != nil || info.Uid != 0 || info.Gid != 0 || info.Mode&0777 != 0700 {
		_ = unix.Close(directory)
		return nil, errors.New("guest alias directory is unsafe")
	}
	return &guestControlAliasStore{directory: directory}, nil
}
func (store *guestControlAliasStore) close() error { return unix.Close(store.directory) }
func (store *guestControlAliasStore) read(
	ctx context.Context, resource string,
) (guestControlAliasReceipt, unix.Stat_t, bool, error) {
	if err := ctx.Err(); err != nil {
		return guestControlAliasReceipt{}, unix.Stat_t{}, false, err
	}
	decoded, err := hex.DecodeString(resource)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != resource {
		return guestControlAliasReceipt{}, unix.Stat_t{}, false, errors.New("invalid alias resource")
	}
	fd, err := unix.Openat2(store.directory, resource+".json", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) {
		return guestControlAliasReceipt{}, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return guestControlAliasReceipt{}, unix.Stat_t{}, false, err
	}
	file := os.NewFile(uintptr(fd), "guest-alias-receipt")
	defer file.Close()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 ||
		before.Gid != 0 || before.Mode&0777 != 0600 || before.Nlink != 1 || before.Size > guestControlAliasRecordLimit {
		return guestControlAliasReceipt{}, before, false, errors.New("guest alias receipt metadata is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(file, guestControlAliasRecordLimit+1))
	if err != nil {
		return guestControlAliasReceipt{}, before, false, err
	}
	if err := unix.Fstat(fd, &after); err != nil || !sameGuestControlAliasFile(before, after) ||
		int64(len(raw)) != before.Size {
		return guestControlAliasReceipt{}, before, false, errors.New("guest alias receipt changed during read")
	}
	record, err := decodeGuestControlAliasReceipt(raw)
	if err != nil || record.Resource != resource {
		return record, before, false, errors.New("guest alias receipt identity is invalid")
	}
	return record, before, true, nil
}
func sameGuestControlAliasFile(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Uid == right.Uid &&
		left.Gid == right.Gid && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim &&
		left.Ctim == right.Ctim
}

// An unpublished candidate never authorizes an effect. Preserve it for inspection;
// only canonical published receipts participate in restart cleanup.
func (store *guestControlAliasStore) unfinishedCandidate(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(name, "."), ".tmp"))
	if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || "."+id.String()+".tmp" != name {
		return errors.New("guest alias inventory contains unknown state")
	}
	fd, err := unix.Openat2(store.directory, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "guest-alias-unpublished-candidate")
	defer file.Close()
	var before, after, named unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != 0 ||
		before.Gid != 0 || before.Mode&0777 != 0600 || before.Nlink != 1 ||
		before.Size < 0 || before.Size > guestControlAliasRecordLimit {
		return errors.New("guest alias candidate metadata is unsafe")
	}
	read, err := io.Copy(io.Discard, io.LimitReader(file, guestControlAliasRecordLimit+1))
	if err != nil {
		return err
	}
	if err := unix.Fstat(fd, &after); err != nil || !sameGuestControlAliasFile(before, after) || read != before.Size {
		return errors.New("guest alias candidate changed during read")
	}
	if err := unix.Fstatat(store.directory, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameGuestControlAliasFile(before, named) {
		return errors.New("guest alias candidate identity changed")
	}
	return ctx.Err()
}
func (store *guestControlAliasStore) list(ctx context.Context) ([]guestControlAliasReceipt, error) {
	fd, err := unix.Openat(store.directory, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), "guest-alias-inventory")
	defer directory.Close()
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	records := make([]guestControlAliasReceipt, 0, len(names))
	for _, name := range names {
		resource := strings.TrimSuffix(name, ".json")
		if resource+".json" != name {
			if err := store.unfinishedCandidate(ctx, name); err != nil {
				return nil, err
			}
			continue
		}
		record, _, found, err := store.read(ctx, resource)
		if err != nil || !found {
			return nil, errors.New("guest alias inventory is ambiguous")
		}
		records = append(records, record)
	}
	return records, nil
}
func (store *guestControlAliasStore) put(
	ctx context.Context, record guestControlAliasReceipt, previous *guestControlAliasReceipt,
) error {
	raw, err := encodeGuestControlAliasReceipt(record)
	if err != nil {
		return err
	}
	before, evidence, found, err := store.read(ctx, record.Resource)
	if err != nil {
		return err
	}
	if previous == nil && found || previous != nil && (!found || before != *previous) {
		return errors.New("guest alias receipt CAS changed")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	name := "." + id.String() + ".tmp"
	fd, err := unix.Openat(store.directory, name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(store.directory, name, 0)
	file := os.NewFile(uintptr(fd), "guest-alias-candidate")
	if err := unix.Fchown(fd, 0, 0); err != nil {
		_ = file.Close()
		return err
	}
	if err := unix.Fchmod(fd, 0600); err != nil {
		_ = file.Close()
		return err
	}
	if written, err := file.Write(raw); err != nil || written != len(raw) {
		_ = file.Close()
		return errors.New("guest alias receipt write failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	observed, stat, exists, err := store.read(ctx, record.Resource)
	if err != nil || exists != found || found && (observed != before || !sameGuestControlAliasFile(evidence, stat)) {
		return errors.New("guest alias receipt publication evidence changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !found {
		err = unix.Renameat2(store.directory, name, store.directory, record.Resource+".json", unix.RENAME_NOREPLACE)
	} else {
		err = unix.Renameat(store.directory, name, store.directory, record.Resource+".json")
	}
	if err != nil {
		return err
	}
	return unix.Fsync(store.directory)
}
func (store *guestControlAliasStore) remove(ctx context.Context, record guestControlAliasReceipt) error {
	observed, _, found, err := store.read(ctx, record.Resource)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if observed != record {
		return errors.New("guest alias removal receipt changed")
	}
	if err := unix.Unlinkat(store.directory, record.Resource+".json", 0); err != nil {
		return err
	}
	return unix.Fsync(store.directory)
}
