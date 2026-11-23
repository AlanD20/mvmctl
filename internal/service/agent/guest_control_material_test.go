package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolauth"
)

func TestGuestControlMaterialRequiresPinnedRootPrivateCanonicalFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned material requires approved root QA")
	}
	_, material, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	raw, err := material.Encode()
	require.NoError(t, err)
	for _, name := range []string{"valid", "absent", "symlink", "public", "directory", "invalid", "hardlink"} {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir()
			require.NoError(t, os.Chmod(path, 0700))
			require.NoError(t, os.Chown(path, 0, 0))
			fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			require.NoError(t, err)
			defer unix.Close(fd)
			leaf := filepath.Join(path, "guest-control.json")
			switch name {
			case "absent":
			case "symlink":
				require.NoError(t, os.Symlink("target", leaf))
			case "directory":
				require.NoError(t, os.Mkdir(leaf, 0700))
			case "invalid":
				require.NoError(t, os.WriteFile(leaf, []byte("invalid"), 0600))
			default:
				require.NoError(t, os.WriteFile(leaf, raw, 0600))
				if name == "public" {
					require.NoError(t, os.Chmod(leaf, 0644))
				}
				if name == "hardlink" {
					require.NoError(t, os.Link(leaf, filepath.Join(path, "alias")))
				}
			}
			loaded, enabled, err := loadGuestControlMaterialAt(t.Context(), fd, "guest-control.json")
			if name == "absent" {
				require.NoError(t, err)
				require.False(t, enabled)
				return
			}
			if name != "valid" {
				require.Error(t, err)
				require.False(t, enabled)
				return
			}
			require.NoError(t, err)
			require.True(t, enabled)
			observed, err := loaded.Encode()
			require.NoError(t, err)
			require.Equal(t, raw, observed)
		})
	}
}

func TestGuestControlLocalListenerRefusesUnsafeAndActiveSocket(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("private root listener requires approved root QA")
	}
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	require.NoError(t, os.Chown(directory, 0, 0))
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	listener, err := listenGuestControlLocalAt(fd)
	require.NoError(t, err)
	_, err = listenGuestControlLocalAt(fd)
	require.Error(t, err, "never replace a live listener")
	require.NoError(t, listener.Close())
	require.NoError(t, os.Chmod(directory, 0755))
	_, err = listenGuestControlLocalAt(fd)
	require.Error(t, err, "weaker remote-exec directory permissions cannot authorize control")
}
