// Package env_test black-box tests the environment workflow engine's step
// factory registry, spec resolution, and step construction. Does not test
// actual provisioning behavior (that requires real API operations).
package env_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/workflow"
	envpkg "mvmctl/internal/workflow/env"
	"mvmctl/pkg/api"
)

// ─── Registry ─────────────────────────────────────────────────────────────────

// Rationale: The Registry must contain all 6 required step types so that
// ResolveSpec can construct the correct steps from a YAML spec.

func TestRegistry_ContainsAllExpectedTypes(t *testing.T) {
	expectedTypes := map[string]struct{}{
		"networks": {},
		"keys":     {},
		"images":   {},
		"kernels":  {},
		"binaries": {},
		"vms":      {},
		"ssh":      {},
		"copy":     {},
	}

	for typ := range expectedTypes {
		t.Run(typ, func(t *testing.T) {
			factory, ok := envpkg.Registry[typ]
			require.True(t, ok, "Registry missing expected step type %q", typ)
			require.NotNil(t, factory.FromSpec, "Registry[%q].FromSpec is nil", typ)
			require.NotNil(t, factory.FromState, "Registry[%q].FromState is nil", typ)
			require.NotEmpty(t, factory.StepType, "Registry[%q].StepType is empty", typ)
		})
	}

	t.Run("no_unexpected_types", func(t *testing.T) {
		assert.Len(t, envpkg.Registry, len(expectedTypes), "Registry has unexpected entries")
	})
}

// ─── FromSpec factory ─────────────────────────────────────────────────────────

// Rationale: Each FromSpec factory must create a Step with the correct name
// format "type:name" and correct Type() so that dependency resolution
// and registry lookups work correctly.

func TestFromSpec_NetworkStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "my-net", "subnet": "10.0.0.0/24"}
	step, err := envpkg.Registry["networks"].FromSpec("network", "my-net", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "network:my-net", step.Name())
	assert.Equal(t, "network", step.Type())
	assert.IsType(t, &envpkg.NetworkStep{}, step)
}

func TestFromSpec_KeyStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "my-key"}
	step, err := envpkg.Registry["keys"].FromSpec("key", "my-key", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "key:my-key", step.Name())
	assert.Equal(t, "key", step.Type())
	assert.IsType(t, &envpkg.KeyStep{}, step)
}

func TestFromSpec_ImageStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "alpine", "type": "alpine", "version": "3.21"}
	step, err := envpkg.Registry["images"].FromSpec("image", "alpine", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "image:alpine", step.Name())
	assert.Equal(t, "image", step.Type())
	assert.IsType(t, &envpkg.ImageStep{}, step)
}

func TestFromSpec_KernelStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "fc-kernel", "type": "firecracker", "version": "1.15.1"}
	step, err := envpkg.Registry["kernels"].FromSpec("kernel", "fc-kernel", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "kernel:fc-kernel", step.Name())
	assert.Equal(t, "kernel", step.Type())
	assert.IsType(t, &envpkg.KernelStep{}, step)
}

func TestFromSpec_BinaryStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "firecracker", "version": "1.15.1"}
	step, err := envpkg.Registry["binaries"].FromSpec("binary", "firecracker", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "binary:firecracker", step.Name())
	assert.Equal(t, "binary", step.Type())
	assert.IsType(t, &envpkg.BinaryStep{}, step)
}

func TestFromSpec_VMStep_NameFormat(t *testing.T) {
	spec := map[string]any{
		"name": "my-vm", "network": "my-net", "key": "my-key",
		"image": "alpine", "kernel": "fc-kernel", "binary": "firecracker",
	}
	step, err := envpkg.Registry["vms"].FromSpec("vm", "my-vm", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "vm:my-vm", step.Name())
	assert.Equal(t, "vm", step.Type())
	assert.IsType(t, &envpkg.VMStep{}, step)
}

func TestFromSpec_SSHStep_NameFormat(t *testing.T) {
	spec := map[string]any{"name": "install-qemu", "target": "fenv-vm-1", "user": "root", "cmd": "apt update"}
	step, err := envpkg.Registry["ssh"].FromSpec("ssh", "install-qemu", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "ssh:install-qemu", step.Name())
	assert.Equal(t, "ssh", step.Type())
	assert.IsType(t, &envpkg.SSHStep{}, step)
}

