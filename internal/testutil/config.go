package testutil

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"mvmctl/internal/core/config"
)

// ConfigRepo is an in-memory settings repository for testing.
// Matches Python's mvmctl.core.config._repository.SettingsRepository exactly,
// including JSON serialization/deserialization of values.
type ConfigRepo struct {
	mu   sync.RWMutex
	data map[string]map[string]settingRow // category -> key -> row
}

type settingRow struct {
	rawValue  string // JSON-encoded value (matches Python's json.dumps/json.loads)
	updatedAt string
}

func NewConfigRepo() *ConfigRepo {
	return &ConfigRepo{data: make(map[string]map[string]settingRow)}
}

// Get returns the parsed JSON value for a setting, or nil if not found.
// Matches Python: json.loads(row["value"]).
func (r *ConfigRepo) Get(_ context.Context, category, key string) (any, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cat, ok := r.data[category]
	if !ok {
		return nil, nil
	}
	row, ok := cat[key]
	if !ok {
		return nil, nil
	}
	// Match Python: return json.loads(row["value"])
	if row.rawValue == "" {
		return nil, nil
	}
	var val any
	if err := json.Unmarshal([]byte(row.rawValue), &val); err != nil {
		return nil, nil
	}
	return val, nil
}

// Set stores a value as JSON. Matches Python: json.dumps(value).
func (r *ConfigRepo) Set(_ context.Context, category, key string, value any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[category]; !ok {
		r.data[category] = make(map[string]settingRow)
	}
	// Match Python: json.dumps(value)
	raw, err := json.Marshal(value)
	if err != nil {
		// Python would raise a JSONEncodeError through json.dumps
		return err
	}
	r.data[category][key] = settingRow{
		rawValue:  string(raw),
		updatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return nil
}

// Delete removes a setting. Returns true if a row was deleted. Matches Python: cursor.rowcount > 0.
func (r *ConfigRepo) Delete(_ context.Context, category, key string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cat, ok := r.data[category]
	if !ok {
		return false, nil
	}
	_, ok = cat[key]
	if !ok {
		return false, nil
	}
	delete(cat, key)
	return true, nil
}

// DeleteByCategory removes all settings in a category. Returns number of rows deleted.
func (r *ConfigRepo) DeleteByCategory(_ context.Context, category string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cat, ok := r.data[category]
	if !ok {
		return 0, nil
	}
	count := len(cat)
	delete(r.data, category)
	return count, nil
}

// DeleteAll removes ALL user settings. Returns number of rows deleted.
func (r *ConfigRepo) DeleteAll(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, cat := range r.data {
		count += len(cat)
	}
	r.data = make(map[string]map[string]settingRow)
	return count, nil
}

// Count returns total number of user settings.
func (r *ConfigRepo) Count(_ context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, cat := range r.data {
		count += len(cat)
	}
	return count, nil
}

// ListByCategory lists settings, optionally filtered by category.
// Returns nested map: {category: {key: value}}.
// Matches Python: ORDER BY category, key. Uses json.loads(value).
func (r *ConfigRepo) ListByCategory(_ context.Context, category *string) (map[string]map[string]any, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]map[string]any)

	if category != nil {
		cat, ok := r.data[*category]
		if !ok {
			return result, nil
		}
		entries := make(map[string]any)
		for k, row := range cat {
			if row.rawValue != "" {
				var val any
				if err := json.Unmarshal([]byte(row.rawValue), &val); err == nil {
					entries[k] = val
				}
			}
		}
		result[*category] = entries
		return result, nil
	}

	// Collect all entries and sort by category, then key (Python: ORDER BY category, key)
	type kv struct {
		category string
		key      string
		value    any
	}
	var sorted []kv
	for cat, entries := range r.data {
		for k, row := range entries {
			if row.rawValue != "" {
				var val any
				if err := json.Unmarshal([]byte(row.rawValue), &val); err == nil {
					sorted = append(sorted, kv{cat, k, val})
				}
			}
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].category != sorted[j].category {
			return sorted[i].category < sorted[j].category
		}
		return sorted[i].key < sorted[j].key
	})

	for _, item := range sorted {
		if result[item.category] == nil {
			result[item.category] = make(map[string]any)
		}
		result[item.category][item.key] = item.value
	}
	return result, nil
}

// Ensure ConfigRepo implements config.SettingsRepository.
var _ config.SettingsRepository = (*ConfigRepo)(nil)
