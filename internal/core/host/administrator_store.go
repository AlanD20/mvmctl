package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const administratorJournalLimit = 64 * 1024

type administratorJournal struct {
	Version         int     `json:"version"`
	Operator        string  `json:"operator"`
	UID             string  `json:"uid"`
	GID             string  `json:"gid"`
	OriginalForward string  `json:"original_forward"`
	OriginalSysctl  *string `json:"original_sysctl"`
	OriginalSudoers *string `json:"original_sudoers"`
	GroupExisted    bool    `json:"group_existed"`
	GroupGID        *string `json:"group_gid"`
	AppliedSudoers  string  `json:"applied_sudoers"`
	MemberExisted   bool    `json:"member_existed"`
	Completed       int     `json:"completed"`
	Pending         bool    `json:"pending"`
	Initialized     bool    `json:"initialized"`
	ResetState      string  `json:"reset_state,omitempty"`
	ResetStep       int     `json:"reset_step,omitempty"`
}

// openAdministratorDirectory only traverses compiled root-owned paths. It never
// follows symlinks or repairs the ownership of existing directories.
func openAdministratorDirectory(
	ctx context.Context,
	parts []string,
	createFrom int,
) (result *os.File, returnErr error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	defer func() {
		if current != nil {
			returnErr = errors.Join(returnErr, current.Close())
		}
	}()
	for index := -1; index < len(parts); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index >= 0 {
			name := parts[index]
			child, err := unix.Openat(int(current.Fd()), name,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if errors.Is(err, unix.ENOENT) && createFrom >= 0 && index >= createFrom {
				if err := unix.Mkdirat(int(current.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
					return nil, err
				}
				if err := current.Sync(); err != nil {
					return nil, err
				}
				child, err = unix.Openat(int(current.Fd()), name,
					unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			}
			if err != nil {
				return nil, err
			}
			next := os.NewFile(uintptr(child), name)
			err = current.Close()
			current = next
			if err != nil {
				return nil, err
			}
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(current.Fd()), &stat); err != nil {
			return nil, err
		}
		if stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0022 != 0 ||
			(index == len(parts)-1 && createFrom >= 0 && stat.Mode&07777 != 0700) {
			return nil, errors.New("unsafe administrator directory ownership or mode")
		}
	}
	result, current = current, nil
	return result, nil
}

func readAdministratorFile(dir *os.File, name string, mode uint32) (data []byte, returnErr error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&07777 != mode ||
		stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size < 0 || stat.Size > administratorJournalLimit {
		return nil, errors.New("unsafe administrator file metadata")
	}
	data, err = io.ReadAll(io.LimitReader(file, administratorJournalLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != stat.Size {
		return nil, errors.New("administrator file changed during read")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if !sameSudoersFile(stat, after) {
		return nil, errors.New("administrator file changed during read")
	}
	return data, nil
}

func writeAdministratorFile(
	ctx context.Context,
	dir *os.File,
	name string,
	data []byte,
	mode uint32,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > administratorJournalLimit {
		return errors.New("administrator state exceeds size limit")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := ".host-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(
		int(dir.Fd()),
		temp,
		unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL,
		mode,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	published := false
	defer func() {
		if file != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if !published {
			returnErr = errors.Join(returnErr, unix.Unlinkat(int(dir.Fd()), temp, 0))
		}
	}()
	if err := file.Chown(0, 0); err != nil {
		return err
	}
	if err := file.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	file = nil
	if err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), temp, int(dir.Fd()), name); err != nil {
		return err
	}
	published = true
	if err := dir.Sync(); err != nil {
		return errs.WrapMsg(errs.CodeHostInitFailed, "sync administrator publication directory", err,
			errs.WithDetails(map[string]any{"file_replaced": true, "durability_uncertain": true}))
	}
	return nil
}

func loadAdministratorJournal(dir *os.File) (*administratorJournal, error) {
	data, err := readAdministratorFile(dir, "journal.json", 0600)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal administratorJournal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing administrator journal data")
	}
	if journal.Version != 2 || journal.Completed < 0 || journal.Completed > 6 ||
		(journal.Completed > 0 && journal.GroupGID == nil) || journal.AppliedSudoers == "" ||
		(journal.OriginalForward != "0" && journal.OriginalForward != "1") ||
		(journal.Initialized && (journal.Pending || journal.Completed != 6)) {
		return nil, errors.New("invalid administrator journal")
	}
	if (journal.ResetState != "" && journal.ResetState != "restoring" && journal.ResetState != "complete") ||
		journal.ResetStep < 0 || journal.ResetStep > 4 ||
		(journal.ResetState == "" && journal.ResetStep != 0) ||
		(journal.ResetState == "complete" && (journal.ResetStep != 4 || journal.Initialized)) {
		return nil, errors.New("invalid administrator reset state")
	}
	return &journal, nil
}

func saveAdministratorJournal(ctx context.Context, dir *os.File, journal *administratorJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return writeAdministratorFile(ctx, dir, "journal.json", data, 0600)
}
