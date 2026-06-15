package provcontent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/provcontent"
)

// ─── SSHDConfig ─────────────────────────────────────────────────────────────
// Rationale: SSHDConfig generates the sshd drop-in config. Wrong content means
// SSH auth breaks — users get locked out of their microVMs.

func TestBuilder_SSHDConfig(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		user string
		want string
	}{
		"root_user": {
			user: "root",
			want: "PubkeyAuthentication yes\n" +
				"AuthorizedKeysFile .ssh/authorized_keys\n" +
				"PasswordAuthentication no\n" +
				"PermitEmptyPasswords no\n" +
				"UsePAM yes\n" +
				"UseDNS no\n" +
				"GSSAPIAuthentication no\n" +
				"PermitRootLogin prohibit-password\n",
		},
		"non_root_user": {
			user: "testuser",
			want: "PubkeyAuthentication yes\n" +
				"AuthorizedKeysFile .ssh/authorized_keys\n" +
				"PasswordAuthentication no\n" +
				"PermitEmptyPasswords no\n" +
				"UsePAM yes\n" +
				"UseDNS no\n" +
				"GSSAPIAuthentication no\n" +
				"AllowUsers testuser\n",
		},
		"empty_user": {
			user: "",
			want: "PubkeyAuthentication yes\n" +
				"AuthorizedKeysFile .ssh/authorized_keys\n" +
				"PasswordAuthentication no\n" +
				"PermitEmptyPasswords no\n" +
				"UsePAM yes\n" +
				"UseDNS no\n" +
				"GSSAPIAuthentication no\n" +
				"AllowUsers \n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.SSHDConfig(tc.user)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SSHDConfig() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── FirstBootInstaller ──────────────────────────────────────────────────────
// Rationale: FirstBootInstaller returns a shell script that installs SSH on
// first boot. Missing or malformed scripts mean VMs boot without SSH access.

func TestBuilder_FirstBootInstaller(t *testing.T) {
	t.Parallel()

	var b provcontent.Builder
	got := b.FirstBootInstaller()

	assert.Contains(t, got, "#!/bin/bash", "must be a bash script")
	assert.Contains(t, got, "first-boot-ssh-installer.service", "must contain self-cleanup")
	assert.Contains(t, got, "openssh", "must install SSH")
}

// ─── FirstBootService ────────────────────────────────────────────────────────
// Rationale: FirstBootService returns the systemd unit that triggers the
// first-boot installer. Wrong unit means the installer never runs.

func TestBuilder_FirstBootService(t *testing.T) {
	t.Parallel()

	var b provcontent.Builder
	got := b.FirstBootService()

	assert.Contains(t, got, "Description=First-boot SSH installer")
	assert.Contains(t, got, "ExecStart=/usr/local/bin/first-boot-ssh-installer.sh")
	assert.Contains(t, got, "WantedBy=multi-user.target")
}

// ─── Hosts ───────────────────────────────────────────────────────────────────
// Rationale: Hosts generates /etc/hosts with the VM hostname at 127.0.1.1.
// A misconfigured hosts file breaks hostname resolution and sudo.

func TestBuilder_Hosts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		hostname string
		want     string
	}{
		"simple_hostname": {
			hostname: "my-vm",
			want: "127.0.0.1\tlocalhost\n" +
				"127.0.1.1\tmy-vm\n" +
				"\n" +
				"::1\tlocalhost ip6-localhost ip6-loopback\n" +
				"fe00::0\tip6-localnet\n" +
				"ff00::0\tip6-mcastprefix\n" +
				"ff02::1\tip6-allnodes\n" +
				"ff02::2\tip6-allrouters\n",
		},
		"empty_hostname": {
			hostname: "",
			want: "127.0.0.1\tlocalhost\n" +
				"127.0.1.1\t\n" +
				"\n" +
				"::1\tlocalhost ip6-localhost ip6-loopback\n" +
				"fe00::0\tip6-localnet\n" +
				"ff00::0\tip6-mcastprefix\n" +
				"ff02::1\tip6-allnodes\n" +
				"ff02::2\tip6-allrouters\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.Hosts(tc.hostname)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Hosts() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildHostnameOps ────────────────────────────────────────────────────────
// Rationale: BuildHostnameOps creates FileOps for /etc/hostname and /etc/hosts.
// Wrong paths or modes mean hostname configuration fails silently.

func TestBuilder_BuildHostnameOps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		hostname string
		want     []provcontent.Operation
	}{
		"valid_hostname": {
			hostname: "my-vm",
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/etc/hostname",
					Data: []byte("my-vm"),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
				provcontent.FileOp{
					Path: "/etc/hosts",
					Data: []byte("127.0.0.1\tlocalhost\n" +
						"127.0.1.1\tmy-vm\n" +
						"\n" +
						"::1\tlocalhost ip6-localhost ip6-loopback\n" +
						"fe00::0\tip6-localnet\n" +
						"ff00::0\tip6-mcastprefix\n" +
						"ff02::1\tip6-allnodes\n" +
						"ff02::2\tip6-allrouters\n"),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
			},
		},
		"empty_hostname": {
			hostname: "",
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/etc/hostname",
					Data: []byte(""),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
				provcontent.FileOp{
					Path: "/etc/hosts",
					Data: []byte("127.0.0.1\tlocalhost\n" +
						"127.0.1.1\t\n" +
						"\n" +
						"::1\tlocalhost ip6-localhost ip6-loopback\n" +
						"fe00::0\tip6-localnet\n" +
						"ff00::0\tip6-mcastprefix\n" +
						"ff02::1\tip6-allnodes\n" +
						"ff02::2\tip6-allrouters\n"),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildHostnameOps(tc.hostname)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildHostnameOps() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildDNSOps ─────────────────────────────────────────────────────────────
// Rationale: BuildDNSOps creates the resolv.conf FileOp. Wrong content means
// VMs cannot resolve DNS names, breaking apt/apk/pacman on first boot.

func TestBuilder_BuildDNSOps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		dnsServer string
		want      []provcontent.Operation
	}{
		"valid_dns": {
			dnsServer: "8.8.8.8",
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/etc/resolv.conf",
					Data: []byte("nameserver 8.8.8.8\n"),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
			},
		},
		"empty_dns": {
			dnsServer: "",
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/etc/resolv.conf",
					Data: []byte("nameserver \n"),
					Mode: 0644,
					UID:  0,
					GID:  0,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildDNSOps(tc.dnsServer)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildDNSOps() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildSSHOps ─────────────────────────────────────────────────────────────
// Rationale: BuildSSHOps injects SSH authorized_keys and ensures correct
// ownership. Wrong chowns or missing useradd mean SSH pubkey auth fails.

func TestBuilder_BuildSSHOps(t *testing.T) {
	t.Parallel()

	pubkeys := []string{"ssh-ed25519 AAAAC3... user@host"}
	keyData := []byte("ssh-ed25519 AAAAC3... user@host\n")

	tests := map[string]struct {
		user    string
		pubkeys []string
		want    []provcontent.Operation
	}{
		"empty_pubkeys_returns_empty": {
			user:    "root",
			pubkeys: nil,
			want:    nil,
		},
		"root_user_with_pubkeys": {
			user:    "root",
			pubkeys: pubkeys,
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/root/.ssh/authorized_keys",
					Data: keyData,
					Mode: 0600,
					UID:  0,
					GID:  0,
				},
				provcontent.ChrootOp{Command: "chown root:root /root"},
			},
		},
		"non_root_user_with_pubkeys": {
			user:    "testuser",
			pubkeys: pubkeys,
			want: []provcontent.Operation{
				provcontent.FileOp{
					Path: "/root/.ssh/authorized_keys",
					Data: keyData,
					Mode: 0600,
					UID:  0,
					GID:  0,
				},
				provcontent.FileOp{
					Path: "/home/testuser/.ssh/authorized_keys",
					Data: keyData,
					Mode: 0600,
					UID:  0,
					GID:  0,
				},
				provcontent.ChrootOp{Command: "useradd -m testuser"},
				provcontent.ChrootOp{Command: "chown testuser:testuser /home/testuser"},
				provcontent.ChrootOp{Command: "chown testuser:testuser /home/testuser/.ssh"},
				provcontent.ChrootOp{Command: "chown testuser:testuser /home/testuser/.ssh/authorized_keys"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildSSHOps(tc.user, tc.pubkeys)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildSSHOps() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── SetupSudo ───────────────────────────────────────────────────────────────
// Rationale: SetupSudo fixes broken ownership in cloud images and creates a
// passwordless sudoers drop-in. Wrong chowns break sudo entirely.

func TestBuilder_SetupSudo(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		user string
		want []provcontent.Operation
	}{
		"valid_user": {
			user: "testuser",
			want: []provcontent.Operation{
				provcontent.ChrootOp{Command: "mkdir -p /etc/sudoers.d"},
				provcontent.ChrootOp{Command: "echo 'testuser ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/testuser"},
				provcontent.ChrootOp{Command: "chmod 440 /etc/sudoers.d/testuser"},
				provcontent.ChrootOp{Command: "chown root:root /etc/sudo.conf && \\\n" +
					"chmod 0440 /etc/sudo.conf && \\\n" +
					"chown root:root /etc/sudoers && \\\n" +
					"chmod 0440 /etc/sudoers && \\\n" +
					"chown root:root -R /etc/sudoers.d && \\\n" +
					"chown root:root /usr/bin/sudo && \\\n" +
					"chmod 4755 /usr/bin/sudo"},
			},
		},
		"root_user": {
			user: "root",
			want: []provcontent.Operation{
				provcontent.ChrootOp{Command: "mkdir -p /etc/sudoers.d"},
				provcontent.ChrootOp{Command: "echo 'root ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/root"},
				provcontent.ChrootOp{Command: "chmod 440 /etc/sudoers.d/root"},
				provcontent.ChrootOp{Command: "chown root:root /etc/sudo.conf && \\\n" +
					"chmod 0440 /etc/sudo.conf && \\\n" +
					"chown root:root /etc/sudoers && \\\n" +
					"chmod 0440 /etc/sudoers && \\\n" +
					"chown root:root -R /etc/sudoers.d && \\\n" +
					"chown root:root /usr/bin/sudo && \\\n" +
					"chmod 4755 /usr/bin/sudo"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.SetupSudo(tc.user)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SetupSudo() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildCloudInitDisableOps ────────────────────────────────────────────────
// Rationale: BuildCloudInitDisableOps masks cloud-init to speed up boot.
// Skipping cloud-init prevents 5+ seconds of unnecessary boot delay.

func TestBuilder_BuildCloudInitDisableOps(t *testing.T) {
	t.Parallel()

	var b provcontent.Builder
	got := b.BuildCloudInitDisableOps()

	require.Len(t, got, 8, "BuildCloudInitDisableOps must return 8 operations")

	// Check FileOp paths
	ops0, ok0 := got[0].(provcontent.FileOp)
	require.True(t, ok0, "ops[0] must be FileOp")
	assert.Equal(t, "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg", ops0.Path)
	assert.Equal(t, 0644, ops0.Mode)

	ops1, ok1 := got[1].(provcontent.FileOp)
	require.True(t, ok1, "ops[1] must be FileOp")
	assert.Equal(t, "/etc/cloud/cloud-init.disabled", ops1.Path)

	ops2, ok2 := got[2].(provcontent.FileOp)
	require.True(t, ok2, "ops[2] must be FileOp")
	assert.Equal(t, "/etc/systemd/system/snapd.seeded.service.d/override.conf", ops2.Path)

	ops3, ok3 := got[3].(provcontent.FileOp)
	require.True(t, ok3, "ops[3] must be FileOp")
	assert.Equal(t, "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf", ops3.Path)

	// Check ChrootOp commands (cloud-init service masks)
	expectedMasks := []string{
		"cloud-init.service",
		"cloud-init-local.service",
		"cloud-config.service",
		"cloud-final.service",
	}
	for i, svc := range expectedMasks {
		op, ok := got[4+i].(provcontent.ChrootOp)
		require.True(t, ok, "ops[%d] must be ChrootOp", 4+i)
		assert.Equal(t, "ln -sf /dev/null /etc/systemd/system/"+svc, op.Command,
			"ops[%d] command mismatch", 4+i)
	}
}

// ─── BuildCloudInitInjectOps ─────────────────────────────────────────────────
// Rationale: BuildCloudInitInjectOps injects a nocloud-net seed directory.
// If the source dir doesn't exist, it must return nil to avoid build failures.

func TestBuilder_BuildCloudInitInjectOps(t *testing.T) {
	t.Parallel()

	t.Run("nonexistent_dir_returns_nil", func(t *testing.T) {
		var b provcontent.Builder
		got := b.BuildCloudInitInjectOps("/tmp/nonexistent-cloud-init-dir-this-should-not-exist")
		assert.Nil(t, got)
	})

	t.Run("existent_dir_returns_copy_op", func(t *testing.T) {
		tmpDir := t.TempDir()
		seedDir := filepath.Join(tmpDir, "seed")
		err := os.Mkdir(seedDir, 0755)
		require.NoError(t, err)

		var b provcontent.Builder
		got := b.BuildCloudInitInjectOps(seedDir)

		require.Len(t, got, 1, "must return exactly one operation")

		op, ok := got[0].(provcontent.CopyDirOp)
		require.True(t, ok, "must be CopyDirOp")
		assert.Equal(t, seedDir, op.Src)
		assert.Equal(t, "/var/lib/cloud/seed/nocloud-net", op.Dst)
	})
}

// ─── BuildResizeOps ──────────────────────────────────────────────────────────
// Rationale: BuildResizeOps creates a grow operation. Wrong action means the
// root filesystem is not expanded when the disk is larger than the image.

func TestBuilder_BuildResizeOps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		size int64
		want []provcontent.Operation
	}{
		"positive_size": {
			size: 10 * 1024 * 1024 * 1024, // 10 GiB
			want: []provcontent.Operation{
				provcontent.ResizeOp{
					Action: provcontent.ResizeActionGrow,
					Bytes:  10 * 1024 * 1024 * 1024,
				},
			},
		},
		"zero_size": {
			size: 0,
			want: []provcontent.Operation{
				provcontent.ResizeOp{
					Action: provcontent.ResizeActionGrow,
					Bytes:  0,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildResizeOps(tc.size)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildResizeOps() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildShrinkOps ──────────────────────────────────────────────────────────
// Rationale: BuildShrinkOps creates a shrink operation. Wrong action means the
// filesystem grows instead of shrinking, potentially corrupting the image.

func TestBuilder_BuildShrinkOps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		limit int64
		want  []provcontent.Operation
	}{
		"positive_limit": {
			limit: 5 * 1024 * 1024 * 1024, // 5 GiB
			want: []provcontent.Operation{
				provcontent.ResizeOp{
					Action: provcontent.ResizeActionShrink,
					Bytes:  5 * 1024 * 1024 * 1024,
				},
			},
		},
		"zero_limit": {
			limit: 0,
			want: []provcontent.Operation{
				provcontent.ResizeOp{
					Action: provcontent.ResizeActionShrink,
					Bytes:  0,
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildShrinkOps(tc.limit)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("BuildShrinkOps() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── BuildFixFstabOps ────────────────────────────────────────────────────────
// Rationale: BuildFixFstabOps comments out UUID/PARTUUID entries in /etc/fstab
// that are invalid in Firecracker. Missing this step causes boot failures.

func TestBuilder_BuildFixFstabOps(t *testing.T) {
	t.Parallel()

	var b provcontent.Builder
	got := b.BuildFixFstabOps()

	require.Len(t, got, 1, "must return exactly one operation")

	op, ok := got[0].(provcontent.ChrootOp)
	require.True(t, ok, "must be ChrootOp")
	assert.Contains(t, op.Command, "/etc/fstab")
	assert.Contains(t, op.Command, "PARTUUID")
	assert.Contains(t, op.Command, "UUID=")
}

// ─── BuildDeblobOps ──────────────────────────────────────────────────────────
// Rationale: BuildDeblobOps removes OS package cache, disables unneeded
// services, and injects SSH config. It is the most complex Builder method
// with 6 OS-specific branches. Wrong branch selection means bloated images
// or broken boot for specific distros.

func TestBuilder_BuildDeblobOps(t *testing.T) {
	t.Parallel()

	// Expected operation counts per OS family (hardcoded static values)
	const (
		countUbuntuDebian = 19
		countAlpine       = 23
		countArch         = 25
		countRedHat       = 17
		countUnknown      = 16
	)

	// Verify base operations that are present in ALL results.
	checkBaseOps := func(t *testing.T, ops []provcontent.Operation) {
		t.Helper()

		// ops[0-2]: Common cleanup
		assert.Contains(t, ops[0].(provcontent.ChrootOp).Command, "rm -rf /var/log/* /tmp/* /var/tmp/*")
		assert.Contains(t, ops[1].(provcontent.ChrootOp).Command, "rm -rf /usr/share/doc/*")
		assert.Contains(t, ops[2].(provcontent.ChrootOp).Command, "find /var/log -type f -delete")

		// ops[3]: Services mask script
		maskScript := ops[3].(provcontent.ChrootOp).Command
		assert.Contains(t, maskScript, "systemd-timesyncd.service")
		assert.Contains(t, maskScript, "unattended-upgrades.service")
		assert.Contains(t, maskScript, "ln -sf /dev/null")

		// ops[4]: SSH daemon config FileOp
		fop4 := ops[4].(provcontent.FileOp)
		assert.Equal(t, "/etc/ssh/sshd_config.d/mvm.conf", fop4.Path)
		assert.Equal(t, 0644, fop4.Mode)
		assert.Contains(t, string(fop4.Data), "PermitRootLogin prohibit-password")

		// ops[5]: First boot installer FileOp
		fop5 := ops[5].(provcontent.FileOp)
		assert.Equal(t, "/usr/local/bin/first-boot-ssh-installer.sh", fop5.Path)
		assert.Equal(t, 0755, fop5.Mode)

		// ops[6]: First boot service FileOp
		fop6 := ops[6].(provcontent.FileOp)
		assert.Equal(t, "/etc/systemd/system/first-boot-ssh-installer.service", fop6.Path)
		assert.Equal(t, 0644, fop6.Mode)

		// ops[7]: ssh-keygen
		assert.Equal(t, "ssh-keygen -A", ops[7].(provcontent.ChrootOp).Command)

		// ops[8]: SSH enable script
		assert.Contains(t, ops[8].(provcontent.ChrootOp).Command, "systemctl enable sshd")
	}

	// Verify final operations (cloud-init disable + cleanup) present in ALL results.
	checkFinalOps := func(t *testing.T, ops []provcontent.Operation, totalCount int) {
		t.Helper()

		// The last 6 ops are: 4 FileOps + 2 ChrootOps
		finalStart := totalCount - 6

		fopA := ops[finalStart+0].(provcontent.FileOp)
		assert.Equal(t, "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg", fopA.Path)
		assert.Equal(t, 0644, fopA.Mode)

		fopB := ops[finalStart+1].(provcontent.FileOp)
		assert.Equal(t, "/etc/cloud/cloud-init.disabled", fopB.Path)

		fopC := ops[finalStart+2].(provcontent.FileOp)
		assert.Equal(t, "/etc/systemd/system/snapd.seeded.service.d/override.conf", fopC.Path)

		fopD := ops[finalStart+3].(provcontent.FileOp)
		assert.Equal(t, "/etc/systemd/system/systemd-networkd-wait-online.service.d/override.conf", fopD.Path)

		chopE := ops[finalStart+4].(provcontent.ChrootOp)
		assert.Contains(t, chopE.Command, "cloud-init")

		chopF := ops[finalStart+5].(provcontent.ChrootOp)
		assert.Equal(t, "rm -rf /var/lib/apt/lists/* 2>/dev/null || true", chopF.Command)
	}

	tests := map[string]struct {
		osType    string
		wantCount int
		checkOS   func(t *testing.T, ops []provcontent.Operation)
	}{
		"ubuntu": {
			osType:    "ubuntu",
			wantCount: countUbuntuDebian,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				// ops[9-12]: OS-specific (ubuntu/debian)
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get clean")
				assert.Contains(t, ops[10].(provcontent.ChrootOp).Command, "rm -rf /var/cache/apt/archives/*.deb")
				assert.Contains(t, ops[11].(provcontent.ChrootOp).Command, "rm -rf /var/cache/debconf/*")
				assert.Contains(t, ops[12].(provcontent.ChrootOp).Command, "e2scrub_all.timer")

				// Verify alpine-specific commands do NOT appear
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apk")
				assert.NotContains(t, ops[12].(provcontent.ChrootOp).Command, "chronyd")
			},
		},
		"debian": {
			osType:    "debian",
			wantCount: countUbuntuDebian,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get clean")
			},
		},
		"Ubuntu": {
			osType:    "Ubuntu",
			wantCount: countUbuntuDebian,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get clean",
					"case-insensitive: 'Ubuntu' must match ubuntu branch")
			},
		},
		"alpine": {
			osType:    "alpine",
			wantCount: countAlpine,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				// ops[9-16]: OS-specific (alpine)
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "apk cache clean")
				assert.Contains(t, ops[10].(provcontent.ChrootOp).Command, "rm -rf /var/cache/apk/*")
				assert.Contains(t, ops[11].(provcontent.ChrootOp).Command, "denyinterfaces eth0")
				assert.Contains(t, ops[12].(provcontent.ChrootOp).Command, "rc-update add sshd")
				assert.Contains(t, ops[13].(provcontent.ChrootOp).Command, "rc_parallel")
				assert.Contains(t, ops[14].(provcontent.ChrootOp).Command, "rc-update del cloud-init")
				assert.Contains(t, ops[15].(provcontent.ChrootOp).Command, "chronyd")
				assert.Contains(t, ops[16].(provcontent.ChrootOp).Command, "sed -i '/ttyS0")

				// Verify debian-specific commands do NOT appear
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get")
			},
		},
		"ALPINE": {
			osType:    "ALPINE",
			wantCount: countAlpine,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "apk cache clean",
					"case-insensitive: 'ALPINE' must match alpine branch")
			},
		},
		"arch": {
			osType:    "arch",
			wantCount: countArch,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				// ops[9-18]: OS-specific (arch)
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "pacman -Sc")
				assert.Contains(t, ops[10].(provcontent.ChrootOp).Command, "rm -rf /var/cache/pacman/pkg/*")
				assert.Contains(t, ops[11].(provcontent.ChrootOp).Command, "pacman-key --init")
				assert.Contains(t, ops[12].(provcontent.ChrootOp).Command, "pacman-key --populate")
				assert.Contains(t, ops[13].(provcontent.ChrootOp).Command, "LANG=en_US.UTF-8")
				assert.Contains(
					t,
					ops[14].(provcontent.ChrootOp).Command,
					"ln -sf /dev/null /etc/systemd/system/pacman-init.service",
				)
				assert.Contains(
					t,
					ops[15].(provcontent.ChrootOp).Command,
					"ln -sf /dev/null /etc/systemd/system/systemd-firstboot.service",
				)
				assert.Contains(t, ops[16].(provcontent.ChrootOp).Command, "btrfs") // btrfs mkinitcpio
				assert.Contains(t, ops[17].(provcontent.ChrootOp).Command, "btrfs balance")
				assert.Contains(t, ops[18].(provcontent.ChrootOp).Command, "systemd-udev-settle")
			},
		},
		"archlinux": {
			osType:    "archlinux",
			wantCount: countArch,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "pacman -Sc",
					"'archlinux' must match arch branch")
			},
		},
		"manjaro": {
			osType:    "manjaro",
			wantCount: countArch,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "pacman -Sc",
					"'manjaro' must match arch branch")
			},
		},
		"Arch": {
			osType:    "Arch",
			wantCount: countArch,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "pacman -Sc",
					"case-insensitive: 'Arch' must match arch branch")
			},
		},
		"fedora": {
			osType:    "fedora",
			wantCount: countRedHat,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				// ops[9-10]: OS-specific (fedora)
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "dnf clean all 2>/dev/null || yum clean all")
				assert.Contains(t, ops[10].(provcontent.ChrootOp).Command, "rm -rf /var/cache/dnf/* /var/cache/yum/*")

				// Verify non-RedHat commands do NOT appear
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get")
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apk")
			},
		},
		"centos": {
			osType:    "centos",
			wantCount: countRedHat,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "dnf clean all",
					"'centos' must match Red Hat branch")
			},
		},
		"rhel": {
			osType:    "rhel",
			wantCount: countRedHat,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "dnf clean all",
					"'rhel' must match Red Hat branch")
			},
		},
		"rocky": {
			osType:    "rocky",
			wantCount: countRedHat,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "dnf clean all",
					"'rocky' must match Red Hat branch")
			},
		},
		"almalinux": {
			osType:    "almalinux",
			wantCount: countRedHat,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Contains(t, ops[9].(provcontent.ChrootOp).Command, "dnf clean all",
					"'almalinux' must match Red Hat branch")
			},
		},
		"unknown": {
			osType:    "unknown",
			wantCount: countUnknown,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				// ops[9]: Generic cache cleanup (default branch)
				assert.Equal(t, "rm -rf /var/cache/* 2>/dev/null || true", ops[9].(provcontent.ChrootOp).Command)

				// Verify no OS-specific commands appear
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apt-get")
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "apk")
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "pacman")
				assert.NotContains(t, ops[9].(provcontent.ChrootOp).Command, "dnf")
			},
		},
		"": {
			osType:    "",
			wantCount: countUnknown,
			checkOS: func(t *testing.T, ops []provcontent.Operation) {
				assert.Equal(t, "rm -rf /var/cache/* 2>/dev/null || true", ops[9].(provcontent.ChrootOp).Command,
					"empty string must fall through to default branch")
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var b provcontent.Builder
			got := b.BuildDeblobOps(tc.osType)

			// Check total count (hardcoded per OS type)
			assert.Equal(t, tc.wantCount, len(got),
				"BuildDeblobOps(%q) operation count mismatch", tc.osType)

			// Common base ops (indices 0-8)
			checkBaseOps(t, got)

			// OS-specific ops (indices 9 to len(got)-7)
			tc.checkOS(t, got)

			// Final common ops (last 6)
			checkFinalOps(t, got, tc.wantCount)
		})
	}
}

