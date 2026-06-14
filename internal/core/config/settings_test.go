package config_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/config"
)

// ─── GoTypeName ──────────────────────────────────────────────────────────────────────────
// Rationale: GoTypeName is used by InitSettings to store type metadata for config coercion.
// Wrong type names would cause Coerce to silently coerce to wrong target types or fail.

func TestGoTypeName(t *testing.T) {
	tests := map[string]struct {
		input any
		want  string
	}{
		// nil
		"nil_value": {input: nil, want: "nil"},

		// bool
		"bool_true":  {input: true, want: "bool"},
		"bool_false": {input: false, want: "bool"},

		// int types (all Go integer kinds collapse to "int")
		"int":    {input: 42, want: "int"},
		"int64":  {input: int64(42), want: "int"},
		"int32":  {input: int32(42), want: "int"},
		"int16":  {input: int16(42), want: "int"},
		"int8":   {input: int8(42), want: "int"},
		"uint":   {input: uint(42), want: "int"},
		"uint64": {input: uint64(42), want: "int"},
		"uint32": {input: uint32(42), want: "int"},
		"uint16": {input: uint16(42), want: "int"},
		"uint8":  {input: uint8(42), want: "int"},

		// float types
		"float64": {input: float64(3.14), want: "float"},
		"float32": {input: float32(3.14), want: "float"},

		// string
		"string":       {input: "hello", want: "string"},
		"empty_string": {input: "", want: "string"},

		// map
		"map":       {input: map[string]any{"key": "val"}, want: "map"},
		"empty_map": {input: map[string]any{}, want: "map"},

		// slice
		"slice":       {input: []any{1, "two"}, want: "slice"},
		"empty_slice": {input: []any{}, want: "slice"},

		// unknown type — %T format
		"unknown_type_returns_percent_t": {input: struct{}{}, want: "struct {}"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := config.GoTypeName(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GoTypeName() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── InitSettings ────────────────────────────────────────────────────────────────────────
// Rationale: InitSettings populates OverridableSettings from infra.OverridableDefaults.
// If it fails to run or produces wrong type mappings, all config coercion is broken.

func TestInitSettings(t *testing.T) {
	config.InitSettings()

	t.Run("overridable_settings_is_populated", func(t *testing.T) {
		require.NotNil(t, config.OverridableSettings)
		require.NotEmpty(t, config.OverridableSettings)
	})

	t.Run("defaults_vm_has_expected_categories", func(t *testing.T) {
		vmSettings, ok := config.OverridableSettings["defaults.vm"]
		require.True(t, ok, "expected category 'defaults.vm' to exist")
		require.NotEmpty(t, vmSettings)
	})

	t.Run("type_names_are_correct", func(t *testing.T) {
		vmSettings, ok := config.OverridableSettings["defaults.vm"]
		require.True(t, ok)

		assert.Equal(t, "int", vmSettings["vcpu_count"], "vcpu_count should be int")
		assert.Equal(t, "string", vmSettings["ssh_user"], "ssh_user should be string")
		assert.Equal(t, "bool", vmSettings["enable_logging"], "enable_logging should be bool")
		assert.Equal(t, "string", vmSettings["lsm_flags"], "lsm_flags should be string")
		assert.Equal(t, "bool", vmSettings["nested_virt"], "nested_virt should be bool")
	})

	t.Run("defaults_kernel_has_nil_type", func(t *testing.T) {
		kernelSettings, ok := config.OverridableSettings["defaults.kernel"]
		require.True(t, ok, "expected category 'defaults.kernel' to exist")
		assert.Equal(t, "nil", kernelSettings["build_jobs"], "build_jobs should be nil type")
	})

	t.Run("cli_category_exists", func(t *testing.T) {
		cliSettings, ok := config.OverridableSettings["cli"]
		require.True(t, ok, "expected category 'cli' to exist")
		require.NotEmpty(t, cliSettings)
	})

	t.Run("settings_category_exists", func(t *testing.T) {
		settingsCat, ok := config.OverridableSettings["settings"]
		require.True(t, ok, "expected category 'settings' to exist")
		require.NotEmpty(t, settingsCat)
	})
}

// ─── GetExpectedType ─────────────────────────────────────────────────────────────────────
// Rationale: GetExpectedType retrieves the expected type for a setting key.
// Returning wrong types (including empty string) would cause config coercion
// to target the wrong type or silently skip coercion.

func TestGetExpectedType(t *testing.T) {
	// Ensure OverridableSettings is initialized
	config.InitSettings()

	tests := map[string]struct {
		category string
		key      string
		want     string
	}{
		// Existing keys — check specific type strings
		"defaults_vm_vcpu_count":     {category: "defaults.vm", key: "vcpu_count", want: "int"},
		"defaults_vm_mem_size_mib":   {category: "defaults.vm", key: "mem_size_mib", want: "int"},
		"defaults_vm_ssh_user":       {category: "defaults.vm", key: "ssh_user", want: "string"},
		"defaults_vm_enable_logging": {category: "defaults.vm", key: "enable_logging", want: "bool"},
		"defaults_vm_nested_virt":    {category: "defaults.vm", key: "nested_virt", want: "bool"},
		"defaults_vm_dns_server":     {category: "defaults.vm", key: "dns_server", want: "string"},
		"defaults_vm_pci_enabled":    {category: "defaults.vm", key: "pci_enabled", want: "bool"},
		"defaults_network_subnet":    {category: "defaults.network", key: "subnet", want: "string"},
		"defaults_network_name":      {category: "defaults.network", key: "name", want: "string"},
		"defaults_image_list_limit":  {category: "defaults.image", key: "remote_list_limit", want: "int"},
		"cli_listing_style":          {category: "cli", key: "listing_style", want: "string"},
		"settings_firewall_backend":  {category: "settings", key: "firewall_backend", want: "string"},
		"defaults_kernel_build_jobs": {category: "defaults.kernel", key: "build_jobs", want: "nil"},

		// Non-existent category → empty string
		"nonexistent_category": {category: "nonexistent", key: "key", want: ""},

		// Non-existent key within existing category → empty string
		"defaults_vm_nonexistent_key": {category: "defaults.vm", key: "nonexistent_key", want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := config.GetExpectedType(tc.category, tc.key)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetExpectedType(%q, %q) mismatch (-want +got):\n%s", tc.category, tc.key, diff)
			}
		})
	}
}
