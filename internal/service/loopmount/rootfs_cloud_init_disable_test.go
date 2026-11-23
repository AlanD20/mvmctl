package loopmount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: OFF must suppress cloud-init even when the image retains it and deblob is omitted.
// Real guest-tree readback catches dropped disable intent while unrelated bytes prove this is not deblob.
func TestApplyRootfsProvisionPlanDisablesCloudInitWithoutDeblob(t *testing.T) {
	t.Parallel()

	for _, init := range []string{"openrc", "systemd", "no_init_services"} {
		t.Run(init, func(t *testing.T) {
			root := t.TempDir()
			preserved := map[string]string{
				"/etc/rc.conf":            "#rc_parallel=\"NO\"\n",
				"/etc/network/interfaces": "auto eth0\niface eth0 inet dhcp\n",
				"/etc/dhcpcd.conf":        "persistent\n",
				"/etc/inittab":            "::wait:/sbin/openrc default\n",
				"/etc/cloud/cloud.cfg":    "datasource_list: [NoCloud]\n",
				"/var/cache/apk/cache":    "keep package cache\n",
				"/var/log/messages":       "keep guest logs\n",
				"/tmp/keep":               "keep temporary data\n",
				"/usr/share/doc/keep":     "keep documentation\n",
			}
			for path, content := range preserved {
				fullPath := filepath.Join(root, strings.TrimPrefix(path, "/"))
				require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
				require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))
			}
			// CONTRACT: Alpine's boot-local, four default services, and legacy ssh init activation
			// must be absent after OFF; only these fixed cloud-init links are removed.
			links := []string{
				"/etc/runlevels/boot/cloud-init-local",
				"/etc/runlevels/default/cloud-init",
				"/etc/runlevels/default/cloud-config",
				"/etc/runlevels/default/cloud-final",
				"/etc/runlevels/default/cloud-init-hotplugd",
				"/etc/runlevels/ssh/cloud-init",
			}
			units := []string{
				"cloud-init.service", "cloud-init-local.service", "cloud-config.service", "cloud-final.service",
			}
			if init == "openrc" {
				for _, path := range links {
					fullPath := filepath.Join(root, strings.TrimPrefix(path, "/"))
					require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
					require.NoError(t, os.Symlink("/etc/init.d/"+filepath.Base(path), fullPath))
				}
				for _, name := range []string{"chronyd", "sshd", "mvm-agent"} {
					require.NoError(t, os.Symlink("/etc/init.d/"+name,
						filepath.Join(root, "etc/runlevels/default", name)))
				}
			}
			if init == "systemd" {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/systemd/system"), 0755))
				for _, unit := range units {
					require.NoError(t, os.Symlink("/usr/lib/systemd/system/"+unit,
						filepath.Join(root, "etc/systemd/system", unit)))
				}
				require.NoError(t, os.Symlink("/vendor/chronyd.service",
					filepath.Join(root, "etc/systemd/system/chronyd.service")))
			}
			var plan VMProvisionPlan
			// The emitted field is the independent intent contract; this test can run unchanged
			// against the pre-fix owner, which ignores it and leaves cloud-init enabled.
			require.NoError(t, json.Unmarshal([]byte(`{"disable_cloud_init":true}`), &plan))
			tree := newTestUnprivilegedRootfsGuestTree(t, root)

			_, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{}, plan)
			require.NoError(t, err)
			assertRootfsProvisionedFile(t, root, "/etc/cloud/cloud-init.disabled",
				[]byte("disabled by mvmctl\n"), 0644)
			assertRootfsProvisionedFile(t, root, "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
				[]byte("datasource_list: [None]\n"), 0644)
			for path, content := range preserved {
				assertRootfsProvisionedFile(t, root, path, []byte(content), 0644)
			}
			if init == "openrc" {
				for _, path := range links {
					_, err := os.Lstat(filepath.Join(root, strings.TrimPrefix(path, "/")))
					assert.ErrorIs(t, err, os.ErrNotExist)
				}
				for _, name := range []string{"chronyd", "sshd", "mvm-agent"} {
					assertRootfsProvisionedSymlink(t, root, "/etc/runlevels/default/"+name, "/etc/init.d/"+name)
				}
				assert.NoDirExists(t, filepath.Join(root, "etc/systemd"))
			}
			if init == "systemd" {
				for _, unit := range units {
					assertRootfsProvisionedSymlink(t, root, "/etc/systemd/system/"+unit, "/dev/null")
				}
				assertRootfsProvisionedSymlink(t, root,
					"/etc/systemd/system/chronyd.service", "/vendor/chronyd.service")
				assert.NoDirExists(t, filepath.Join(root, "etc/runlevels"))
			}
			replayed, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{}, plan)
			require.NoError(t, err)
			assert.Zero(t, replayed, "OFF replay must not republish unchanged disable files or masks")
			assertRootfsProvisionedFile(t, root, "/etc/cloud/cloud-init.disabled",
				[]byte("disabled by mvmctl\n"), 0644)
			for path, content := range preserved {
				assertRootfsProvisionedFile(t, root, path, []byte(content), 0644)
			}
		})
	}
}