// ─── BuildVsockAgentOps ────────────────────────────────────────────────────
// Rationale: BuildVsockAgentOps generates the operations to embed the vsock
// guest agent into the root filesystem. Wrong paths, modes, or systemd content
// mean the agent won't start inside the VM, making Exec and Shell impossible.

func TestBuilder_BuildVsockAgentOps(t *testing.T) {
	t.Parallel()

	agentBinary := []byte("#!/bin/sh\necho mock-agent\n")
	port := 1024
	token := "test-auth-token"

	var b provcontent.Builder
	ops := b.BuildVsockAgentOps(agentBinary, port, token)

	require.Len(t, ops, 5, "BuildVsockAgentOps must return 5 operations")

	// ops[0]: Agent binary at /usr/bin/mvm-vsock-agent
	op0, ok0 := ops[0].(provcontent.FileOp)
	require.True(t, ok0, "ops[0] must be FileOp")
	assert.Equal(t, "/usr/bin/mvm-vsock-agent", op0.Path)
	assert.Equal(t, 0755, op0.Mode, "agent binary must be executable")
	if diff := cmp.Diff(agentBinary, op0.Data); diff != "" {
		t.Errorf("BuildVsockAgentOps() agent binary mismatch (-want +got):\n%s", diff)
	}

	// ops[1]: Auth token at /var/run/mvm-vsock-agent.token
	op1, ok1 := ops[1].(provcontent.FileOp)
	require.True(t, ok1, "ops[1] must be FileOp")
	assert.Equal(t, "/var/run/mvm-vsock-agent.token", op1.Path)
	assert.Equal(t, 0600, op1.Mode, "token file mode must be 0600")
	assert.Equal(t, token, string(op1.Data), "token file content must match")

	// ops[2]: Systemd unit at /etc/systemd/system/mvm-vsock-agent.service
	op2, ok2 := ops[2].(provcontent.FileOp)
	require.True(t, ok2, "ops[2] must be FileOp")
	assert.Equal(t, "/etc/systemd/system/mvm-vsock-agent.service", op2.Path)
	assert.Equal(t, 0644, op2.Mode)

	unitContent := string(op2.Data)
	assert.Contains(t, unitContent, "Description=MVM Guest Agent")
	assert.Contains(t, unitContent, "/usr/bin/mvm-vsock-agent -port 1024")
	assert.NotContains(t, unitContent, "-token", "ExecStart must not expose the token flag")
	assert.NotContains(t, unitContent, "After=", "systemd unit must not have After=")
	assert.NotContains(t, unitContent, "Requires=", "systemd unit must not have Requires=")

	// ops[3]: OpenRC init script at /etc/init.d/mvm-vsock-agent
	op3, ok3 := ops[3].(provcontent.FileOp)
	require.True(t, ok3, "ops[3] must be FileOp")
	assert.Equal(t, "/etc/init.d/mvm-vsock-agent", op3.Path)
	assert.Equal(t, 0755, op3.Mode, "init script must be executable")

	initContent := string(op3.Data)
	assert.Contains(t, initContent, "mvm-vsock-agent -port 1024")
	assert.Contains(t, initContent, "start)")
	assert.Contains(t, initContent, "stop)")

	// ops[4]: Enable command (ChrootOp)
	op4, ok4 := ops[4].(provcontent.ChrootOp)
	require.True(t, ok4, "ops[4] must be ChrootOp")
	assert.Contains(t, op4.Command, "systemctl enable mvm-vsock-agent")
	assert.Contains(t, op4.Command, "rc-update add mvm-vsock-agent default")
	assert.Contains(t, op4.Command, "unknown init system")
}
