package workflow

import (
	"mvmctl/internal/lib/model"
	"sync"
)

// SharedState is a thread-safe key-value store for passing data between
// steps in a pipeline. Keys are step names; values are arbitrary data
// that a step wants to expose to its dependents.
type SharedState struct {
	mu   sync.RWMutex
	data model.ResourceMap
}

// NewSharedState creates an empty shared state.
func NewSharedState() *SharedState {
	return &SharedState{
		data: make(model.ResourceMap),
	}
}

// Set stores a value under the given step name. Safe for concurrent use.
// value is any because step output types vary per domain (NetworkState, VMState, etc.)
// and the shared state is a generic pass-through between pipeline steps.
func (s *SharedState) Set(stepName string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[stepName] = value
}

// Get retrieves a value by step name. Returns the value and true if found,
// or nil and false if the step has no stored data.
func (s *SharedState) Get(stepName string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[stepName]
	return v, ok
}

// Keys returns all step names that have stored data.
func (s *SharedState) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}
