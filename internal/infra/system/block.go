package system

import (
	"context"
	"fmt"
	"strings"
)

// CopyWithDD copies src to dst using dd with optional sparse conversion.
func CopyWithDD(ctx context.Context, src, dst string, sparse bool) error {
	conv := "fsync"
	if sparse {
		conv = "sparse,fsync"
	}
	result := RunCmdCompat(ctx, []string{
		"dd", fmt.Sprintf("if=%s", src), fmt.Sprintf("of=%s", dst),
		"bs=1M", fmt.Sprintf("conv=%s", conv), "status=none",
	}, RunCmdOpts{Check: true, Capture: true})
	if result.Err != nil {
		combined := string(result.StdoutBytes) + string(result.StderrBytes)
		return fmt.Errorf("dd copy failed: %s", combined)
	}
	return nil
}

// CopyBytesDD copies bytes from src starting at skipBytes into dst using dd.
func CopyBytesDD(ctx context.Context, src, dst string, skipBytes, countBytes int64) error {
	ddArgs := []string{
		"dd",
		fmt.Sprintf("if=%s", src), fmt.Sprintf("of=%s", dst),
		"bs=1M", fmt.Sprintf("skip=%d", skipBytes),
		"iflag=skip_bytes,count_bytes", "conv=sparse,fsync", "status=none",
	}
	if countBytes > 0 {
		ddArgs = append(ddArgs, fmt.Sprintf("count=%d", countBytes))
	}
	opts := RunCmdOpts{Check: false, Capture: true, Text: true}
	result := RunCmdCompat(ctx, ddArgs, opts)
	if result.ExitCode != 0 {
		errMsg := strings.TrimSpace(result.Stderr)
		if errMsg == "" {
			errMsg = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("dd failed: %s", errMsg)
	}
	return nil
}

// DetectFilesystemType detects filesystem type using blkid.
func DetectFilesystemType(ctx context.Context, imagePath string) string {
	result := RunCmdCompat(
		ctx,
		[]string{"blkid", "-o", "value", "-s", "TYPE", imagePath},
		RunCmdOpts{Check: false, Capture: true},
	)
	if result.ExitCode == 0 {
		return strings.TrimSpace(result.Stdout)
	}
	return ""
}

// DetectFilesystemUUID detects the filesystem UUID using blkid.
func DetectFilesystemUUID(ctx context.Context, imagePath string) string {
	result := RunCmdCompat(
		ctx,
		[]string{"blkid", "-p", "-s", "UUID", "-o", "value", imagePath},
		RunCmdOpts{Check: false, Capture: true},
	)
	if result.ExitCode == 0 {
		return strings.TrimSpace(result.Stdout)
	}
	return ""
}
