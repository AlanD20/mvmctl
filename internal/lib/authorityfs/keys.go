package authorityfs

import "fmt"

const (
	vmKeyLength      = 32
	imageKeyLength   = 64
	releaseKeyLength = 64
)

// VMKey identifies one canonical VM lock in a closed authority family.
type VMKey struct {
	value string
}

// ImageKey identifies one canonical LoopMount image lock.
type ImageKey struct {
	value string
}

// NewImageKey admits one 64-character lowercase hexadecimal image identity.
func NewImageKey(value string) (ImageKey, error) {
	if !isLowerHex(value, imageKeyLength) {
		return ImageKey{}, fmt.Errorf(
			"authorityfs: image key must be %d lowercase hexadecimal characters",
			imageKeyLength,
		)
	}
	return ImageKey{value: value}, nil
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
