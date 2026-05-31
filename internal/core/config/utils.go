package config

// IsKeyInCategory checks if a key is valid for a given category in OverridableSettings.
func IsKeyInCategory(category, key string) bool {
	if catSettings, ok := OverridableSettings[category]; ok {
		for k := range catSettings {
			if k == key {
				return true
			}
		}
	}
	return false
}