// Rationale: OFF must not follow guest-controlled parent links or silently accept a non-link
// activation entry. Errors after publication retain mutation evidence and never change outside bytes.
func TestApplyRootfsProvisionPlanCloudInitOffPreservesSafetyAndPartialState(t *testing.T) {
	t.Parallel()

	for _, fault := range []string{
		"cloud_parent_symlink", "runlevel_parent_symlink", "regular_runlevel_leaf", "unlink_sync",
	} {
		t.Run(fault, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc"), 0755))
			outsideFile := filepath.Join(outside, "victim")
			require.NoError(t, os.WriteFile(outsideFile, []byte("outside unchanged\n"), 0644))
			activation := filepath.Join(root, "etc/runlevels/boot/cloud-init-local")
			if fault == "cloud_parent_symlink" {
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "etc/cloud")))
			} else if fault == "runlevel_parent_symlink" {
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "etc/runlevels")))
			} else {
				require.NoError(t, os.MkdirAll(filepath.Dir(activation), 0755))
				if fault == "regular_runlevel_leaf" {
					require.NoError(t, os.WriteFile(activation, []byte("must not unlink regular file\n"), 0644))
				} else {
					require.NoError(t, os.Symlink(outsideFile, activation))
				}
			}
			tree := newTestUnprivilegedRootfsGuestTree(t, root)
			syncFault := errors.New("sync removed activation")
			if fault == "unlink_sync" {
				unlink, sync := tree.deps.unlinkAt, tree.deps.fsync
				removed := false
				tree.deps.unlinkAt = func(ctx context.Context, fd int, leaf string, flags int) error {
					err := unlink(ctx, fd, leaf, flags)
					removed = removed || err == nil && leaf == "cloud-init-local"
					return err
				}
				tree.deps.fsync = func(ctx context.Context, fd int) error {
					if removed {
						return syncFault
					}
					return sync(ctx, fd)
				}
			}
			var plan VMProvisionPlan
			require.NoError(t, json.Unmarshal([]byte(`{"disable_cloud_init":true}`), &plan))

			_, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{}, plan)
			require.Error(t, err)
			// Partial state is the error-path contract, so inspect it even though no success result exists.
			assertRootfsProvisionedFile(t, outside, "/victim", []byte("outside unchanged\n"), 0644)
			if fault == "cloud_parent_symlink" {
				assert.ErrorContains(t, err, "open fixed managed guest directory")
				assert.Equal(t, vmProvisionEffectStateUnchanged, tree.tracker.state())
				assert.NoFileExists(t, filepath.Join(outside, "cloud-init.disabled"))
				return
			}
			assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tree.tracker.state())
			assertRootfsProvisionedFile(t, root, "/etc/cloud/cloud-init.disabled",
				[]byte("disabled by mvmctl\n"), 0644)
			if fault == "regular_runlevel_leaf" {
				assert.ErrorContains(t, err, "runlevel entry is not a single link")
				assertRootfsProvisionedFile(t, root, "/etc/runlevels/boot/cloud-init-local",
					[]byte("must not unlink regular file\n"), 0644)
			}
			if fault == "unlink_sync" {
				assert.ErrorIs(t, err, syncFault)
				_, statErr := os.Lstat(activation)
				assert.ErrorIs(t, statErr, os.ErrNotExist)
			}
			if fault == "runlevel_parent_symlink" {
				assert.ErrorContains(t, err, "open managed guest file parent")
			}
		})
	}
}
