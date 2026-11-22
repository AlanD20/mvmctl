package model

import (
	"bytes"
	"encoding/json"
	"io"

	"gopkg.in/yaml.v3"

	"mvmctl/pkg/errs"
)

func (labels *VMLabels) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return invalidVMLabelMap()
	}
	next := make(VMLabels)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return invalidVMLabelMap()
		}
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return invalidVMLabelMap()
		}
		if err := next.add(key, value); err != nil {
			return err
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return invalidVMLabelMap()
	}
	if _, err = decoder.Token(); err != io.EOF {
		return invalidVMLabelMap()
	}
	*labels = next
	return nil
}

func (labels *VMLabels) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode || node.Tag != "!!map" || len(node.Content)%2 != 0 {
		return invalidVMLabelMap()
	}
	next := make(VMLabels)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" ||
			value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return invalidVMLabelMap()
		}
		if err := next.add(key.Value, value.Value); err != nil {
			return err
		}
	}
	*labels = next
	return nil
}

func (selector *VMLabelSelector) UnmarshalJSON(raw []byte) error {
	var labels VMLabels
	if err := labels.UnmarshalJSON(raw); err != nil {
		return err
	}
	return selector.assign(labels)
}

func (selector *VMLabelSelector) UnmarshalYAML(node *yaml.Node) error {
	var labels VMLabels
	if err := labels.UnmarshalYAML(node); err != nil {
		return err
	}
	return selector.assign(labels)
}

func (selector *VMLabelSelector) assign(labels VMLabels) error {
	next := VMLabelSelector(labels)
	if err := next.Validate(); err != nil {
		return err
	}
	*selector = next
	return nil
}

func (labels VMLabels) add(key, value string) error {
	if _, duplicate := labels[key]; duplicate {
		return errs.New(errs.CodeValidationFailed, "duplicate VM label key")
	}
	if len(labels) >= maxVMLabels {
		return tooManyVMLabels()
	}
	if err := validateVMLabel(key, value); err != nil {
		return err
	}
	labels[key] = value
	return nil
}

func invalidVMLabelMap() error {
	return errs.New(errs.CodeValidationFailed, "VM labels must be a mapping of string keys to string values")
}
