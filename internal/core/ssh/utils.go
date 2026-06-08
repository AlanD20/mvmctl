package ssh

import "fmt"

// buildSSHOpts builds the base SSH argument list shared by all SSH connections.
// connectTimeout is in seconds; 0 means no ConnectTimeout flag.
func buildSSHOpts(ip, user, keyPath string, connectTimeout int) []string {
	opts := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
	}
	if connectTimeout > 0 {
		opts = append(opts, "-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeout))
	}
	if keyPath != "" {
		opts = append(opts, "-i", keyPath)
	}
	opts = append(opts, fmt.Sprintf("%s@%s", user, ip))
	return opts
}
