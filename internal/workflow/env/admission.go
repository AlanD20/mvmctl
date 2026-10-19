package env

import (
	"bytes"
	"fmt"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	"mvmctl/pkg/api/inputs"

	"gopkg.in/yaml.v3"
)

// decodeStepInput excludes workflow metadata while strictly decoding the public input.
// The destination is a sum of the concrete input types selected below.
func decodeStepInput(spec model.ResourceMap, input any, extras ...string) error {
	fields := make(model.ResourceMap, len(spec))
	for key, value := range spec {
		fields[key] = value
	}
	for _, key := range append([]string{"depends_on", "removes", "existing"}, extras...) {
		delete(fields, key)
	}
	data, err := yaml.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(input)
}

func validateSpecStructure(spec EnvSpec) error {
	for kind, entries := range spec.Steps {
		if _, ok := Registry[kind]; !ok {
			return fmt.Errorf("unknown environment section %q", kind)
		}
		for name, fields := range entries {
			if name == "" || strings.ContainsAny(name, ":@ \t\r\n") || fields == nil {
				return fmt.Errorf("invalid %s step name or declaration %q", kind, name)
			}
			for _, key := range []string{"depends_on", "removes"} {
				if raw, ok := fields[key]; ok {
					values, ok := raw.([]any)
					if !ok {
						return fmt.Errorf("%s:%s %s must be a list of step references", kind, name, key)
					}
					for _, value := range values {
						ref, ok := value.(string)
						if !ok || !validStepReference(ref) {
							return fmt.Errorf("%s:%s invalid %s reference %v", kind, name, key, value)
						}
					}
				}
			}
			if value, ok := fields["existing"]; ok {
				selector, ok := value.(string)
				if !ok || strings.TrimSpace(selector) == "" || strings.HasPrefix(selector, "@") {
					return fmt.Errorf("%s:%s existing must be a nonempty resource selector", kind, name)
				}
			}
			if err := validateStepInput(kind, name, fields); err != nil {
				return fmt.Errorf("%s:%s: %w", kind, name, err)
			}
		}
	}
	return nil
}

func validateStepInput(kind, name string, spec model.ResourceMap) error {
	existing := spec.GetString("existing") != ""
	if existing {
		switch kind {
		case "network", "key", "vm":
			for field := range spec {
				if field != "existing" && field != "depends_on" && field != "removes" {
					return fmt.Errorf("existing cannot be combined with creation field %q", field)
				}
			}
		case "volume", "traffic_policy", "exec_policy":
		default:
			return fmt.Errorf("existing is unsupported for %s", kind)
		}
	}
	switch kind {
	case "network":
		var in inputs.NetworkCreateInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		if in.Name == "" {
			in.Name = name
		}
		if !existing {
			return in.Validate()
		}
	case "key":
		var in inputs.KeyCreateInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		if in.Name == "" {
			in.Name = name
		}
		if in.Overwrite {
			return fmt.Errorf("force cannot overwrite an environment key")
		}
		if !existing {
			return in.Validate()
		}
	case "vm":
		var in inputs.VMCreateInput
		if err := decodeStepInput(spec, &in, "key"); err != nil {
			return err
		}
		if in.Name == "" {
			in.Name = name
		}
		if in.Count != nil && *in.Count != 1 {
			return fmt.Errorf("count must be 1; declare a separate named step for each VM")
		}
		if !existing {
			return in.Validate()
		}
	case "image":
		var in inputs.ImagePullInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		return in.Validate()
	case "image_import":
		var in inputs.ImageImportInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		if in.Name == "" {
			in.Name = name
		}
		return in.Validate()
	case "kernel":
		var in inputs.KernelPullInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		return in.Validate()
	case "binary":
		var in inputs.BinaryPullInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		return in.Validate()
	case "exec":
		var in inputs.ExecInput
		if err := decodeStepInput(spec, &in, "ignore_errors"); err != nil {
			return err
		}
		if v, ok := spec["ignore_errors"]; ok {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("ignore_errors must be a boolean")
			}
		}
		if in.Command == "" {
			return fmt.Errorf("cmd is required")
		}
		if strings.HasPrefix(in.Identifier, "@") {
			in.Identifier = "reference"
		}
		return in.Validate()
	case "ssh":
		var in inputs.SSHInput
		if err := decodeStepInput(spec, &in); err != nil {
			return err
		}
		if in.Cmd == nil || *in.Cmd == "" {
			return fmt.Errorf("cmd is required")
		}
		if strings.HasPrefix(in.Identifier, "@") {
			in.Identifier = "reference"
		}
		return in.Validate()
	case "copy":
		fields := make(model.ResourceMap, len(spec))
		for k, v := range spec {
			fields[k] = v
		}
		if src, ok := fields["src"].(string); ok {
			fields["src"] = []string{src}
		}
		var in inputs.CPInput
		if err := decodeStepInput(fields, &in); err != nil {
			return err
		}
		return in.Validate()
	case "volume", "traffic_policy", "exec_policy", "ready":
		allowed := map[string]string{
			"volume":         "name size read_only shareable writeback retain existing",
			"traffic_policy": "source_network source_vm destination_vm protocol port existing",
			"exec_policy":    "source_vm destination_vm target_user existing",
			"ready":          "target cmd timeout interval user",
		}
		for field := range spec {
			if field != "depends_on" && field != "removes" && !strings.Contains(" "+allowed[kind]+" ", " "+field+" ") {
				return fmt.Errorf("unknown field %q", field)
			}
		}
	}
	return nil
}

