package inputs

import (
	"strings"
	"time"

	"mvmctl/pkg/errs"
)

// ReadyInput describes a bounded guest command readiness check.
type ReadyInput struct {
	Identifier string `json:"target" yaml:"target"`
	Command    string `json:"command" yaml:"cmd"`
	User       string `json:"user,omitempty" yaml:"user,omitempty"`
	Timeout    *int   `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Interval   *int   `json:"interval,omitempty" yaml:"interval,omitempty"`
}

// Validate rejects empty checks and invalid or overflowing duration values.
func (i *ReadyInput) Validate() error {
	if strings.TrimSpace(i.Command) == "" {
		return errs.New(errs.CodeValidationFailed, "readiness command is required")
	}
	for _, value := range []*int{i.Timeout, i.Interval} {
		if value != nil && (*value <= 0 || int64(*value) > int64((1<<63-1)/time.Second)) {
			return errs.New(
				errs.CodeValidationFailed,
				"readiness timeout and interval must be positive seconds within duration range",
			)
		}
	}
	input := ExecInput{Identifier: i.Identifier, Command: i.Command, User: i.User}
	return input.Validate()
}

// Budget returns the total readiness deadline, defaulting to sixty seconds.
func (i ReadyInput) Budget() time.Duration {
	if i.Timeout == nil {
		return 60 * time.Second
	}
	return time.Duration(*i.Timeout) * time.Second
}

// RetryInterval returns the delay between failed checks, defaulting to one second.
func (i ReadyInput) RetryInterval() time.Duration {
	if i.Interval == nil {
		return time.Second
	}
	return time.Duration(*i.Interval) * time.Second
}