func TestFromSpec_CopyStep_NameFormat(t *testing.T) {
	spec := map[string]any{
		"name":   "copy-binary",
		"target": "fenv-vm-1",
		"user":   "root",
		"src":    "./mvm",
		"dst":    "/root/",
	}
	step, err := envpkg.Registry["copy"].FromSpec("copy", "copy-binary", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "copy:copy-binary", step.Name())
	assert.Equal(t, "copy", step.Type())
	assert.IsType(t, &envpkg.CopyStep{}, step)
}

// ─── FromState factory ─────────────────────────────────────────────────────────

// Rationale: Each FromState factory must reconstruct a step from previously
// persisted state. The resulting step must have the correct name, type,
// and be castable to the correct step type.

func TestFromState_NetworkStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"network_id":  "net-123",
		"subnet":      "10.0.0.0/24",
		"was_created": true,
	}
	step, err := envpkg.Registry["networks"].FromState("network", "my-net", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "network:my-net", step.Name())
	assert.Equal(t, "network", step.Type())
	assert.IsType(t, &envpkg.NetworkStep{}, step)
}

func TestFromState_KeyStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"key_id":      "key-123",
		"was_created": true,
	}
	step, err := envpkg.Registry["keys"].FromState("key", "my-key", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "key:my-key", step.Name())
	assert.Equal(t, "key", step.Type())
	assert.IsType(t, &envpkg.KeyStep{}, step)
}

func TestFromState_ImageStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"image_id":    "img-123",
		"was_created": true,
	}
	step, err := envpkg.Registry["images"].FromState("image", "alpine", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "image:alpine", step.Name())
	assert.Equal(t, "image", step.Type())
	assert.IsType(t, &envpkg.ImageStep{}, step)
}

func TestFromState_KernelStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"kernel_id":   "krnl-123",
		"was_created": true,
	}
	step, err := envpkg.Registry["kernels"].FromState("kernel", "fc-kernel", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "kernel:fc-kernel", step.Name())
	assert.Equal(t, "kernel", step.Type())
	assert.IsType(t, &envpkg.KernelStep{}, step)
}

func TestFromState_BinaryStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"binary_id":   "bin-123",
		"was_created": true,
	}
	step, err := envpkg.Registry["binaries"].FromState("binary", "firecracker", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "binary:firecracker", step.Name())
	assert.Equal(t, "binary", step.Type())
	assert.IsType(t, &envpkg.BinaryStep{}, step)
}

func TestFromState_VMStep_CorrectType(t *testing.T) {
	saved := map[string]any{
		"vm_id":       "vm-123",
		"vm_dir":      "/mnt/vms/vm-123",
		"was_created": true,
	}
	step, err := envpkg.Registry["vms"].FromState("vm", "my-vm", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "vm:my-vm", step.Name())
	assert.Equal(t, "vm", step.Type())
	assert.IsType(t, &envpkg.VMStep{}, step)
}

func TestFromState_SSHStep_CorrectType(t *testing.T) {
	saved := map[string]any{"command": "apt update", "was_run": true}
	step, err := envpkg.Registry["ssh"].FromState("ssh", "install-qemu", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "ssh:install-qemu", step.Name())
	assert.Equal(t, "ssh", step.Type())
	assert.IsType(t, &envpkg.SSHStep{}, step)
}

func TestFromState_CopyStep_CorrectType(t *testing.T) {
	saved := map[string]any{"source": "./mvm", "was_run": true}
	step, err := envpkg.Registry["copy"].FromState("copy", "copy-binary", saved, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "copy:copy-binary", step.Name())
	assert.Equal(t, "copy", step.Type())
	assert.IsType(t, &envpkg.CopyStep{}, step)
}

// ─── ResolveSpec ──────────────────────────────────────────────────────────────

// Rationale: ResolveSpec must read a YAML spec, validate it, and convert each
// entry into a workflow.Step using the appropriate Registry factory. Invalid
// version (missing) and unknown step types must produce errors.

