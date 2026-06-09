# IMPROVEMENTS 011: Environment/Workflow Engine — Portable VM Specs

**Status:** Design Draft  
**Goal:** Replace brittle `vm export/import` with a generic environment workflow engine supporting declarative YAML specs, DAG-based parallel resource provisioning, and stateful destroy.

---

## 1. Motivation

The current `vm export/import` is brittle: import fails unless every asset (image, kernel, binary, network) is manually pre-provisioned. The original vision — share a single file that materialises a complete environment on any machine — was never delivered.

This design replaces export/import with a **workflow engine**:

```
mvm env apply spec.yaml      → provisions everything, tracks state
mvm env ls                    → list applied environments
mvm env destroy <id|path>    → tears down exactly what was provisioned
```

The engine is generic (lives in `internal/lib/workflow/`), domain-agnostic, and reusable. The environment layer (`internal/workflow/env/`) provides concrete steps for each mvmctl resource type. Users can register custom steps with arbitrary state for their own provisioning logic.

---

## 2. Architecture Overview

```
internal/cli/env.go        ← cobra commands → pkg/api.Operation
    |                           + directly calls env.Apply/Destroy/List
    |
internal/workflow/env/     ← spec resolver, step implementations, Apply/Destroy/List
    |  imports: pkg/api (calls *api.Operation directly — no interface)
    |  imports: internal/lib/workflow, internal/lib/model
    |  zero circular deps: never imported by pkg/api/
    |
internal/lib/workflow/     ← generic engine: DAG walker, state persistence, progress
    |  imports: internal/lib/model only
    |  zero domain knowledge
    |
internal/lib/model/        ← ResourceSpec (type with GetString/GetBool/GetInt),
                              SavedResource, WorkflowState (shared types)
```

### Key design decision: no Operation interface

The `env` package is a pure consumer of the API layer — exactly like the CLI. It lives under `internal/` (not `pkg/api/`) so it can import `pkg/api/` without creating a circular dependency.

Steps hold a `*api.Operation` reference directly. There is no `Operation` interface, no mock, no indirection. The `Operation` interface that previously lived in `pkg/api/env/operation.go` was deleted — it was only a circular dependency workaround.

### Registry keys: plural YAML ↔ singular StepType

