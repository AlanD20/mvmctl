package ssh

import (
	"fmt"
	"os"
	"path/filepath"
)

// getDirectorySize returns the approximate total size of a directory
// by summing file sizes. Matches Python's CPService._get_directory_size().
func getDirectorySize(path string) int64 {
	var total int64
	filepath.Walk(path, func(fp string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files, matching Python's OSError pass
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

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
