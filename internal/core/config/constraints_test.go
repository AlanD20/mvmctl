package config_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/config"
	"mvmctl/pkg/errs"
)

// --- Helpers ---

// resolveMap returns a ResolveFn backed by the given map.
// If the key exists in the map, returns the value. Otherwise returns an error.
func resolveMap(m map[string]any) config.ResolveFn {
	return func(otherKey string, _ ...string) (any, error) {
		v, ok := m[otherKey]
		if !ok {
			return nil, fmt.Errorf("key %q not found", otherKey)
		}
		return v, nil
	}
}

// resolveErr returns a ResolveFn that always errors for a specific key.
func resolveErr(forKey string) config.ResolveFn {
	return func(otherKey string, _ ...string) (any, error) {
		if otherKey == forKey {
			return nil, fmt.Errorf("resolve error for %s", forKey)
		}
		return 42, nil // fallback
	}
}

// assertConfigError checks that err is a DomainError with CodeConfigError.
func assertConfigError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var de *errs.DomainError
	if errors.As(err, &de) {
		assert.Equal(t, errs.CodeConfigError, de.Code,
			"expected CodeConfigError, got %s", de.Code)
	} else {
		t.Errorf("expected *errs.DomainError, got %T", err)
	}
}

// --- ConstraintRegistry ---
// Rationale: Registry stores constraints keyed by (category, key). Register
// must append to existing constraints, and Get returns a copy (or the slice).

func TestConstraintRegistry(t *testing.T) {
	t.Run("empty_registry_returns_nil", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		got := r.Get("defaults.vm", "guest_mac_prefix")
		assert.Empty(t, got)
	})

	t.Run("register_and_get_single", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		called := false
		fn := func(key string, resolve config.ResolveFn) error {
			called = true
			return nil
		}
		r.Register("defaults.vm", []string{"guest_mac_prefix"}, fn)

		got := r.Get("defaults.vm", "guest_mac_prefix")
		require.Len(t, got, 1)

		// Execute the constraint to verify it's our function
		err := got[0]("guest_mac_prefix", nil)
		require.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("register_multiple_keys_same_constraint", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		count := 0
		fn := func(key string, resolve config.ResolveFn) error {
			count++
			return nil
		}
		r.Register("defaults.cloudinit", []string{"nocloud_port_range_start", "nocloud_port_range_end"}, fn)

		got1 := r.Get("defaults.cloudinit", "nocloud_port_range_start")
		got2 := r.Get("defaults.cloudinit", "nocloud_port_range_end")
		assert.Len(t, got1, 1)
		assert.Len(t, got2, 1)
	})

	t.Run("multiple_constraints_same_key", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		r.Register("defaults.vm", []string{"guest_mac_prefix"},
			func(key string, resolve config.ResolveFn) error { return nil })
		r.Register("defaults.vm", []string{"guest_mac_prefix"},
			func(key string, resolve config.ResolveFn) error { return nil })

		got := r.Get("defaults.vm", "guest_mac_prefix")
		assert.Len(t, got, 2, "second Register must append, not replace")
	})

	t.Run("different_category_no_cross_pollution", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		r.Register("defaults.vm", []string{"guest_mac_prefix"},
			func(key string, resolve config.ResolveFn) error { return nil })
		r.Register("defaults.cloudinit", []string{"nocloud_port_range_start"},
			func(key string, resolve config.ResolveFn) error { return nil })

		vm := r.Get("defaults.vm", "guest_mac_prefix")
		cloud := r.Get("defaults.cloudinit", "nocloud_port_range_start")
		assert.Len(t, vm, 1)
		assert.Len(t, cloud, 1)

		// Unregistered (category, key) should return empty
		unreg := r.Get("defaults.vm", "nonexistent")
		assert.Empty(t, unreg)
	})
}

// --- validateNoCloudPortRange ---
// Rationale: end > start is valid. Any other relationship (end == start,
// end < start) must error with a specific message.

