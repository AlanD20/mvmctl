package config

import (
	"fmt"
	"regexp"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/validators"
)

// ResolveFn is a callable that resolves the effective value of a setting.
// It receives (key, category...) and returns the effective value:
//   - new_value if the key matches the one being set and category matches
//   - current DB/default for any other key/category
//
// Matches Python: ResolveFn = Callable[..., Any]
type ResolveFn func(otherKey string, otherCategory ...string) (any, error)

// Constraint receives (key_being_set, resolve_fn) and returns an error
// if the pending change would create an invalid state.
// Matches Python: Constraint = Callable[[str, ResolveFn], None]
type Constraint func(key string, resolve ResolveFn) error

// ConstraintRegistry registers and looks up cross-key validation constraints.
// Matches Python ConstraintRegistry exactly.
type ConstraintRegistry struct {
	constraints map[[2]string][]Constraint // (category, key) -> constraints
}

// NewConstraintRegistry creates an empty ConstraintRegistry.
func NewConstraintRegistry() *ConstraintRegistry {
	return &ConstraintRegistry{
		constraints: make(map[[2]string][]Constraint),
	}
}

// Register registers a constraint that fires when any of the given keys in
// the given category is set. Matches Python register(category, keys, constraint).
func (r *ConstraintRegistry) Register(category string, keys []string, constraint Constraint) {
	for _, key := range keys {
		pair := [2]string{category, key}
		r.constraints[pair] = append(r.constraints[pair], constraint)
	}
}

// Get returns constraints for a (category, key) pair.
// Matches Python: constraints.get((category, key), []).
func (r *ConstraintRegistry) Get(category, key string) []Constraint {
	pair := [2]string{category, key}
	return r.constraints[pair]
}

// ---------------------------------------------------------------------------
// Built-in constraints (matching Python _constraints.py)
// ---------------------------------------------------------------------------

// validateNoCloudPortRange ensures nocloud_port_range_end > nocloud_port_range_start.
// Matches Python _validate_nocloud_port_range.
func validateNoCloudPortRange(key string, resolve ResolveFn) error {
	startRaw, err := resolve("nocloud_port_range_start")
	if err != nil {
		return err
	}
	endRaw, err := resolve("nocloud_port_range_end")
	if err != nil {
		return err
	}

	start, err := validators.ToInt(startRaw)
	if err != nil {
		return err
	}
	end, err := validators.ToInt(endRaw)
	if err != nil {
		return err
	}

	if end <= start {
		return &errs.DomainError{
			Code:    errs.CodeConfigError,
			Message: fmt.Sprintf("nocloud_port_range_end (%d) must be greater than nocloud_port_range_start (%d)", end, start),
			Op:      "constraint",
			Class:   errs.ClassValidation,
		}
	}
	return nil
}

// validateMACPrefix ensures guest_mac_prefix is a valid 2-byte hex MAC prefix.
// Matches Python _validate_mac_prefix.
var macPrefixRE = regexp.MustCompile(`^[0-9a-fA-F]{2}:[0-9a-fA-F]{2}$`)

func validateMACPrefix(key string, resolve ResolveFn) error {
	prefixRaw, err := resolve("guest_mac_prefix")
	if err != nil {
		return err
	}

	prefix := fmt.Sprintf("%v", prefixRaw)
	if !macPrefixRE.MatchString(prefix) {
		return &errs.DomainError{
			Code:    errs.CodeConfigError,
			Message: fmt.Sprintf("Invalid MAC prefix '%s'. Must be two hex bytes separated by a colon (e.g. '02:FC').", prefix),
			Op:      "constraint",
			Class:   errs.ClassValidation,
		}
	}
	return nil
}

// defaultConstraints is the package-level default registry with built-in constraints
// auto-registered via InitConstraints(), matching Python's module-level singleton behavior.
// Call InitConstraints() before accessing.
var defaultConstraints *ConstraintRegistry

// InitConstraints initializes the package-level defaultConstraints singleton and
// registers all built-in constraints. Replaces the former init() — must be called
// explicitly from app startup.
// TODO: call InitConstraints() from internal/app/app.go after InitSettings().
func InitConstraints() {
	defaultConstraints = NewConstraintRegistry()
	RegisterBuiltinConstraints(defaultConstraints)
}

// RegisterBuiltinConstraints registers all built-in constraints on the given registry.
// Called during app initialization to match Python's module-level registration.
func RegisterBuiltinConstraints(r *ConstraintRegistry) {
	r.Register("defaults.cloudinit",
		[]string{"nocloud_port_range_start", "nocloud_port_range_end"},
		validateNoCloudPortRange,
	)

	r.Register("defaults.vm",
		[]string{"guest_mac_prefix"},
		validateMACPrefix,
	)
}