func TestResolveSpec_ValidSpecReturnsAllSteps(t *testing.T) {
	specContent := `
version: "1"
networks:
  - name: my-net
    subnet: 10.0.0.0/24
keys:
  - name: my-key
images:
  - name: alpine
    type: alpine
    version: "3.21"
kernels:
  - name: fc-kernel
    type: firecracker
    version: 1.15.1
binaries:
  - name: firecracker
    version: 1.15.1
vms:
  - name: my-vm
    network: my-net
    key: my-key
    image: alpine
    kernel: fc-kernel
    binary: firecracker
    vcpu: 2
    mem: 512M
    disk_size: 2G
`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 6)

	stepByName := make(map[string]string, len(steps))
	for _, step := range steps {
		stepByName[step.Name()] = step.Name()
	}
	expected := []string{
		"network:my-net",
		"key:my-key",
		"image:alpine",
		"kernel:fc-kernel",
		"binary:firecracker",
		"vm:my-vm",
	}
	for _, want := range expected {
		_, ok := stepByName[want]
		assert.True(t, ok, "expected step %q not found", want)
	}
}

// Rationale: ResolveSpec must parse VM step dependencies from the spec so that
// the VM step correctly depends on network, key, image, kernel, and binary steps.
func TestResolveSpec_VMStepHasDependencies(t *testing.T) {
	specContent := `
version: "1"
networks:
  - name: my-net
    subnet: 10.0.0.0/24
keys:
  - name: my-key
images:
  - name: alpine
    type: alpine
    version: "3.21"
kernels:
  - name: fc-kernel
    type: firecracker
    version: 1.15.1
binaries:
  - name: firecracker
    version: 1.15.1
vms:
  - name: full-vm
    network: my-net
    key: my-key
    image: alpine
    kernel: fc-kernel
    binary: firecracker
`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 6)

	// Find the VM step by name
	var vmStep workflow.Step
	for _, s := range steps {
		if s.Name() == "vm:full-vm" {
			vmStep = s
			break
		}
	}
	require.NotNil(t, vmStep, "expected VM step not found")
	deps := vmStep.Dependencies()
	require.Len(t, deps, 5)

	expectedDeps := []string{
		"network:my-net",
		"key:my-key",
		"image:alpine",
		"kernel:fc-kernel",
		"binary:firecracker",
	}
	assert.ElementsMatch(t, expectedDeps, deps)
}

// Rationale: An empty spec (only version, no resources) must produce zero steps,
// not an error or nil pointer dereference.
func TestResolveSpec_EmptySpecFile(t *testing.T) {
	specContent := `version: "1"`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	assert.Len(t, steps, 0)
}

// Rationale: ResolveSpec must reject a spec with a missing version field,
// since version is required for schema validation.
func TestResolveSpec_MissingVersion(t *testing.T) {
	specContent := `
networks:
  - name: test
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	_, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

// Rationale: ResolveSpec must return ErrSpecNotFound when the spec file does
// not exist, rather than panicking or returning an unhelpful error.
func TestResolveSpec_SpecFileNotFound(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "nonexistent.yaml")

	_, err := envpkg.ResolveSpec(context.Background(), missingPath, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// Rationale: ResolveSpec must return a validation error when the spec file
// contains invalid YAML, not silently return partial results.
func TestResolveSpec_InvalidYAML(t *testing.T) {
	specPath := writeSpec(t, "version: \"1\"\nnetworks:\n  - invalid_yaml: [")
	_, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.Error(t, err)
}

// Rationale: ResolveSpec must accept the context (currently unused but reserved)
// and still function correctly when context is cancelled. The function does not
// yet use the context for cancellation, so a cancelled context must not error.
func TestResolveSpec_ContextCancellation(t *testing.T) {
	specContent := `version: "1"
networks:
  - name: my-net
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel

	steps, err := envpkg.ResolveSpec(ctx, specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, "network:my-net", steps[0].Name())
}

// NOTE: This test mutates the package-level Registry. Do NOT add t.Parallel().

