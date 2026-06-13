package image

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// copyViaSendfile copies srcPath to dstPath using sendfile(2) for in-kernel
// zero-copy transfer. Works across different filesystem types.
func copyViaSendfile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open src for sendfile: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open dst for sendfile: %w", err)
	}
	defer dst.Close()

	const maxSend = 0x7ffff000
	var offset int64
	for {
		n, err := unix.Sendfile(int(dst.Fd()), int(src.Fd()), &offset, maxSend)
		if err != nil {
			return fmt.Errorf("sendfile at offset %d: %w", offset, err)
		}
		if n == 0 {
			break
		}
	}
	return nil
}

// copyViaIO copies srcPath to dstPath using io.Copy with a 32KB buffer.
// Always works as a userspace fallback.
func copyViaIO(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open src for io.Copy: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open dst for io.Copy: %w", err)
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}
