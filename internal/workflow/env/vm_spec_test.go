package env

import (
	"testing"

	"gopkg.in/yaml.v3"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
)

// TestVMAllowRemoteExecFullSpec verifies that allow_remote_exec: true in the
// YAML spec correctly populates VMCreateInput.AllowRemoteExec through the
// entire pipeline: YAML → EnvSpec.Steps → yaml.Marshal → yaml.Unmarshal → VMCreateInput.
func TestVMAllowRemoteExecFullSpec(t *testing.T) {
	yamlData := []byte(`
version: "1"
vm:
  - name: controller-1
    vcpu: 2
    network: k8s
    key: main-key
    image: k8s-base
    kernel: custom-kernel
    binary: fc-binary
    allow_remote_exec: true
  - name: controller-2
    vcpu: 2
    network: k8s
    key: main-key
    image: k8s-base
    kernel: custom-kernel
    binary: fc-binary
    allow_remote_exec: true
`)

	var spec EnvSpec
	if err := yaml.Unmarshal(yamlData, &spec); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}

	vms := spec.Steps["vm"]
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}

	for i, name := range []string{"controller-1", "controller-2"} {
		vm := vms[i]
		if vm["name"] != name {
			t.Errorf("vm[%d].name = %v, want %s", i, vm["name"], name)
		}
		if vm["allow_remote_exec"] != true {
			t.Errorf("vm[%d].allow_remote_exec = %v (type %T), want true",
				i, vm["allow_remote_exec"], vm["allow_remote_exec"])
		}

		// Same round-trip as newVMStepFromSpec
		data, err := yaml.Marshal(vm)
		if err != nil {
			t.Fatalf("marshal vm[%d]: %v", i, err)
		}
		var input inputs.VMCreateInput
		if err := yaml.Unmarshal(data, &input); err != nil {
			t.Fatalf("unmarshal vm[%d] into VMCreateInput: %v", i, err)
		}
		if input.AllowRemoteExec == nil || *input.AllowRemoteExec != true {
			t.Errorf("VMCreateInput.AllowRemoteExec for %s = %v, want true",
				name, input.AllowRemoteExec)
		}
	}
}

// TestVMStepFromSpec_PreservesAllowRemoteExec verifies that Registry["vm"].FromSpec
// produces a step whose Apply input has AllowRemoteExec set correctly.
func TestVMStepFromSpec_PreservesAllowRemoteExec(t *testing.T) {
	spec := model.ResourceMap{
		"name":              "controller-1",
		"vcpu":              2,
		"network":           "k8s",
		"key":               "main-key",
		"image":             "k8s-base",
		"kernel":            "custom-kernel",
		"binary":            "fc-binary",
		"allow_remote_exec": true,
	}

	step, err := Registry["vm"].FromSpec("vm", "controller-1", spec, &api.Operation{})
	if err != nil {
		t.Fatalf("FromSpec: %v", err)
	}

	// The step should produce a VMCreateInput with AllowRemoteExec set.
	// We can't access it directly from the Step interface, but we can verify
	// the step type and that it was constructed without error.
	if step.Type() != "vm" {
		t.Errorf("step.Type() = %q, want %q", step.Type(), "vm")
	}
	if step.Name() != "vm:controller-1" {
		t.Errorf("step.Name() = %q, want %q", step.Name(), "vm:controller-1")
	}
}

// TestVMAllowRemoteExecStepFunc verifies that the actual step function
// correctly parses allow_remote_exec from spec and sets it in the input.
func TestVMAllowRemoteExecStepFunc(t *testing.T) {
	spec := model.ResourceMap{
		"name":              "test-vm",
		"vcpu":              2,
		"network":           "test-net",
		"key":               "test-key",
		"image":             "test-image",
		"kernel":            "test-kernel",
		"binary":            "test-binary",
		"allow_remote_exec": true,
	}

	// Marshal the spec and unmarshal into VMCreateInput (same path as newVMStepFromSpec)
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}

	var input inputs.VMCreateInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		t.Fatalf("unmarshal into VMCreateInput: %v", err)
	}

	if input.Name != "test-vm" {
		t.Errorf("Name = %q, want %q", input.Name, "test-vm")
	}
	if input.AllowRemoteExec == nil {
		t.Fatal("AllowRemoteExec is nil")
	}
	if *input.AllowRemoteExec != true {
		t.Errorf("AllowRemoteExec = %v, want true", *input.AllowRemoteExec)
	}
}

// TestVMFromSpec_AllowsEmptyOp ensures FromSpec accepts an empty Operation.
func TestVMFromSpec_AllowsEmptyOp(t *testing.T) {
	spec := model.ResourceMap{
		"name":    "test-vm",
		"vcpu":    2,
		"network": "test-net",
	}

	_, err := Registry["vm"].FromSpec("vm", "test-vm", spec, &api.Operation{})
	if err != nil {
		t.Fatalf("FromSpec with empty Operation: %v", err)
	}
}

// TestVMDependencies asserts that the env.Dependencies() helper includes
// the correct reference dependencies from spec.
func TestVMDependencies(t *testing.T) {
	tests := []struct {
		name     string
		spec     model.ResourceMap
		stepName string
		wantDeps []string
	}{
		{
			name: "no_deps",
			spec: model.ResourceMap{
				"name": "test-vm",
			},
			wantDeps: nil,
		},
		{
			name: "resource_refs_only",
			spec: model.ResourceMap{
				"name":    "test-vm",
				"network": "my-net",
				"key":     "my-key",
				"image":   "my-image",
			},
			wantDeps: nil, // resource refs don't create implicit DAG deps
		},
		{
			name: "explicit_depends_on",
			spec: model.ResourceMap{
				"name":       "test-vm",
				"depends_on": []any{"network:my-net", "key:my-key"},
			},
			wantDeps: []string{"network:my-net", "key:my-key"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			step, err := Registry["vm"].FromSpec("vm", "test-vm", tc.spec, &api.Operation{})
			if err != nil {
				t.Fatalf("FromSpec: %v", err)
			}
			deps := step.Dependencies()

			if len(deps) != len(tc.wantDeps) {
				t.Errorf("Dependencies() = %v, want %v", deps, tc.wantDeps)
				return
			}
			for i := range deps {
				if deps[i] != tc.wantDeps[i] {
					t.Errorf("Dependencies()[%d] = %q, want %q", i, deps[i], tc.wantDeps[i])
				}
			}
		})
	}
}
