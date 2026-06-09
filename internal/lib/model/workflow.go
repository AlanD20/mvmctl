package model

// ResourceSpec is a generic YAML spec entry. Using map[string]any instead of
// typed structs means all YAML fields pass through to step implementations
// without schema validation. Each step extracts the fields it needs.
type ResourceSpec map[string]any

// GetString safely extracts a string value from the spec.
func (r ResourceSpec) GetString(key string) string {
	if r == nil {
		return ""
	}
	v, ok := r[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// GetBool safely extracts a boolean value from the spec.
// Returns false if the key is missing or not a bool.
func (r ResourceSpec) GetBool(key string) bool {
	if r == nil {
		return false
	}
	v, ok := r[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// GetInt safely extracts an int value from the spec.
// Returns 0 if the key is missing or not an int.
func (r ResourceSpec) GetInt(key string) int {
	if r == nil {
		return 0
	}
	v, ok := r[key]
	if !ok {
		return 0
	}
	i, _ := v.(int)
	return i
}

// SavedResource represents a single step's persisted state within a workflow.
type SavedResource struct {
	StepName     string         `yaml:"step_name"`
	StepType     string         `yaml:"step_type"`
	Dependencies []string       `yaml:"depends_on,omitempty"`
	State        ResourceSpec `yaml:"state,omitempty"`
}

// WorkflowState holds the complete persisted state for a workflow execution.
type WorkflowState struct {
	WorkflowID    string          `yaml:"workflow_id"`
	SpecPath      string          `yaml:"spec_path"`
	SchemaVersion string          `yaml:"schema_version"`
	CreatedAt     string          `yaml:"created_at"`
	UpdatedAt     string          `yaml:"updated_at"`
	ContentHash   string          `yaml:"content_hash,omitempty"`
	Resources     []SavedResource `yaml:"resources"`
}
