package cloudinit

import (
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// CloudInitModeResolved matches Python's CloudInitModeResolved dataclass.
type CloudInitModeResolved struct {
	Mode    model.CloudInitMode
	ISOPath *string
}

// ResolveMode resolves a cloud-init mode from raw CLI input.
// Matches Python's VMCreateRequest._resolve_cloud_init_mode().
func ResolveMode(mode *string, isoPath *string) (CloudInitModeResolved, error) {
	// Off is default cloud-init mode
	result := CloudInitModeResolved{Mode: model.CloudInitModeOFF, ISOPath: nil}

	if mode == nil {
		return result, nil
	}

	modeLower := strings.ToLower(*mode)
	modeVal := model.CloudInitMode(modeLower)
	if !IsValidMode(modeVal) {
		return CloudInitModeResolved{}, errs.New(errs.CodeCloudInitProvisionFailed,
			fmt.Sprintf("Invalid --cloud-init-mode '%s'. Valid modes: inject, iso, off, net", *mode),
			errs.WithClass(errs.ClassValidation))
	}

	switch modeVal {
	case model.CloudInitModeISO:
		if isoPath != nil && *isoPath != "" {
			if _, err := os.Stat(*isoPath); os.IsNotExist(err) {
				return CloudInitModeResolved{}, errs.New(errs.CodeCloudInitProvisionFailed,
					fmt.Sprintf("Cloud-init ISO not found: %s", *isoPath),
					errs.WithClass(errs.ClassValidation))
			}
			result = CloudInitModeResolved{Mode: model.CloudInitModeISO, ISOPath: isoPath}
		} else {
			// Default: ISO will be created during provisioning
			result = CloudInitModeResolved{Mode: model.CloudInitModeISO, ISOPath: nil}
		}
	case model.CloudInitModeNET:
		result = CloudInitModeResolved{Mode: model.CloudInitModeNET, ISOPath: nil}
	case model.CloudInitModeINJECT:
		result = CloudInitModeResolved{Mode: model.CloudInitModeINJECT, ISOPath: nil}
	case model.CloudInitModeOFF:
		result = CloudInitModeResolved{Mode: model.CloudInitModeOFF, ISOPath: nil}
	}

	return result, nil
}

// ValidModes returns all valid cloud-init modes.
func ValidModes() []model.CloudInitMode {
	return []model.CloudInitMode{
		model.CloudInitModeOFF,
		model.CloudInitModeINJECT,
		model.CloudInitModeNET,
		model.CloudInitModeISO,
	}
}

// validateTemplateData checks all required TemplateData fields are non-empty,
// mimicking Jinja2's StrictUndefined behavior in the Python implementation.
// If any required field is empty, it returns an error with the field name.
func validateTemplateData(data TemplateData) error {
	required := []struct {
		name  string
		value string
	}{
		{"VMName", data.VMName},
		{"User", data.User},
		{"GuestIP", data.GuestIP},
		{"IPv4Gateway", data.IPv4Gateway},
		{"PasswordHash", data.PasswordHash},
	}
	for _, r := range required {
		if r.value == "" {
			return fmt.Errorf("cloud-init template requires non-empty field: .%s", r.name)
		}
	}
	return nil
}

// IsValidMode returns true if the given mode is a valid cloud-init mode.
func IsValidMode(mode model.CloudInitMode) bool {
	switch mode {
	case model.CloudInitModeOFF, model.CloudInitModeINJECT, model.CloudInitModeNET, model.CloudInitModeISO:
		return true
	default:
		return false
	}
}

// validateCloudinitConfig checks a custom cloud-init config for dangerous directives.
func validateCloudinitConfig(cfg map[string]any) error {
	var found []string
	for directive := range dangerousCloudInitDirectives {
		if _, ok := cfg[directive]; ok {
			found = append(found, directive)
		}
	}

	if len(found) > 0 {
		details := make([]string, 0, len(found))
		for _, d := range found {
			details = append(details, fmt.Sprintf("%s: %s", d, dangerousCloudInitDirectives[d]))
		}
		return errs.New(errs.CodeCloudInitProvisionFailed,
			fmt.Sprintf(
				"custom cloud-init config contains blocked directive(s): %s. %s",
				strings.Join(found, ", "),
				strings.Join(details, "; "),
			),
		)
	}

	return nil
}
