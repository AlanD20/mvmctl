package cloudinit

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ─── ResolveMode ────────────────────────────────────────────────────────────
// Rationale: Resolves raw CLI input to CloudInitModeResolved.

func TestResolveMode(t *testing.T) {
	t.Run("nil_mode_returns_off_nil_iso", func(t *testing.T) {
		result, err := ResolveMode(nil, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeOFF, result.Mode)
		assert.Nil(t, result.ISOPath)
	})

	t.Run("off_mode", func(t *testing.T) {
		mode := strPtr("off")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeOFF, result.Mode)
		assert.Nil(t, result.ISOPath)
	})

	t.Run("inject_mode", func(t *testing.T) {
		mode := strPtr("inject")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeINJECT, result.Mode)
		assert.Nil(t, result.ISOPath)
	})

	t.Run("net_mode", func(t *testing.T) {
		mode := strPtr("net")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeNET, result.Mode)
		assert.Nil(t, result.ISOPath)
	})

	t.Run("iso_without_isoPath_returns_iso_nil", func(t *testing.T) {
		mode := strPtr("iso")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeISO, result.Mode)
		assert.Nil(t, result.ISOPath)
	})

	t.Run("iso_with_valid_isoPath_returns_iso_with_path", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "cloudinit-*.iso")
		require.NoError(t, err)
		f.Close()
		isoPath := f.Name()
		mode := strPtr("iso")
		result, err := ResolveMode(mode, &isoPath)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeISO, result.Mode)
		require.NotNil(t, result.ISOPath)
		assert.Equal(t, isoPath, *result.ISOPath)
	})

	t.Run("iso_with_nonexistent_iso_path_returns_error", func(t *testing.T) {
		isoPath := t.TempDir() + "/nonexistent.iso"
		mode := strPtr("iso")
		_, err := ResolveMode(mode, &isoPath)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeCloudInitProvisionFailed, de.Code)
	})

	t.Run("invalid_mode_returns_error", func(t *testing.T) {
		mode := strPtr("invalid")
		_, err := ResolveMode(mode, nil)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeCloudInitProvisionFailed, de.Code)
	})

	t.Run("case_insensitive_OFF", func(t *testing.T) {
		mode := strPtr("OFF")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeOFF, result.Mode)
	})

	t.Run("case_insensitive_Off", func(t *testing.T) {
		mode := strPtr("Off")
		result, err := ResolveMode(mode, nil)
		require.NoError(t, err)
		assert.Equal(t, model.CloudInitModeOFF, result.Mode)
	})
}

func strPtr(s string) *string { return &s }

// ─── IsValidMode ────────────────────────────────────────────────────────────
// Rationale: Returns true for valid CloudInitMode values.

func TestIsValidMode(t *testing.T) {
	tests := []struct {
		name string
		mode model.CloudInitMode
		want bool
	}{
		{name: "off", mode: model.CloudInitModeOFF, want: true},
		{name: "inject", mode: model.CloudInitModeINJECT, want: true},
		{name: "net", mode: model.CloudInitModeNET, want: true},
		{name: "iso", mode: model.CloudInitModeISO, want: true},
		{name: "empty", mode: model.CloudInitMode(""), want: false},
		{name: "invalid", mode: model.CloudInitMode("invalid"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValidMode(tc.mode)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── validateTemplateData ───────────────────────────────────────────────────
// Rationale: Validates all required TemplateData fields are non-empty.

func TestValidateTemplateData(t *testing.T) {
	validData := TemplateData{
		VMName:       "test-vm",
		User:         "ubuntu",
		GuestIP:      "10.0.0.1",
		IPv4Gateway:  "10.0.0.254",
		PasswordHash: "$6$abc",
	}

	t.Run("all_fields_set_returns_nil", func(t *testing.T) {
		err := validateTemplateData(validData)
		assert.NoError(t, err)
	})

	t.Run("VMName_empty_returns_error", func(t *testing.T) {
		d := validData
		d.VMName = ""
		err := validateTemplateData(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".VMName")
	})

	t.Run("User_empty_returns_error", func(t *testing.T) {
		d := validData
		d.User = ""
		err := validateTemplateData(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".User")
	})

	t.Run("GuestIP_empty_returns_error", func(t *testing.T) {
		d := validData
		d.GuestIP = ""
		err := validateTemplateData(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".GuestIP")
	})

	t.Run("IPv4Gateway_empty_returns_error", func(t *testing.T) {
		d := validData
		d.IPv4Gateway = ""
		err := validateTemplateData(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".IPv4Gateway")
	})

	t.Run("PasswordHash_empty_returns_error", func(t *testing.T) {
		d := validData
		d.PasswordHash = ""
		err := validateTemplateData(d)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".PasswordHash")
	})
}

// ─── validateCloudinitConfig ────────────────────────────────────────────────
// Rationale: Rejects custom cloud-init configs with dangerous directives.

func TestValidateCloudinitConfig(t *testing.T) {
	t.Run("empty_config_returns_nil", func(t *testing.T) {
		err := validateCloudinitConfig(nil)
		assert.NoError(t, err)
	})

	t.Run("no_dangerous_directives_returns_nil", func(t *testing.T) {
		cfg := map[string]any{"ssh_authorized_keys": []string{"ssh-rsa AAA"}}
		err := validateCloudinitConfig(cfg)
		assert.NoError(t, err)
	})

	t.Run("write_files_returns_error", func(t *testing.T) {
		cfg := map[string]any{"write_files": "anything"}
		err := validateCloudinitConfig(cfg)
		require.Error(t, err)
		de, ok := errs.AsType[*errs.DomainError](err)
		require.True(t, ok)
		assert.Equal(t, errs.CodeCloudInitProvisionFailed, de.Code)
		assert.Contains(t, err.Error(), "write_files")
	})

	t.Run("multiple_dangerous_directives_mentions_all", func(t *testing.T) {
		cfg := map[string]any{
			"write_files": "anything",
			"runcmd":      "anything",
			"packages":    "anything",
		}
		err := validateCloudinitConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write_files")
		assert.Contains(t, err.Error(), "runcmd")
		assert.Contains(t, err.Error(), "packages")
	})
}
