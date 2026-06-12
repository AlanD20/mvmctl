package model

// ResourceMap is a generic string→any map for specs and output data.
// Using map[string]any instead of typed structs means all YAML fields
// pass through to step implementations without schema validation.
// Each step extracts the fields it needs.
type ResourceMap map[string]any

// GetString safely extracts a string value from the spec.
func (r ResourceMap) GetString(key string) string {
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
// Returns false if the key is missing. Handles both bool and string values
// ("true", "yes", "1" → true; "false", "no", "0" → false).
func (r ResourceMap) GetBool(key string) bool {
	if r == nil {
		return false
	}
	v, ok := r[key]
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true" || b == "yes" || b == "1"
	default:
		return false
	}
}

// GetInt safely extracts an int value from the spec.
// Returns 0 if the key is missing or not an int.
func (r ResourceMap) GetInt(key string) int {
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

// ResourceMeta is workflow metadata per resource.
type ResourceMeta struct {
	WasCreated bool   `yaml:"was_created"`
	SpecHash   string `yaml:"spec_hash,omitempty"`
}

// ResourceState is the full state of a resource (input + output + metadata).
type ResourceState struct {
	Spec   ResourceMap  `yaml:"spec,omitempty"`
	Output ResourceMap  `yaml:"output,omitempty"`
	Meta   ResourceMeta `yaml:"meta,omitempty"`
}

// AppliedResource is a resource that has been applied and persisted.
type AppliedResource struct {
	Name         string         `yaml:"name"`
	Type         string         `yaml:"type"`
	Dependencies []string       `yaml:"depends_on,omitempty"`
	State        ResourceState  `yaml:"state"`
}

// WorkflowState holds the complete persisted state for a workflow execution.
type WorkflowState struct {
	WorkflowID    string            `yaml:"workflow_id"`
	SpecPath      string            `yaml:"spec_path"`
	SchemaVersion string            `yaml:"schema_version"`
	CreatedAt     string            `yaml:"created_at"`
	UpdatedAt     string            `yaml:"updated_at"`
	ContentHash   string            `yaml:"content_hash,omitempty"`
	Resources     []AppliedResource `yaml:"resources"`
}

// StepResult captures the outcome of a single step execution.
type StepResult struct {
	StepName string `json:"step_name"`
	StepType string `json:"step_type"`
	// Status is one of: "applied", "skipped", "drifted", "failed".
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
