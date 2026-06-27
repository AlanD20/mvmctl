package config

import (
	"fmt"
	"regexp"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// ResolveFn is a callable that resolves the effective value of a setting.
// It receives (key, category...) and returns the effective value:
// - new_value if the key matches the one being set and category matches
// - current DB/default for any other key/category
type ResolveFn func(otherKey string, otherCategory ...string) (any, error)

// Constraint receives (key_being_set, resolve_fn) and returns an error
// if the pending change would create an invalid state.
type Constraint func(key string, resolve ResolveFn) error

// TransformFunc transforms a config value before it is stored.
// Receives (key, value_being_set) and returns the transformed value or an error.
type TransformFunc func(key string, value any) (any, error)

// ConstraintRegistry registers and looks up cross-key validation constraints.
type ConstraintRegistry struct {
	constraints map[[2]string][]Constraint    // (category, key) -> constraints
	transforms  map[[2]string][]TransformFunc // (category, key) -> transforms
}

// NewConstraintRegistry creates an empty ConstraintRegistry.
func NewConstraintRegistry() *ConstraintRegistry {
	return &ConstraintRegistry{
		constraints: make(map[[2]string][]Constraint),
		transforms:  make(map[[2]string][]TransformFunc),
	}
}

// Register registers a constraint that fires when any of the given keys in
// the given category is set.
func (r *ConstraintRegistry) Register(category string, keys []string, constraint Constraint) {
	for _, key := range keys {
		pair := [2]string{category, key}
		r.constraints[pair] = append(r.constraints[pair], constraint)
	}
}

// Get returns constraints for a (category, key) pair.
func (r *ConstraintRegistry) Get(category, key string) []Constraint {
	pair := [2]string{category, key}
	return r.constraints[pair]
}

// RegisterTransform registers a transform that fires when any of the given
// keys in the given category is set.
func (r *ConstraintRegistry) RegisterTransform(category string, keys []string, transform TransformFunc) {
	for _, key := range keys {
		pair := [2]string{category, key}
		r.transforms[pair] = append(r.transforms[pair], transform)
	}
}

// GetTransforms returns transforms for a (category, key) pair.
func (r *ConstraintRegistry) GetTransforms(category, key string) []TransformFunc {
	pair := [2]string{category, key}
	return r.transforms[pair]
}

// Built-in constraints

// validateNoCloudPortRange ensures nocloud_port_range_end > nocloud_port_range_start.
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
		return errs.New(errs.CodeConfigError,
			fmt.Sprintf("nocloud_port_range_end (%d) must be greater than nocloud_port_range_start (%d)", end, start))
	}
	return nil
}

// validateMACPrefix ensures guest_mac_prefix is a valid 2-byte hex MAC prefix.
var macPrefixRE = regexp.MustCompile(`^[0-9a-fA-F]{2}:[0-9a-fA-F]{2}$`)

func validateMACPrefix(key string, resolve ResolveFn) error {
	prefixRaw, err := resolve("guest_mac_prefix")
	if err != nil {
		return err
	}

	prefix := fmt.Sprintf("%v", prefixRaw)
	if !macPrefixRE.MatchString(prefix) {
		return errs.New(errs.CodeConfigError,
			fmt.Sprintf("Invalid MAC prefix '%s'. Must be two hex bytes separated by a colon (e.g. '02:FC').", prefix))
	}
	return nil
}

// NormalizeCacheType normalizes cache_type values: first letter uppercase, rest lowercase.
func NormalizeCacheType(key string, value any) (any, error) {
	if value == nil {
		return model.CacheTypeUnsafe, nil
	}
	s := fmt.Sprintf("%v", value)
	if s == "" {
		return model.CacheTypeUnsafe, nil
	}
	norm := strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
	if norm != model.CacheTypeWriteback && norm != model.CacheTypeUnsafe {
		return nil, errs.New(errs.CodeConfigError,
			fmt.Sprintf("Invalid cache type '%s'. Must be 'Unsafe' or 'Writeback'.", s))
	}
	return norm, nil
}

// RegisterBuiltinConstraints registers all built-in constraints on the given registry.
// Called during app initialization.
func RegisterBuiltinConstraints(r *ConstraintRegistry) {
	r.Register("defaults.cloudinit",
		[]string{"nocloud_port_range_start", "nocloud_port_range_end"},
		validateNoCloudPortRange,
	)

	r.Register("defaults.vm",
		[]string{"guest_mac_prefix"},
		validateMACPrefix,
	)

	r.RegisterTransform("defaults.volume", []string{"cache_type"}, NormalizeCacheType)
}