A key design choice: **Registry keys are plural** (matching YAML spec keys like `networks:`, `keys:`), while **`StepFactory.StepType` is singular** (the step's identity, e.g. `"network"`, `"vm"`).

```
YAML key:     networks:
Registry key: "networks"    (plural, matches YAML for direct lookup)
StepType:     "network"     (singular, returned by step.Type())
State StepType: "network"   (singular, stored from step.Type())
Step name:    "network:my-net" (singular, FormatStepName(stepType, name))
```

The `Destroy` path bridges the gap: state files store `StepType` (singular), but Registry is keyed by YAML keys (plural). The helper `lookupFactoryByStepType()` scans Registry to find the factory whose `StepType` matches the saved value.

---

## 3. The Engine — `internal/lib/workflow/`

### 3.1 Step Interface

```go
// Step is the unit of provisioning. One instance per resource in the spec.
type Step interface {
    // Name returns a unique identifier within this workflow, e.g. "network:default", "vm:dev-vm".
    Name() string

    // Type returns the singular step type identifier (e.g. "network", "vm", "ssh").
    // Stored as StepType in state files for registry lookups during destroy.
    Type() string

    // Dependencies returns names of steps that must complete before this one.
    Dependencies() []string

    // Apply provisions the resource. Called in topological order.
    // The saved parameter contains previously persisted state data for this
    // step (from a prior workflow execution). Steps can use it to detect
    // re-apply and preserve flags like WasCreated. If nil, this is a fresh
    // execution.
    Apply(ctx context.Context, state *SharedState, saved ResourceSpec) error

    // Destroy tears down the resource using data from the saved state.
    // Called in reverse topological order during env destroy.
    Destroy(ctx context.Context, saved ResourceSpec) error

    // StateData returns the opaque data to persist for this step.
    // This is passed back to Destroy() during env destroy.
    StateData() ResourceSpec
}
```

### 3.2 SharedState

SharedState is the shared context passed through the pipeline. Steps read and write to it for cross-step data sharing (e.g., a network step stores created `NetworkID`, VM step reads it).

```go
// Shared mutable state across all steps in a workflow.
type SharedState struct {
    mu   sync.RWMutex
    data map[string]any   // step_name → any (cross-step data sharing)
}

func (s *SharedState) Set(stepName string, value any)
func (s *SharedState) Get(stepName string) (any, bool)
```

### 3.3 Pipeline

```go
// Pipeline executes steps in DAG order with parallelisation.
type Pipeline struct {
    steps  []Step
    levels [][]Step        // topological levels — steps within a level run in parallel
}

// NewPipeline validates and sorts steps into levels.
func NewPipeline(steps []Step) (*Pipeline, error)

// Execute walks the DAG level by level. Steps within a level run in parallel.
// Calls Apply on each step. The savedResources parameter contains state data
// from a prior workflow execution — steps use it to detect re-apply and
// preserve flags like WasCreated. Pass nil or an empty slice for fresh
// executions.
func (p *Pipeline) Execute(ctx context.Context, state *SharedState,
    onProgress func(phase, status, msg string),
    savedResources []SavedResource,
    opts ...ExecuteOption) error

// Destroy runs Destroy on each step in reverse topological order.
// The step decides internally what to do based on its saved state.
func (p *Pipeline) Destroy(ctx context.Context,
    savedResources []SavedResource,
    onProgress func(phase, status, msg string)) error
```

**Execute options:**

```go
// WithStepCompleteCallback registers a callback invoked after each step's
// Apply completes successfully. The callback receives the step name and its
// StateData at the time of completion. This is called from the step's
// goroutine so the callback must be thread-safe. Used by the env layer to
// collect state data incrementally for partial-state persistence on failure.
func WithStepCompleteCallback(cb func(stepName string, stateData ResourceSpec)) ExecuteOption
```

### 3.4 DAG Resolver

```go
// BuildDAG topologically sorts steps into levels.
// Returns an error if cycles are detected (with the cycle path).
func BuildDAG(steps []Step) ([][]Step, error)
```

Algorithm:

1. Build adjacency map from `Dependencies()`
2. Kahn's algorithm to produce topological levels
3. Level 0 = no dependencies (networks, keys, images, kernels, binaries, ssh, copy)
4. Level 1 = depends on level 0 (VMs)
5. Detect cycles → error with cycle trace

### 3.5 State Persistence

```go
type SavedResource struct {
    StepName     string       `yaml:"step_name"`
    StepType     string       `yaml:"step_type"`
    Dependencies []string     `yaml:"depends_on,omitempty"`
    State        ResourceSpec `yaml:"state,omitempty"`
}

type WorkflowState struct {
    WorkflowID    string          `yaml:"workflow_id"`
    SpecPath      string          `yaml:"spec_path"`
    SchemaVersion string          `yaml:"schema_version"`
    CreatedAt     string          `yaml:"created_at"`
    UpdatedAt     string          `yaml:"updated_at"`
    ContentHash   string          `yaml:"content_hash,omitempty"`
    Resources     []SavedResource `yaml:"resources"`
}
```

Stored at `~/.cache/mvmctl/workflows/<workflow-id>/state.yaml`.

Persistence properties:
- **Atomic writes:** `state.yaml.tmp` → `os.Rename` → `state.yaml` (atomic on POSIX)
- **File locking:** `<state_dir>/.lock` via `unix.Flock` (exclusive on write, shared on read)
- **Content verification:** SHA256 hash of state content stored in `WorkflowState.ContentHash`, verified on read — if present, the hash is validated; files without a ContentHash field are accepted as-is for backward compatibility
- **Backup files:** Previous state is backed to `state.yaml.bak` before overwrite

---

## 4. Workflow ID

```
wf_id = sha256(realpath(spec_file))[:16]    // 8 bytes, 16 hex chars
```

- Deterministic per file path
- Same path on different machines → different IDs (local state, no conflict)
- Editing the spec and re-applying → same ID → reconcile (skip existing, create new)
- Renaming/moving the file → new ID, old state orphaned

### Prefix matching for destroy

`ResolveWorkflowID()` accepts a partial workflow ID prefix for destroy. If the input looks like a file path (contains `/`, `\`, or `.`), it is treated as a spec path and hashed. Otherwise it first tries an exact match against existing state directories. If that fails, it scans state directories for a prefix match — allowing `mvm env destroy abc123` to match a full ID like `abc123def4567890`.

A `mvm env clean` command can be added later to prune orphaned state directories.

---

## 5. The Env Layer — `internal/workflow/env/`

### 5.1 Step Factory & Registry

```go
// StepFactory creates a Step from either a spec entry or saved state.
// The map key in Registry is the plural YAML key (e.g. "networks").
// StepType is the singular step type identifier returned by step.Type().
type StepFactory struct {
    StepType  string
    FromSpec  func(stepType, name string, spec ResourceSpec, op *api.Operation) (workflow.Step, error)
    FromState func(stepType, name string, saved ResourceSpec, op *api.Operation) (workflow.Step, error)
}

// Registry is a package-level map literal in factory.go.
// Map keys are plural YAML keys matching env spec files directly.
// StepType is the singular identifier used by step.Type() and state files.
// No init() calls, no side effects — all step types visible in one place.
var Registry = map[string]StepFactory{
    "networks": {StepType: "network", FromSpec: newNetworkStepFromSpec, FromState: newNetworkStepFromState},
    "keys":     {StepType: "key",     FromSpec: newKeyStepFromSpec,     FromState: newKeyStepFromState},
    "images":   {StepType: "image",   FromSpec: newImageStepFromSpec,   FromState: newImageStepFromState},
    "kernels":  {StepType: "kernel",  FromSpec: newKernelStepFromSpec,  FromState: newKernelStepFromState},
    "binaries": {StepType: "binary",  FromSpec: newBinaryStepFromSpec,  FromState: newBinaryStepFromState},
    "vms":      {StepType: "vm",      FromSpec: newVMStepFromSpec,      FromState: newVMStepFromState},
    "ssh":      {StepType: "ssh",     FromSpec: newSSHStepFromSpec,     FromState: newSSHStepFromState},
    "copy":     {StepType: "copy",    FromSpec: newCopyStepFromSpec,    FromState: newCopyStepFromState},
}
```

Key points:
- `op` parameter is `*api.Operation` (concrete type, not an interface)
- Factory functions never call methods on `op` during construction — they only store the reference
- This means tests can pass `nil` for `op` during step construction testing
- No `Operation` interface exists — steps are coupled to the concrete `api.Operation` struct

### 5.2 Step Implementations

All steps follow these common patterns:

1. **`depends_on` support**: Every `FromSpec` factory calls `extractDependsOn(spec)` to read explicit `depends_on` from the YAML entry. The helper returns a `[]string` of full step names (e.g. `"network:my-net"`), or nil if the field is missing.

2. **Nil guards**: Every `Apply()` and `Destroy()` checks `s.op == nil` at the top and returns an error immediately.

3. **Re-apply detection**: Every `Apply(ctx, state, saved)` reads the `saved` parameter (previous state from a prior workflow execution). If `saved != nil`, it reconstructs the previous state via `StateFromMap[T]()` and preserves the `WasCreated` flag. If the resource already exists (checked via `op.Repos`), `WasCreated` is carried forward from the previous state — ensuring destroy correctly skips resources that were pre-existing at the time of first apply.

4. **`Name()`**: `FormatStepName(s.stepType, s.name)` → `"network:my-net"`

5. **`Type()`**: Returns the singular step type string (e.g. `"network"`, `"vm"`, `"ssh"`), which is persisted as `StepType` in the workflow state file.

---

**NetworkStep**

```go
type NetworkStep struct {
    stepType string
    name     string
    deps     []string
    input    inputs.NetworkCreateInput
    op       *api.Operation
    saved    *NetworkState
}

type NetworkState struct {
    NetworkID   string `yaml:"network_id"`
    Subnet      string `yaml:"subnet"`
    WasCreated  bool   `yaml:"was_created"`
}

func (s *NetworkStep) Apply(ctx, state, saved) error {
    // Nil guard: s.op == nil → error
    // If saved != nil, reconstruct previous state via StateFromMap[NetworkState](saved)
    // Check if network exists via op.Repos.Network.GetByName(...)
    // → skip if exists, preserve WasCreated from previous state
    // Not found → op.NetworkCreate(...)
    // Store network state in shared state for downstream steps (via state.Set)
}

func (s *NetworkStep) Destroy(ctx, saved) error {
    // Nil guard: s.op == nil → error
    // If WasCreated is false, skip (was pre-existing)
    // If WasCreated is true → op.NetworkRemove(...)
}

func (s *NetworkStep) StateData() model.ResourceSpec {
    return StructToMap(s.saved)
}

func newNetworkStepFromSpec(stepType, name, spec, op) {
    // YAML unmarshal into NetworkCreateInput
    // extractDependsOn(spec) for explicit deps
    // Return step
}
```

---

**KeyStep**

```go
type KeyStep struct {
    stepType string
    name     string
    deps     []string
    input    inputs.KeyCreateInput
    op       *api.Operation
    saved    *KeyState
}

type KeyState struct {
    KeyID      string `yaml:"key_id"`
    WasCreated bool   `yaml:"was_created"`
}

func (s *KeyStep) Apply(ctx, state, saved) error {
    // Same pattern: check existence → skip or create
    // Re-apply: preserve WasCreated from saved state
}
```

---

**ImageStep, KernelStep, BinaryStep** follow the same pattern:
- Existence check via `op.Repos` (e.g., `op.Repos.Image.GetByType(...)`)
- If exists → skip, preserve `WasCreated` from previous state
- If not → pull via `op.ImagePull(...)`, `op.KernelPull(...)`, `op.BinaryPull(...)`
- Destroy is a no-op for Image/Kernel/Binary (they persist in the DB)
- All have nil guards on `s.op == nil`

---

**VMStep**

```go
type VMStep struct {
    stepType string
    name     string
    deps     []string
    input    inputs.VMCreateInput
    op       *api.Operation
    saved    *VMState
}

type VMState struct {
    VMID        string `yaml:"vm_id"`
    VMDir       string `yaml:"vm_dir"`
    NocloudPort int    `yaml:"nocloud_port,omitempty"`
    TapName     string `yaml:"tap_name,omitempty"`
    WasCreated  bool   `yaml:"was_created"`
}

func (s *VMStep) Dependencies() []string {
    // Deduplicates explicit depends_on against inferred deps
    // from reference fields (network, key, image, kernel, binary)
    // Uses a seen-set to avoid duplicates
}

func (s *VMStep) Apply(ctx, state, saved) error {
    // Nil guard
    // Read resolved dependency IDs from shared state
    // (NetworkStep stores NetworkID, KeyStep stores KeyID, etc.)
    // Check if VM already exists → skip, preserve WasCreated
    // Not found → op.VMCreate(resolvedInput)
}
```

**Dependency deduplication in VMStep:**

The VM step's `Dependencies()` performs deduplication against the explicit `depends_on` entries (from `extractDependsOn()`) and the inferred dependencies from reference fields (`network`, `key`, `image`, `kernel`, `binary`). If a user writes:

```yaml
vms:
  - name: dev-vm
    network: default
    depends_on:
      - network:default
```

The dependency `network:default` is not duplicated — the seen-set ensures it appears once.

---

**SSHStep** — Imperative, always re-runs on re-apply

```go
type SSHStep struct {
    stepType string
    name     string
    deps     []string
    input    inputs.SSHInput
    op       *api.Operation
    saved    *SSHState
}

type SSHState struct {
    Command string `yaml:"command"`
    WasRun  bool   `yaml:"was_run"`
}

func (s *SSHStep) Apply(ctx, state, saved) error {
    // Nil guard
    // SSH commands are imperative — always execute on apply (no skip check)
    // op.SSHConnect(ctx, input)
}

func (s *SSHStep) Destroy(ctx, saved) error {
    // SSH commands are ephemeral — no teardown needed
    return nil
}
```

---

**CopyStep** — Imperative, always re-runs on re-apply

```go
type CopyStep struct {
    stepType string
    name     string
    deps     []string
    input    inputs.CPInput
    op       *api.Operation
    saved    *CPState
}

type CPState struct {
    Source string `yaml:"source"`
    WasRun bool   `yaml:"was_run"`
}

func (s *CopyStep) Apply(ctx, state, saved) error {
    // Nil guard
    // Copy commands are imperative — always execute on apply (no skip check)
    // op.CPCopy(ctx, input, nil)
}

func (s *CopyStep) Destroy(ctx, saved) error {
    // File copies are ephemeral — no teardown needed
    return nil
}
```

**Copy Dst construction:** The YAML spec uses separate `target` (VM name) and `dst` (remote path) fields. The `FromSpec` factory builds the `CPInput.Dst` as `target + ":" + dst` — matching the `vm:path` format expected by the cp operation.

**SSH and Copy are always re-run** on re-apply. They are imperative steps with no existence check — unlike DB-backed resources (networks, keys, VMs) that check `op.Repos` and skip if the resource already exists.

---

### 5.3 Spec Resolver

`EnvSpec` uses a dynamic `Steps` map keyed by Registry keys, decoded via custom `UnmarshalYAML` that checks each YAML key against `Registry`:

```go
type EnvSpec struct {
    Version string                         `yaml:"version"`
    Steps   map[string][]model.ResourceSpec `yaml:"-"` // populated by UnmarshalYAML
}

// UnmarshalYAML decodes a YAML mapping into EnvSpec. The "version" key is
// decoded explicitly; all remaining keys that match an entry in Registry
// are decoded as []model.ResourceSpec and stored in Steps.
func (s *EnvSpec) UnmarshalYAML(value *yaml.Node) error
```

Resolution flow:

1. Read YAML file → call `yaml.Unmarshal` → custom `UnmarshalYAML` populates `Steps`:
   - Extract `version` explicitly
   - For each remaining YAML key, check if `Registry[key]` exists
   - If found, decode the value as `[]model.ResourceSpec` and store in `Steps[key]`
   - Unknown keys are silently ignored
2. Validate `Version` — must be `"1"` (the only supported version). Returns error for unknown versions.
3. For each entry in `Registry`, look up matching step list from `spec.Steps[yamlKey]`
   - Pass each spec entry through `factory.FromSpec(factory.StepType, name, entry, op)`
   - `factory.StepType` is the singular step type name (e.g. "network")
4. Collect all steps → feed to `workflow.NewPipeline()`

No per-resource-type fields needed on `EnvSpec` — the `Registry` is the schema. Adding a new step type means adding one entry to `Registry` (with `StepType`, `FromSpec`, `FromState`) and one `step_*.go` file.

### 5.4 Apply/Destroy/List — Standalone Functions

The orchestration layer lives in `internal/workflow/env/env.go` as standalone functions — **not** methods on `api.Operation`. This is the key architectural difference from the original design.

```go
// Apply provisions everything in the spec file.
// - Resolves spec → steps
// - Builds pipeline, executes
// - Persists partial state on failure so destroy can still clean up
func Apply(ctx context.Context, op *api.Operation, specPath string,
    onProgress event.OnProgressCallback) error
```

Apply flow:
1. `ResolveSpec(ctx, specPath, op)` → `[]workflow.Step`
2. Build `stepTypeByStepName` map from `step.Type()` (singular) for callback lookups
3. `workflow.NewPipeline(steps)` → validates and topologically sorts
4. Derive workflow ID from spec path → `~/.cache/mvmctl/workflows/<wf-id>/`
5. **Read previous workflow state** for re-apply detection: `workflow.ReadWorkflowState(stateDir)` — reads `prevState.Resources` so steps receive their previous `saved` state during `Apply()`.
6. `pipeline.Execute(ctx, state, progressFn, prevResources, opts...)` — passes previous resources for re-apply detection
7. Collect per-step state data from `WithStepCompleteCallback` during execution (thread-safe)
8. Persist `WorkflowState` to state directory — even if Execute fails, completed steps are saved

```go
// Destroy tears down all resources created by a previous Apply.
// Reconstructs steps from saved state (no spec file needed).
// Removes the state file after successful teardown.
func Destroy(ctx context.Context, op *api.Operation, specOrID string,
    onProgress event.OnProgressCallback) error
```

Destroy flow:
1. Resolve the identifier via `ResolveWorkflowID(specOrID)` — supports exact match, prefix match, and path-based hashing
2. Read saved `WorkflowState` from state directory
3. For each `SavedResource`, look up factory by `StepType` via `LookupFactoryByStepType(stepType)`:
   - Scans `Registry` to find factory where `StepType == res.StepType`
   - This bridges the gap between state files (singular) and Registry keys (plural)
4. Extract bare name from step name via `BareStepName(res.StepName, res.StepType)` — strips `"type:"` prefix
5. `factory.FromState(factory.StepType, bareName, res.State, op)` → reconstruct step
6. `workflow.NewPipeline(steps)` → validate
7. `pipeline.Destroy(ctx, savedResources, progressFn)`
8. Remove workflow state directory

```go
// List returns summaries of all saved workflow states.
func List(ctx context.Context) ([]ListSummary, error)

type ListSummary struct {
    WorkflowID string `json:"workflow_id"`
    SpecPath   string `json:"spec_path"`
    CreatedAt  string `json:"created_at"`
    UpdatedAt  string `json:"updated_at"`
    Resources  int    `json:"resources"`
}
```

### 5.5 Step Reconstruction from State

When destroying without the spec file, each step type's `FromState` factory reconstructs a minimal Step instance that only needs `Destroy()` and `StateData()`. The factory reparses the saved state through `StateFromMap[T]()`:

```go
func newNetworkStepFromState(stepType string, name string, saved model.ResourceSpec,
    op *api.Operation) (workflow.Step, error) {
    ns := StateFromMap[NetworkState](saved)
    return &NetworkStep{
        stepType: stepType,
        name:     name,
        op:       op,
        saved:    ns,
    }, nil
}
```

The reconstructed step's `Type()` returns the singular step type, and `Name()` uses `FormatStepName(stepType, name)`.

The reconstructed step uses the `WasCreated` flag to decide whether to tear down:
- `WasCreated: true` → call `op.NetworkRemove(...)` / `op.VMRemove(...)` with the saved IDs
- `WasCreated: false` → skip (resource was pre-existing, not ours to destroy)

For SSH and Copy steps, `Destroy()` is always a no-op — the reconstructed step does nothing with the saved state.

---

## 6. Error Handling

**The engine has no error handling policy.** The step decides everything.

A step's `Apply` returns an error or not — the step controls its own behavior internally. The engine just calls `Apply` and returns the error to the caller. The step may handle cleanup inside `Apply` before returning an error, or leave things dirty — entirely its choice.

During `env destroy`, each step's `Destroy()` decides what to do based on its saved state: tear down, skip, log, error. The engine just calls it.

The engine is a DAG walker. It calls `Apply`, `Destroy`, `StateData`, and persists whatever `StateData` returns. Nothing more.

---

## 7. Migrating Away from vm export/import

### Removal Plan

| File | Action |
|------|--------|
| `src/mvmctl/api/inputs/_vm_export_config.py` | Remove entire file |
| `src/mvmctl/api/inputs/_vm_import_input.py` | Remove entire file |
| `src/mvmctl/api/vm_operations.py` — `export()`, `import_()` | Remove methods |
| `src/mvmctl/cli/vm.py` — `vm_export`, `vm_import` commands | Remove CLI handlers |
| `tests/unit/api/test_vm_export_config.py` | Remove entire file |
| `pkg/api/inputs/vm_export_config.go` | Remove entire file |
| `pkg/api/inputs/vm_import.go` | Remove entire file |
| `pkg/api/vm.go` — `VMExport()`, `VMImport()` | Remove methods |
| `internal/cli/vm.go` — `newVMExportCmd`, `newVMImportCmd` | Remove CLI commands |

### YAML Spec Format

```yaml
# example-env.yaml
version: "1"

networks:
  - name: default
    subnet: "172.27.0.0/24"
    nat: true

keys:
  - name: main-key
    algorithm: ed25519
    bits: 256
    comment: "my-key"

images:
  - name: os-image
    type: alpine
    version: "3.21"

kernels:
  - name: default-kernel
    type: firecracker
    version: "6.1"

binaries:
  - name: firecracker       # yaml: "type" maps to BinaryPullInput.Name
    version: "1.15.1"       # yaml: "version"
    default: true           # yaml: "default" maps to SetDefault
    force: false            # yaml: "force" maps to DownloadOverride

vms:
  - name: dev-vm
    network: default
    key: main-key
    image: os-image
    kernel: default-kernel
    binary: firecracker
    vcpu: 2
    mem: 2048
    disk_size: 10G
    depends_on:             # explicit deps — deduplicated against inferred refs
      - key:main-key

ssh:                        # imperative — always re-run on re-apply
  - name: setup-hostname
    target: dev-vm          # yaml: "target" maps to SSHInput.Identifier
    user: root              # yaml: "user"
    cmd: "hostnamectl set-hostname my-dev-vm"  # yaml: "cmd"
    depends_on:
      - vm:dev-vm

copy:                       # imperative — always re-run on re-apply
  - name: deploy-binary
    target: dev-vm          # combined with dst → CPInput.Dst = "dev-vm:/opt/bin/"
    dst: "/opt/bin/"        # yaml: separate fields
    src: ./mvm              # yaml: "src" maps to CPInput.Sources
    user: root              # yaml: "user"
    key: main-key           # yaml: "key"
    force: true             # yaml: "force"
    depends_on:
      - vm:dev-vm
```

**YAML tag mappings for input types:**

| Input Type | YAML Field | Go Field | Notes |
|-----------|-----------|----------|-------|
| `BinaryPullInput` | `type` | `Name` | Spec uses `type: firecracker` for binary name |
| `BinaryPullInput` | `version` | `Version` | |
| `BinaryPullInput` | `force` | `DownloadOverride` | |
| `BinaryPullInput` | `default` | `SetDefault` | |
| `KeyCreateInput` | `force` | `Overwrite` | |
| `SSHInput` | `target` | `Identifier` | |
| `CPInput` | `src` | `Sources` | Single string auto-normalized to `[]string` |
| `CPInput` | *(none)* | `Dst` | Built from `target` + `:` + `dst` in `FromSpec` |

---

## 8. CLI Surface

```
mvm env apply <spec-path>     # Provision everything in the spec
mvm env ls                    # List applied environments
mvm env destroy <wf-id|path>  # Tear down exactly what was provisioned
                              # Accepts full or prefix workflow ID
```

The `env` group is a new top-level command, defined in `internal/cli/env.go`.

---

## 9. File Layout (Go)

```
internal/lib/workflow/
    step.go              # Step interface, StepFunc adapter
    pipeline.go          # Pipeline struct, Execute (with savedResources param, options), Destroy
    state.go             # SharedState (thread-safe)
    dag.go               # BuildDAG topo-sort
    persist.go           # Read/Write WorkflowState to YAML

internal/lib/model/
    workflow.go          # ResourceSpec (type definition with GetString/GetBool/GetInt methods),
                         # SavedResource, WorkflowState

internal/workflow/env/
    env.go               # Apply, Destroy, List (standalone functions)
    spec.go              # EnvSpec (dynamic Steps map via UnmarshalYAML), ResolveSpec
    factory.go           # StepFactory, Registry (plural keys, StepType field)
    utils.go             # FormatStepName, InferStepType, BareStepName,
                         # StateFromMap, StructToMap, extractDependsOn,
                         # LookupFactoryByStepType
    step_network.go      # NetworkStep
    step_key.go          # KeyStep
    step_image.go        # ImageStep
    step_kernel.go       # KernelStep
    step_binary.go       # BinaryStep
    step_vm.go           # VMStep (with deduplicated Dependencies)
    step_ssh.go          # SSHStep (imperative, always re-run)
    step_copy.go         # CopyStep (imperative, always re-run)
    env_test.go          # Black-box tests (package env_test)

internal/cli/
    env.go               # newEnvApplyCmd, newEnvListCmd, newEnvDestroyCmd
```

Note: `pkg/api/` no longer contains any env-related files. The old `pkg/api/env/` directory and `pkg/api/env.go` have been deleted. The `env` package is purely under `internal/`.

Note: `internal/lib/util/` has been removed entirely. `StateFromMap` and `StructToMap` now live in `internal/workflow/env/utils.go`.

---

## 10. V1 Scope

| Feature | Status |
|---------|--------|
| Step interface (Apply/Destroy/StateData with Type() and saved param) | ✅ |
| DAG resolver (topological sort, cycle detection) | ✅ |
| SharedState (thread-safe, cross-step data sharing) | ✅ |
| Pipeline.Execute (sequential levels, parallel within level, savedResources param) | ✅ |
| Pipeline.Destroy (reverse level order) | ✅ |
| State persistence (atomic write, file locking, content hash, backup) | ✅ |
| Path-based workflow ID (16 hex chars) | ✅ |
| Prefix matching for workflow ID on destroy | ✅ |
| Dynamic YAML spec resolver — EnvSpec uses Steps map via UnmarshalYAML, Registry is the schema | ✅ |
| Step factory registry (plural YAML keys + singular StepType field) | ✅ |
| `depends_on` support on all step types via extractDependsOn helper | ✅ |
| VM step deduplicates explicit deps against inferred ref deps | ✅ |
| ResourceSpec type definition with GetString/GetBool/GetInt methods | ✅ |
| StateFromMap/StructToMap moved from internal/lib/util/ to env/utils.go | ✅ |
| StateFromMap logging (slog.Error on marshal/unmarshal failure) | ✅ |
| Nil guards (s.op == nil) on every Apply() and Destroy() | ✅ |
| Re-apply detection: Apply(ctx, state, saved) preserves WasCreated from previous state | ✅ |
| NetworkStep (apply: op.NetworkCreate, destroy: op.NetworkRemove, skip if exists) | ✅ |
| KeyStep (apply: op.KeyCreate, destroy: op.KeyRemove, skip if exists) | ✅ |
| ImageStep (apply: op.ImagePull, destroy: skip) | ✅ |
| KernelStep (apply: op.KernelPull, destroy: skip) | ✅ |
| BinaryStep (apply: op.BinaryPull, destroy: skip) | ✅ |
| VMStep (apply: op.VMCreate, destroy: op.VMRemove, skip if exists) | ✅ |
| SSHStep (apply: op.SSHConnect, destroy: no-op, always re-run) | ✅ |
| CopyStep (apply: op.CPCopy, destroy: no-op, always re-run) | ✅ |
| Step reconstruction from state for spec-less destroy | ✅ |
| YAML tags on all *CreateInput / *PullInput / SSHInput / CPInput types | ✅ |
| No Operation interface — steps use *api.Operation directly | ✅ |
| Env package moved to internal/ (consumer of API, like CLI) | ✅ |
| `mvm env apply` CLI command | ✅ |
| `mvm env ls` CLI command | ✅ |
| `mvm env destroy` CLI command | ✅ |
| Remove Python vm export/import | ✅ |
| Remove Go vm export/import | ✅ |
| `mvm env sync` (reconcile existing env) | 🟡 V1.1 |
| `mvm env diff` (show pending changes) | ❌ Future |
| In-place spec update (Update on Step) | ❌ Future |
| Multi-VM compose (cross-VM references) | ❌ Future |

---

## 11. Build Plan (Recommended Order)

1. **Engine**: `internal/lib/workflow/` — Step interface, DAG resolver, Pipeline, SharedState, state persistence. Test with mock steps.
2. **Step implementations**: `internal/workflow/env/` — 8 step types, registry, spec resolver. Test each step in isolation.
3. **Orchestration**: `internal/workflow/env/env.go` — `Apply`, `Destroy`, `List` standalone functions.
4. **CLI**: `internal/cli/env.go` — cobra commands, output formatting.
5. **Cleanup**: Remove Python and Go vm export/import files.
6. **Package move**: Move from `pkg/api/env/` to `internal/workflow/env/`, delete `Operation` interface.
7. **System tests**: Write black-box tests for the full `apply → ls → destroy` cycle.
