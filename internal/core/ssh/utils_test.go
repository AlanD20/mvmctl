package ssh

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// --- buildSSHOpts ---
// Rationale: buildSSHOpts constructs the base SSH argument list. It must
// always include security hardening flags and conditionally add the identity
// file and connect timeout. Empty IP or user may produce a syntactically
// invalid SSH command — we verify the output format is consistent.

func TestBuildSSHOpts(t *testing.T) {
	tests := map[string]struct {
		ip             string
		user           string
		keyPath        string
		connectTimeout int
		want           []string
	}{
		// Boundary cases
		"empty_ip": {
			ip:   "",
			user: "ubuntu",
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"ubuntu@",
			},
		},
		"empty_user": {
			ip:   "10.0.0.5",
			user: "",
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"@10.0.0.5",
			},
		},
		// Happy paths
		"basic without key or timeout": {
			ip:   "10.0.0.5",
			user: "ubuntu",
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"ubuntu@10.0.0.5",
			},
		},
		"with key path only": {
			ip:      "10.0.0.5",
			user:    "ubuntu",
			keyPath: "/home/user/.ssh/id_ed25519",
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"-i", "/home/user/.ssh/id_ed25519",
				"ubuntu@10.0.0.5",
			},
		},
		"with connect timeout only": {
			ip:             "10.0.0.5",
			user:           "ubuntu",
			connectTimeout: 10,
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"-o", "ConnectTimeout=10",
				"ubuntu@10.0.0.5",
			},
		},
		"with key and timeout": {
			ip:             "10.0.0.5",
			user:           "ubuntu",
			keyPath:        "/home/user/.ssh/id_ed25519",
			connectTimeout: 10,
			want: []string{
				"ssh", "-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "BatchMode=yes",
				"-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=2",
				"-o", "ServerAliveCountMax=3",
				"-o", "ConnectTimeout=10",
				"-i", "/home/user/.ssh/id_ed25519",
				"ubuntu@10.0.0.5",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := buildSSHOpts(tc.ip, tc.user, tc.keyPath, tc.connectTimeout)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildSSHOpts mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