// Rationale: ResolveSpec must silently skip step types not in the Registry.
// The Registry is the source of truth — unknown types in YAML are ignored.
func TestResolveSpec_UnknownStepType(t *testing.T) {
	origFactory := envpkg.Registry["networks"]
	t.Cleanup(func() { envpkg.Registry["networks"] = origFactory })
	delete(envpkg.Registry, "networks")

	specContent := `version: "1"
networks:
  - name: test
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	assert.Len(t, steps, 0, "no steps should be created when the type is not in Registry")
}

// NOTE: This test mutates the package-level Registry. Do NOT add t.Parallel().

// Rationale: ResolveSpec must propagate errors returned by a factory function —
// if a StepFactory's FromSpec returns an error, ResolveSpec must return it to
// the caller rather than continuing or panicking.
func TestResolveSpec_FactoryFromSpecError(t *testing.T) {
	origFactory := envpkg.Registry["networks"]
	t.Cleanup(func() { envpkg.Registry["networks"] = origFactory })

	errFactory := errors.New("from spec failed")
	envpkg.Registry["networks"] = envpkg.StepFactory{
		StepType: "network",
		FromSpec: func(_, _ string, _ model.ResourceSpec, _ *api.Operation) (workflow.Step, error) {
			return nil, errFactory
		},
		FromState: origFactory.FromState,
	}

	specContent := `version: "1"
networks:
  - name: test
    subnet: 10.0.0.0/24
`
	specPath := writeSpec(t, specContent)

	_, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errFactory)
}

// Rationale: ResolveSpec must honor explicit depends_on in YAML resources.
// A key with depends_on: [network:my-net] should produce a key step that
// depends on the network step by its canonical singular-prefixed name.
func TestResolveSpec_ExplicitDependsOn(t *testing.T) {
	specContent := `
version: "1"
networks:
  - name: my-net
    subnet: 10.0.0.0/24
keys:
  - name: my-key
    depends_on:
      - network:my-net
`
	specPath := writeSpec(t, specContent)
	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 2)

	var keyStep workflow.Step
	var netStep workflow.Step
	for _, s := range steps {
		if s.Name() == "key:my-key" {
			keyStep = s
		}
		if s.Name() == "network:my-net" {
			netStep = s
		}
	}
	require.NotNil(t, netStep, "expected network step")
	require.NotNil(t, keyStep, "expected key step")

	assert.Empty(t, netStep.Dependencies())

	deps := keyStep.Dependencies()
	require.Len(t, deps, 1)
	assert.Equal(t, "network:my-net", deps[0])
}

// ─── Helper Functions ──────────────────────────────────────────────────────────

// Rationale: FormatStepName produces a display name in "type:name" format
// that is used as the canonical step identifier throughout the workflow engine.
func TestFormatStepName_Format(t *testing.T) {
	result := envpkg.FormatStepName("network", "my-net")
	assert.Equal(t, "network:my-net", result)
}

// Rationale: InferStepType extracts the step type from a "type:name" formatted step name.
func TestInferStepType_ExtractsType(t *testing.T) {
	assert.Equal(t, "network", envpkg.InferStepType("network:my-net"))
}

// Rationale: InferStepType falls back to "unknown" when the step name has no colon separator,
// avoiding a panic or empty string.
func TestInferStepType_Fallback(t *testing.T) {
	assert.Equal(t, "unknown", envpkg.InferStepType("no-colon"))
}

// Rationale: BareStepName strips the "type:" prefix from a full step name,
// returning the bare resource name.
func TestBareStepName_ExtractsName(t *testing.T) {
	result := envpkg.BareStepName("network:my-net", "network")
	assert.Equal(t, "my-net", result)
}

// Rationale: BareStepName returns the input unchanged when there is no matching prefix.
func TestBareStepName_NoPrefix(t *testing.T) {
	result := envpkg.BareStepName("my-net", "network")
	assert.Equal(t, "my-net", result)
}

// Rationale: LookupFactoryByStepType must find the correct factory by singular step type.
func TestLookupFactoryByStepType_FindsFactory(t *testing.T) {
	factory, ok := envpkg.LookupFactoryByStepType("network")
	require.True(t, ok)
	require.NotNil(t, factory.FromSpec)
}

// Rationale: LookupFactoryByStepType must return false for unknown step types.
func TestLookupFactoryByStepType_NotFound(t *testing.T) {
	_, ok := envpkg.LookupFactoryByStepType("nonexistent")
	assert.False(t, ok)
}

// Rationale: ResolveWorkflowID must hash file paths containing "/" into a 16-hex-char ID.
func TestResolveWorkflowID_HashesFilePath(t *testing.T) {
	id := envpkg.ResolveWorkflowID("/tmp/my-spec.yaml")
	assert.Len(t, id, 16, "expected 16-char hex ID from file path")
}

// Rationale: ResolveWorkflowID must return a raw ID unchanged when no matching
// directory exists (no prefix match possible).
func TestResolveWorkflowID_ReturnsUnknownIDAsIs(t *testing.T) {
	id := envpkg.ResolveWorkflowID("nonexistent")
	assert.Equal(t, "nonexistent", id)
}

// Rationale: ResolveWorkflowID must find a workflow directory by prefix when
// a matching state directory exists.
func TestResolveWorkflowID_PrefixMatch(t *testing.T) {
	// Create a workflow state directory with a full 16-char ID
	fullID := "ec7299aabbccddee"
	stateDir := infra.GetWorkflowsStateDirByID(fullID)
	// Clean up any pre-existing workflow state dirs that prefix match
	statesDir := infra.GetWorkflowsStateDir()
	if entries, err := os.ReadDir(statesDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "ec72") {
				os.RemoveAll(filepath.Join(statesDir, e.Name()))
			}
		}
	}
	require.NoError(t, os.MkdirAll(stateDir, 0755))
	t.Cleanup(func() { os.RemoveAll(stateDir) })

	// Prefix "ec72" should resolve to the full ID
	id := envpkg.ResolveWorkflowID("ec72")
	assert.Equal(t, fullID, id)
}

// ─── SSH & Copy Steps ─────────────────────────────────────────────────────────

// Rationale: SSH step with a key reference should set SSHInput.Key correctly.
func TestFromSpec_SSHStep_WithKey(t *testing.T) {
	spec := map[string]any{
		"name":   "install-qemu",
		"target": "fenv-vm-1",
		"user":   "root",
		"key":    "fenv-key",
		"cmd":    "apt update",
	}
	step, err := envpkg.Registry["ssh"].FromSpec("ssh", "install-qemu", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "ssh:install-qemu", step.Name())
	assert.Equal(t, "ssh", step.Type())
	assert.IsType(t, &envpkg.SSHStep{}, step)
}

// Rationale: Copy step with single-string src and explicit target+dst must
// build a valid CPInput with Sources=[src] and Dst=target:dst.
func TestFromSpec_CopyStep_BuildsDst(t *testing.T) {
	spec := map[string]any{
		"name":   "copy-binary",
		"target": "fenv-vm-1",
		"user":   "root",
		"src":    "./mvm",
		"dst":    "/root/",
	}
	step, err := envpkg.Registry["copy"].FromSpec("copy", "copy-binary", spec, nil)
	require.NoError(t, err)
	assert.Equal(t, "copy:copy-binary", step.Name())
	assert.Equal(t, "copy", step.Type())
	assert.IsType(t, &envpkg.CopyStep{}, step)
}

// Rationale: ResolveSpec must parse ssh and copy entries from a YAML spec
// and produce the correct number of steps with correct dependencies.
func TestResolveSpec_WithSSHAndCopy(t *testing.T) {
	specContent := `
version: "1"
networks:
  - name: my-net
    subnet: 10.0.0.0/24
keys:
  - name: my-key
vms:
  - name: my-vm
    network: my-net
    key: my-key
ssh:
  - name: run-update
    target: my-vm
    user: root
    cmd: apt update
    depends_on:
      - vm:my-vm
copy:
  - name: copy-binary
    target: my-vm
    user: root
    src: ./mvm
    dst: /root/
    depends_on:
      - vm:my-vm
`
	specPath := writeSpec(t, specContent)
	steps, err := envpkg.ResolveSpec(context.Background(), specPath, nil)
	require.NoError(t, err)
	require.Len(t, steps, 5)

	stepByName := make(map[string]workflow.Step)
	for _, s := range steps {
		stepByName[s.Name()] = s
	}

	// Verify ssh step exists and depends on VM
	sshStep, ok := stepByName["ssh:run-update"]
	require.True(t, ok, "expected ssh step")
	assert.Equal(t, "ssh", sshStep.Type())
	assert.Contains(t, sshStep.Dependencies(), "vm:my-vm")

	// Verify copy step exists and depends on VM
	copyStep, ok := stepByName["copy:copy-binary"]
	require.True(t, ok, "expected copy step")
	assert.Equal(t, "copy", copyStep.Type())
	assert.Contains(t, copyStep.Dependencies(), "vm:my-vm")
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// writeSpec writes YAML content to a temp file and returns the path.
func writeSpec(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(content), 0644), "failed to write spec file")
	return specPath
}
