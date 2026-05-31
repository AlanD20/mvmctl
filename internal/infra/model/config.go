package model

// ── Setting ──

// Setting represents a row in the user_settings table.
// Value is stored as JSON in the DB and parsed on read.
type Setting struct {
	Category  string `json:"category" db:"category"`
	Key       string `json:"key" db:"key"`
	Value     string `json:"value" db:"value"`           // Raw JSON string from DB; callers json.Unmarshal to get the actual value
	UpdatedAt string `json:"updated_at" db:"updated_at"` // ISO timestamp from CURRENT_TIMESTAMP
}

// ── SettingInfo ──

// SettingInfo represents a single key's metadata for listing.
type SettingInfo struct {
	Type     string `json:"type"`     // Go type name: "string", "int", "bool", "map", "nil"
	Default  any    `json:"default"`  // Default is any because config defaults can be int, bool, string, nil, or map — cannot use concrete type
	Override any    `json:"override"` // Override is any because config overrides can be int, bool, string, nil, or map — nil means no override set
}

// ── Constraint / ConstraintRegistry ──

// ResolveFn is a callable that resolves the effective value of a setting.
type ResolveFn func(otherKey string, otherCategory ...string) (any, error)

// Constraint receives (key_being_set, resolve_fn) and returns an error
// if the pending change would create an invalid state.
type Constraint func(key string, resolve ResolveFn) error

// ConstraintRegistry registers and looks up cross-key validation constraints.
type ConstraintRegistry struct {
	Constraints map[[2]string][]Constraint // (category, key) -> constraints
}
