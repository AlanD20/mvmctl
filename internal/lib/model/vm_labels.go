package model

import (
	"fmt"
	"regexp"

	"mvmctl/pkg/errs"
)

const maxVMLabels = 32

var vmLabelPart = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// VMLabels holds the explicit labels of one VM. An empty map clears its labels.
type VMLabels map[string]string

// VMLabelSelector requires every declared key and value to match.
type VMLabelSelector map[string]string

func (labels VMLabels) Validate() error {
	if len(labels) > maxVMLabels {
		return tooManyVMLabels()
	}
	for key, value := range labels {
		if err := validateVMLabel(key, value); err != nil {
			return err
		}
	}
	return nil
}

func tooManyVMLabels() error {
	return errs.New(errs.CodeValidationFailed, fmt.Sprintf("VM labels may contain at most %d entries", maxVMLabels))
}

func validateVMLabel(key, value string) error {
	if !vmLabelPart.MatchString(key) || !vmLabelPart.MatchString(value) {
		return errs.New(errs.CodeValidationFailed,
			"VM label keys and values must be 1 to 63 lowercase ASCII letters, digits, dots, underscores or hyphens, "+
				"and start and end with a letter or digit")
	}
	return nil
}

func (selector VMLabelSelector) Validate() error {
	if len(selector) == 0 {
		return errs.New(errs.CodeValidationFailed, "VM label selector must contain at least one requirement")
	}
	return VMLabels(selector).Validate()
}

// Matches applies an equality selector during policy materialization.
func (selector VMLabelSelector) Matches(labels VMLabels) bool {
	if len(selector) == 0 {
		return false
	}
	for key, expected := range selector {
		if actual, ok := labels[key]; !ok || actual != expected {
			return false
		}
	}
	return true
}
