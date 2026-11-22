package inputs

import (
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

type VMPolicySelectorInput struct {
	VM     string                `json:"vm,omitempty" yaml:"vm,omitempty"`
	Labels model.VMLabelSelector `json:"labels,omitempty" yaml:"labels,omitempty"`
}

func (input VMPolicySelectorInput) Validate() error {
	if input.VM == "" {
		return input.Labels.Validate()
	}
	if input.Labels != nil {
		return errs.New(errs.CodeValidationFailed, "a policy endpoint must specify a VM or a label selector")
	}
	return (VMLabelInput{VM: input.VM}).Validate()
}

type LabelTrafficPolicyCreateInput struct {
	Source          VMPolicySelectorInput `json:"source" yaml:"source"`
	Destination     VMPolicySelectorInput `json:"destination" yaml:"destination"`
	Protocol        string                `json:"protocol" yaml:"protocol"`
	DestinationPort string                `json:"destination_port" yaml:"destination_port"`
}

func (input LabelTrafficPolicyCreateInput) ResolveTraffic() (model.TrafficProtocol, uint16, uint16, error) {
	if input.Source.VM != "" && input.Destination.VM != "" {
		return "", 0, 0, errs.New(errs.CodeValidationFailed, "label traffic policies require at least one label selector")
	}
	if err := input.Source.Validate(); err != nil {
		return "", 0, 0, err
	}
	if err := input.Destination.Validate(); err != nil {
		return "", 0, 0, err
	}
	transport, first, last, err := resolveCanonicalTrafficValues(input.Protocol, input.DestinationPort)
	if err != nil {
		return "", 0, 0, err
	}
	return transport, uint16(first), uint16(last), nil
}

type LabelTrafficPolicyInput struct {
	Identifiers []string `json:"identifiers"`
}

func (input LabelTrafficPolicyInput) Validate() error {
	return validatePolicyIdentifiers(input.Identifiers)
}
