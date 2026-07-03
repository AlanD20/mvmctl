package env

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestVMAllowRemoteExecRoundTrip(t *testing.T) {
	yamlData := []byte(`
version: "1"
ephemeral: true
vm:
  - name: test-vm
    vcpu: 2
    allow_remote_exec: true
    key: builder
    image: test-image
    network: test-net
`)

	var spec EnvSpec
	if err := yaml.Unmarshal(yamlData, &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if spec.Version != "1" {
		t.Errorf("version = %q, want %q", spec.Version, "1")
	}
	if !spec.Ephemeral {
		t.Error("ephemeral should be true")
	}
	vms := spec.Steps["vm"]
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(vms))
	}
	vm := vms[0]
	if vm["name"] != "test-vm" {
		t.Errorf("name = %v, want test-vm", vm["name"])
	}
	if vm["allow_remote_exec"] != true {
		t.Errorf("allow_remote_exec = %v (type %T), want true", vm["allow_remote_exec"], vm["allow_remote_exec"])
	}

	// Round-trip through yaml.Marshal + Unmarshal (same path as newVMStepFromSpec)
	data, err := yaml.Marshal(vm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	type vmInput struct {
		Name            string `yaml:"name"`
		AllowRemoteExec *bool  `yaml:"allow_remote_exec,omitempty"`
		VCPU            int    `yaml:"vcpu"`
	}
	var input vmInput
	if err := yaml.Unmarshal(data, &input); err != nil {
		t.Fatalf("unmarshal into input: %v", err)
	}

	if input.AllowRemoteExec == nil || *input.AllowRemoteExec != true {
		t.Errorf("AllowRemoteExec = %v, want true", input.AllowRemoteExec)
	}
}
