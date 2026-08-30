package authorityfs

import "fmt"

const (
	vmKeyLength      = 32
	releaseKeyLength = 64
)

// VMKey identifies one canonical Jailer VM lock.
type VMKey struct {
	value string
}

// NewVMKey admits one 32-character lowercase hexadecimal VM identity.
func NewVMKey(value string) (VMKey, error) {
	if !isLowerHex(value, vmKeyLength) {
		return VMKey{}, fmt.Errorf("authorityfs: VM key must be %d lowercase hexadecimal characters", vmKeyLength)
	}
	return VMKey{value: value}, nil
}

// ReleaseKey identifies one canonical Jailer release lock.
type ReleaseKey struct {
	value string
}

// NewReleaseKey admits one 64-character lowercase hexadecimal release-slot digest.
func NewReleaseKey(value string) (ReleaseKey, error) {
	if !isLowerHex(value, releaseKeyLength) {
		return ReleaseKey{}, fmt.Errorf(
			"authorityfs: release key must be %d lowercase hexadecimal characters",
			releaseKeyLength,
		)
	}
	return ReleaseKey{value: value}, nil
}

func isLowerHex(value string, expectedLength int) bool {
	if len(value) != expectedLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
