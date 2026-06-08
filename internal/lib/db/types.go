// Package db provides database-level types that implement sql.Scanner and
// driver.Valuer for common patterns (JSON arrays, etc.).
package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// StringSlice is a []string that implements sql.Scanner and driver.Valuer
// for TEXT columns containing JSON arrays (e.g. `["a","b"]`).
type StringSlice []string

// Scan implements sql.Scanner for reading JSON array TEXT into StringSlice.
func (s *StringSlice) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var val string
	switch v := src.(type) {
	case []byte:
		val = string(v)
	case string:
		val = v
	default:
		return fmt.Errorf("db.StringSlice: unsupported scan type %T", src)
	}
	if val == "" {
		*s = StringSlice{}
		return nil
	}
	return json.Unmarshal([]byte(val), s)
}

// Value implements driver.Valuer for writing StringSlice as JSON array TEXT.
func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	return json.Marshal(s)
}