func validStepReference(ref string) bool {
	ref = strings.TrimPrefix(ref, "@")
	kind, name, ok := strings.Cut(ref, ":")
	_, known := Registry[kind]
	return ok && known && name != "" && !strings.ContainsAny(name, ":@ \t\r\n")
}

func validateSpecReferences(spec EnvSpec, steps []workflow.Step) error {
	byName := make(map[string]workflow.Step, len(steps))
	for _, s := range steps {
		byName[s.Name()] = s
	}
	for kind, entries := range spec.Steps {
		for name, fields := range entries {
			step := byName[FormatStepName(kind, name)]
			reachable := map[string]bool{}
			var visit func(string)
			visit = func(n string) {
				if reachable[n] {
					return
				}
				reachable[n] = true
				for _, d := range byName[n].Dependencies() {
					visit(d)
				}
			}
			for _, d := range step.Dependencies() {
				visit(d)
			}
			for field, value := range fields {
				expected := ""
				switch kind + "/" + field {
				case "vm/network", "traffic_policy/source_network":
					expected = "network"
				case "vm/key", "vm/ssh_keys", "ssh/key":
					expected = "key"
				case "vm/image":
					expected = "image|image_import"
				case "vm/kernel":
					expected = "kernel"
				case "vm/binary":
					expected = "binary"
				case "vm/volumes":
					expected = "volume"
				case "exec/target",
					"ssh/target",
					"ready/target",
					"image_import/source",
					"traffic_policy/source_vm",
					"traffic_policy/destination_vm",
					"exec_policy/source_vm",
					"exec_policy/destination_vm",
					"copy/src",
					"copy/dest":
					expected = "vm"
				}
				if expected == "" {
					continue
				}
				values := []any{value}
				if list, ok := value.([]any); ok {
					values = list
				}
				for _, v := range values {
					ref, ok := v.(string)
					if !ok {
						return fmt.Errorf("%s %s must contain resource selectors", step.Name(), field)
					}
					if !strings.HasPrefix(ref, "@") {
						continue
					}
					if (kind == "copy" || kind == "image_import") && !strings.HasPrefix(ref, "@vm:") {
						continue
					}
					if kind == "copy" {
						parts := strings.SplitN(ref, ":", 3)
						if len(parts) != 3 || parts[2] == "" {
							return fmt.Errorf("%s copy reference requires @vm:step:/path", step.Name())
						}
						ref = parts[0] + ":" + parts[1]
					}
					target := strings.TrimPrefix(ref, "@")
					targetType, _, _ := strings.Cut(target, ":")
					if !validStepReference(ref) || !strings.Contains("|"+expected+"|", "|"+targetType+"|") {
						return fmt.Errorf("%s invalid %s reference %q (expected %s)", step.Name(), field, ref, expected)
					}
					if byName[target] == nil || !reachable[target] {
						return fmt.Errorf("%s reference %q requires an explicit depends_on path", step.Name(), ref)
					}
				}
			}
			for _, target := range step.Removes() {
				if byName[target] == nil || target == step.Name() {
					return fmt.Errorf("%s invalid removes target %q", step.Name(), target)
				}
				if InferStepType(target) == "binary" {
					return fmt.Errorf("binary removes is unsupported; binary assets are retained")
				}
				if InferStepType(target) == "kernel" {
					return fmt.Errorf("kernel removes is unsupported; kernels are retained")
				}
			}
		}
	}
	return nil
}
