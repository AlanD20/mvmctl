package loopmount

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteRootfsHostsConvergesToOneManagedHostname(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		current  []byte
		hostname string
		want     []byte
		wantErr  string
	}{
		"rejects_overlong_line": {
			current:  []byte("127.0.0.1 " + strings.Repeat("x", maxRootfsProvisionLineBytes+1) + "\n"),
			hostname: "vm-a",
			wantErr:  "line",
		},
		"preserves_unrelated_and_replaces_every_127_0_1_1_row": {
			current:  []byte("127.0.0.1 localhost\n127.0.1.1 old alias\n# keep\n127.0.1.1 stale\n"),
			hostname: "vm-a",
			want:     []byte("127.0.0.1 localhost\n# keep\n127.0.1.1\tvm-a\n"),
		},
		"normalizes_missing_final_newline": {
			current:  []byte("127.0.0.1 localhost"),
			hostname: "vm-b",
			want:     []byte("127.0.0.1 localhost\n127.0.1.1\tvm-b\n"),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := rewriteRootfsHosts(tc.current, tc.hostname)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("hosts rewrite mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRewriteRootfsFstabAppliesOnlyClosedFirecrackerPolicy(t *testing.T) {
	t.Parallel()

	current := []byte("# keep\n" +
		"PARTUUID=abcd / ext4 defaults 0 1\n" +
		"UUID=beef /data ext4 defaults 0 2\n" +
		"/dev/vda / ext4 defaults 0 1\n" +
		"/dev/vda1 /boot ext4 defaults 0 2\n" +
		"tmpfs /tmp tmpfs defaults 0 0\n" +
		"/dev/vdb none swap sw 0 0\n")
	want := []byte("# keep\n" +
		"# mvmctl: PARTUUID=abcd / ext4 defaults 0 1\n" +
		"# mvmctl: UUID=beef /data ext4 defaults 0 2\n" +
		"/dev/vda / ext4 noatime,defaults 0 1\n" +
		"# mvmctl: /dev/vda1 /boot ext4 defaults 0 2\n" +
		"tmpfs /tmp tmpfs defaults 0 0\n" +
		"# mvmctl: /dev/vdb none swap sw 0 0\n")

	got, err := rewriteRootfsFstab(current)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fstab rewrite mismatch (-want +got):\n%s", diff)
	}

	replayed, err := rewriteRootfsFstab(got)
	require.NoError(t, err)
	if diff := cmp.Diff(want, replayed); diff != "" {
		t.Errorf("fstab replay mismatch (-want +got):\n%s", diff)
	}
}

func TestParseRootfsGuestAccountRequiresOneCanonicalExistingAccount(t *testing.T) {
	t.Parallel()

	passwd := []byte("root:x:0:0:root:/root:/bin/sh\n" +
		"alice:x:1000:1001:Alice:/home/alice:/bin/bash\n")
	tests := map[string]struct {
		username string
		passwd   []byte
		want     rootfsGuestAccount
		wantErr  string
	}{
		"rejects_duplicate": {
			username: "alice",
			passwd:   append(append([]byte(nil), passwd...), []byte("alice:x:1002:1002::/srv/alice:/bin/sh\n")...),
			wantErr:  "duplicate",
		},
		"rejects_relative_home": {
			username: "alice",
			passwd:   []byte("alice:x:1000:1000::home/alice:/bin/sh\n"),
			wantErr:  "home",
		},
		"rejects_missing": {username: "bob", passwd: passwd, wantErr: "does not exist"},
		"accepts_existing": {
			username: "alice",
			passwd:   passwd,
			want:     rootfsGuestAccount{name: "alice", uid: 1000, gid: 1001, home: "home/alice"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseRootfsGuestAccount(tc.passwd, tc.username)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(rootfsGuestAccount{})); diff != "" {
				t.Errorf("guest account mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRootfsCapacityConvergedAllowsOnlySubBlockTail(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		capacity uint64
		target   uint64
		block    uint64
		want     bool
	}{
		"rejects_zero_block":       {capacity: 4096, target: 4096, block: 0, want: false},
		"rejects_over_target":      {capacity: 8192, target: 4096, block: 4096, want: false},
		"rejects_full_block_short": {capacity: 4096, target: 8192, block: 4096, want: false},
		"accepts_exact":            {capacity: 8192, target: 8192, block: 4096, want: true},
		"accepts_rounding_tail":    {capacity: 8192, target: 8193, block: 4096, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, rootfsCapacityConverged(tc.capacity, tc.target, tc.block))
		})
	}
}
