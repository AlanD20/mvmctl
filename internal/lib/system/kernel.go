package system

import "syscall"

// KernelRelease returns the kernel release string matching Python's os.uname().release.
func KernelRelease() string {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return ""
	}
	// Convert [65]int8 to string, stopping at null byte.
	b := make([]byte, 0, len(uname.Release))
	for _, c := range uname.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
