package infra

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// GetWarmImagesDir returns a writable warm-image pool for the invoking operator.
// The default remains on /tmp's filesystem, but never shares a 0700 directory
// between different operator UIDs.
func GetWarmImagesDir(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if pool, _ := EnvGet("WARM_POOL"); pool == "disk" {
		path := filepath.Join(GetImagesDir(), "ready")
		return path, ensureDirAndChown(path)
	}
	if override, _ := EnvGet("TEMP_DIR"); override != "" {
		path := filepath.Join(GetTempDir(), "ready")
		return path, ensureDirAndChown(path)
	}

	uid, gid := os.Getuid(), os.Getgid()
	if callerUID, callerGID, sudo := GetRealUserIDs(); sudo {
		uid, gid = callerUID, callerGID
	}
	if err := admitWarmTempRoot("/tmp", uid); err != nil {
		return "", err
	}
	operatorDir := filepath.Join("/tmp", fmt.Sprintf("%s-%d", ProjectName, uid))
	if err := ensurePrivateWarmDirectory(ctx, operatorDir, uid, gid); err != nil {
		return "", err
	}
	ready := filepath.Join(operatorDir, "ready")
	if err := ensurePrivateWarmDirectory(ctx, ready, uid, gid); err != nil {
		return "", err
	}
	return ready, nil
}

func admitWarmTempRoot(path string, uid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect warm-image temporary root: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (stat.Uid != 0 && stat.Uid != uint32(uid)) ||
		(info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
		return fmt.Errorf("warm-image temporary root is not a trusted sticky directory")
	}
	return nil
}

func ensurePrivateWarmDirectory(ctx context.Context, path string, uid, gid int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	created := false
	if err := os.Mkdir(path, 0700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create private warm-image directory: %w", err)
	}
	if created {
		if os.Geteuid() == 0 && (uid != 0 || gid != 0) {
			if err := os.Chown(path, uid, gid); err != nil {
				return fmt.Errorf("own private warm-image directory: %w", err)
			}
		}
		if err := os.Chmod(path, 0700); err != nil {
			return fmt.Errorf("mode private warm-image directory: %w", err)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private warm-image directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || info.Mode().Perm() != 0700 {
		return fmt.Errorf("private warm-image directory ownership or mode is invalid")
	}
	return nil
}
