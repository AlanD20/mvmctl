package model

import "mvmctl/pkg/errs"

// PCIVolumeIdentity identifies a guest disk by VMM-derived serial and capacity.
// Paths and PCI addresses are exclusively guest-discovered.
type PCIVolumeIdentity struct {
	Serial        string `json:"serial" db:"-"`
	ExpectedBytes uint64 `json:"expected_bytes" db:"-"`
}

// Validate admits Firecracker's decimal 20-byte disk identifier and sector capacity.
func (identity PCIVolumeIdentity) Validate() error {
	if identity.Serial == "" || len(identity.Serial) > 20 || identity.ExpectedBytes == 0 ||
		identity.ExpectedBytes%512 != 0 ||
		identity.ExpectedBytes > 1<<63-1 {
		return errs.New(errs.CodeValidationFailed, "guest volume identity is incomplete")
	}
	for _, digit := range identity.Serial {
		if digit < '0' || digit > '9' {
			return errs.New(errs.CodeValidationFailed, "guest volume serial is not decimal")
		}
	}
	return nil
}