func TestValidateNoCloudPortRange(t *testing.T) {
	t.Run("valid_range", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		resolve := resolveMap(map[string]any{
			"nocloud_port_range_start": 32768,
			"nocloud_port_range_end":   33768,
		})
		err := constraints[0]("nocloud_port_range_end", resolve)
		assert.NoError(t, err)
	})

	t.Run("end_equals_start_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		resolve := resolveMap(map[string]any{
			"nocloud_port_range_start": 32768,
			"nocloud_port_range_end":   32768,
		})
		err := constraints[0]("nocloud_port_range_end", resolve)
		assertConfigError(t, err)
		assert.Contains(t, err.Error(), "must be greater than")
	})

	t.Run("end_less_than_start_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		resolve := resolveMap(map[string]any{
			"nocloud_port_range_start": 32768,
			"nocloud_port_range_end":   31768,
		})
		err := constraints[0]("nocloud_port_range_end", resolve)
		assertConfigError(t, err)
		assert.Contains(t, err.Error(), "must be greater than")
	})

	t.Run("resolve_error_for_start_propagated", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		err := constraints[0]("nocloud_port_range_end", resolveErr("nocloud_port_range_start"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve error for nocloud_port_range_start")
	})

	t.Run("resolve_error_for_end_propagated", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		err := constraints[0]("nocloud_port_range_end", resolveErr("nocloud_port_range_end"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve error for nocloud_port_range_end")
	})

	t.Run("start_is_not_numeric", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		resolve := resolveMap(map[string]any{
			"nocloud_port_range_start": "not-a-number",
			"nocloud_port_range_end":   33768,
		})
		err := constraints[0]("nocloud_port_range_end", resolve)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected numeric value")
	})

	t.Run("end_is_not_numeric", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")

		resolve := resolveMap(map[string]any{
			"nocloud_port_range_start": 32768,
			"nocloud_port_range_end":   "not-a-number",
		})
		err := constraints[0]("nocloud_port_range_end", resolve)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected numeric value")
	})
}

// --- validateMACPrefix ---
// Rationale: Must accept exactly two hex bytes separated by a colon.
// Reject anything else — wrong length, wrong separator, extra characters.

func TestValidateMACPrefix(t *testing.T) {
	t.Run("valid_prefix", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "02:FC"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assert.NoError(t, err)
	})

	t.Run("valid_lowercase", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "aa:bb"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assert.NoError(t, err)
	})

	t.Run("valid_zeros", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "00:00"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assert.NoError(t, err)
	})

	t.Run("not_hex_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "xyz"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
		assert.Contains(t, err.Error(), "Invalid MAC prefix")
	})

	t.Run("three_bytes_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "02:FC:00"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
	})

	t.Run("wrong_separator_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "02-fc"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
	})

	t.Run("trailing_colon_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": "02:FC:"})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
	})

	t.Run("empty_string_errors", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		resolve := resolveMap(map[string]any{"guest_mac_prefix": ""})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
	})

	t.Run("resolve_error_propagated", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		err := constraints[0]("guest_mac_prefix", resolveErr("guest_mac_prefix"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve error for guest_mac_prefix")
	})

	t.Run("non_string_value_handled", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)
		constraints := r.Get("defaults.vm", "guest_mac_prefix")

		// ResolveFn might return non-string; validateMACPrefix converts via fmt.Sprintf
		resolve := resolveMap(map[string]any{"guest_mac_prefix": 42})
		err := constraints[0]("guest_mac_prefix", resolve)
		assertConfigError(t, err)
	})
}

// --- RegisterBuiltinConstraints ---
// Rationale: Must register both constraints in the correct categories and keys.

func TestRegisterBuiltinConstraints(t *testing.T) {
	t.Run("registers_nocloud_port_range", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)

		startConstraints := r.Get("defaults.cloudinit", "nocloud_port_range_start")
		endConstraints := r.Get("defaults.cloudinit", "nocloud_port_range_end")
		assert.Len(t, startConstraints, 1, "nocloud_port_range_start should have 1 constraint")
		assert.Len(t, endConstraints, 1, "nocloud_port_range_end should have 1 constraint")

		// They should be the same constraint function (same pointer)
		assert.Equal(t, fmt.Sprintf("%p", startConstraints[0]), fmt.Sprintf("%p", endConstraints[0]),
			"both keys should share the same constraint function")
	})

	t.Run("registers_guest_mac_prefix", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)

		constraints := r.Get("defaults.vm", "guest_mac_prefix")
		assert.Len(t, constraints, 1, "guest_mac_prefix should have 1 constraint")
	})

	t.Run("unregistered_key_has_no_constraints", func(t *testing.T) {
		r := config.NewConstraintRegistry()
		config.RegisterBuiltinConstraints(r)

		unreg := r.Get("defaults.vm", "nonexistent")
		assert.Empty(t, unreg)

		unreg2 := r.Get("defaults.cloudinit", "nonexistent")
		assert.Empty(t, unreg2)
	})
}
