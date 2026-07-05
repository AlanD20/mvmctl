# Environment Workflow Engine — `mvm env`

## Problem

Declarative environment management allows users to define a complete VM environment (networks, keys, images, kernels, binaries, VMs, and post-boot provisioning steps) in a single YAML spec file. The engine provisions all resources in dependency order, tracks what was created, and tears everything down on destroy. Without it, users must manually provision each resource via separate CLI commands, with no state tracking, no dependency ordering, and no unified destroy.

## Architecture

The workflow engine lives in `internal/lib/workflow/` — a domain-agnostic DAG pipeline. The environment layer (`internal/workflow/env/`) provides concrete step implementations for each mvmctl resource type. The engine is a pure consumer of the API layer, using the `api.API` composite interface — exactly like the CLI.

```
internal/cli/env.go        ← cobra commands → pkg/api.Operation
    |                           + directly calls env.Apply/Destroy/List
    |
internal/workflow/env/     ← spec resolver, step implementations, Apply/Destroy/List
    |  imports: pkg/api (uses api.API composite interface)
    |  imports: internal/lib/workflow, internal/lib/model
    |  zero circular deps: never imported by pkg/api/
    |
internal/lib/workflow/     ← generic engine: DAG walker, state persistence, progress
    |  imports: internal/lib/model only
    |  zero domain knowledge
    |
internal/lib/model/        ← ResourceMap, ResourceMeta, ResourceState, AppliedResource, WorkflowState
```

The `env` package lives under `internal/` (not `pkg/api/`) so it can import `pkg/api/` without creating a circular dependency.

## Entry point

Users invoke the workflow engine via `internal/cli/env.go`, which provides four commands:

- `mvm env apply <spec-path>` — provisions everything in the spec (local file or remote URL)
- `mvm env apply <spec-path> --env KEY=VAL` — with extra env vars for exec steps (repeatable)
- `mvm env apply https://example.com/team-env.yaml` — apply from remote URL
- `mvm env ls` — lists applied environments
- `mvm env diff <spec-path>` — shows what would change (spec vs state)
- `mvm env destroy <wf-id|path>` — tears down exactly what was provisioned

The CLI calls standalone functions in `internal/workflow/env/env.go`: `Apply()`, `Destroy()`, `List()`, `Diff()`. These are NOT methods on `api.Operation` — the env package is a consumer of the API, like the CLI.

## The Engine — `internal/lib/workflow/`

### Step interface

```go
type StateWriter func(ctx context.Context, stateData model.ResourceState) error

type Step interface {
    Name() string
    Type() string
    Dependencies() []string
    Apply(ctx context.Context, state *SharedState, saved model.ResourceState,
        write StateWriter, onProgress event.OnProgressCallback) error
    Destroy(ctx context.Context, saved model.ResourceState,
        write StateWriter, onProgress event.OnProgressCallback) error
    StateData() model.ResourceState
    SpecHash() string
}
```

### Pipeline

The `Pipeline` executes steps in DAG order with parallelization. `NewPipeline()` validates and sorts steps into topological levels using `BuildDAG()` (Kahn's algorithm). Levels are independent: steps within a level run in parallel; levels run sequentially.

- `Pipeline.Execute()` — walks the DAG level by level, calling `Apply` on each step
- `Pipeline.Destroy()` — runs `Destroy` on each step in reverse topological order

Execute options include `WithOnStepComplete` for per-step state persistence callbacks.

### SharedState

Thread-safe shared context across all steps. Steps read from and write to it for cross-step data sharing (e.g., a network step stores created `NetworkID`, VM step reads it).

### State persistence

State is persisted to `~/.cache/mvmctl/workflows/<wf-id>/state.yaml` **per-step**, immediately after each successful API operation, not batched at the end. This narrows the crash window to a single atomic file write.

Each resource entry stores three fields under `state`:
- `spec` — the input spec fields from the YAML (what the user configured)
- `output` — the created resource state (IDs, properties returned by the API)
- `meta` — metadata (was_created, spec_hash for drift detection)

```yaml
workflow_id: "ec729934a8fb9c67"
spec_path: "./my-env.yaml"
schema_version: "1.0"
created_at: "2026-07-05T10:00:00Z"
updated_at: "2026-07-05T10:05:00Z"
resources:
  - name: "network:default"
    type: "network"
    state:
      spec:
        subnet: "172.27.0.0/24"
      output:
        network_id: "net-abc123"
      meta:
        was_created: true
        spec_hash: "a1b2c3d4..."
  - name: "vm:dev-vm"
    type: "vm"
    depends_on: ["network:default"]
    state:
      spec:
        vcpu: 2
        mem: 2048
      output:
        vm_id: "vm-xyz789"
      meta:
        was_created: true
        spec_hash: "e5f6g7h8..."
```

Persistence properties:
- **Atomic writes:** `state.yaml.tmp` → `os.Rename` → `state.yaml`
- **File locking:** `<state_dir>/.lock` via `unix.Flock` (exclusive on write, shared on read)
- **Content verification:** SHA256 hash stored in `WorkflowState.ContentHash`, verified on read
- **Backup files:** Previous state backed to `state.yaml.backup` before overwrite

### Workflow ID

`wf_id = sha256(realpath(spec_file))[:16]` — 8 bytes, 16 hex chars.
- Deterministic per file path
- Same path on different machines → different IDs (local state, no conflict)
- Editing the spec and re-applying → same ID → reconcile (skip existing, create new)

## The Env Layer — `internal/workflow/env/`

### Step Factory & Registry

```go
type StepFactory struct {
    StepType  string
    FromSpec  func(stepType, name string, spec model.ResourceMap, op api.API) (workflow.Step, error)
    FromState func(stepType, name string, saved model.ResourceState, deps []string, op api.API) (workflow.Step, error)
}

var Registry = map[string]StepFactory{
    "network": {StepType: "network", FromSpec: newNetworkStepFromSpec, FromState: newNetworkStepFromState},
    "key":     {StepType: "key",     FromSpec: newKeyStepFromSpec,     FromState: newKeyStepFromState},
    "image":   {StepType: "image",   FromSpec: newImageStepFromSpec,   FromState: newImageStepFromState},
    "kernel":  {StepType: "kernel",  FromSpec: newKernelStepFromSpec,  FromState: newKernelStepFromState},
    "binary":  {StepType: "binary",  FromSpec: newBinaryStepFromSpec,  FromState: newBinaryStepFromState},
    "vm":      {StepType: "vm",      FromSpec: newVMStepFromSpec,      FromState: newVMStepFromState},
    "ssh":     {StepType: "ssh",     FromSpec: newSSHStepFromSpec,     FromState: newSSHStepFromState},
    "exec":    {StepType: "exec",    FromSpec: newExecStepFromSpec,    FromState: newExecStepFromState},
    "copy":    {StepType: "copy",    FromSpec: newCopyStepFromSpec,    FromState: newCopyStepFromState},
}
```

All identifiers are singular — YAML keys, step types, `depends_on`, step names, state files. Registry keys match StepType, so destroy can do a direct `Registry[stepType]` lookup.

### Step categories

| Category | Step Types | Apply Behavior | Destroy Behavior |
|----------|-----------|----------------|------------------|
| **DB-backed** | network, key, vm | Check existence → skip or create | Remove if `WasCreated`, skip if pre-existing |
| **Pull-based** | image, kernel, binary | Check existence → skip or pull | No-op (persist in DB) |
| **Imperative** | ssh, copy, exec | Always execute (no skip check) | No-op (ephemeral) |

### Spec Resolver

`EnvSpec.Steps` is `map[string]map[string]model.ResourceMap` with `yaml:",inline"`. The outer key is the step type, the inner key is the step name. Known scalar fields (`version`, `ephemeral`) are decoded by their struct tags; all remaining top-level YAML keys flow into `Steps` via the inline tag. `resolveSpecV1` filters `Steps` against `Registry` — unknown keys are silently ignored.

At parse time, each entry goes through two normalizations:
1. **`stripSpecRefs()`** — strips the `@` sigil from all string values and string-list entries (e.g., `"@network:default"` → `"network:default"`)
2. **`stripBareName()`** — (per-factory) reduces `"type:name"` → `"name"` for step reference fields
3. **`name` override** — the `name` field in the params optionally overrides the resource name; if absent, the step name (map key) is the resource name

Resolution flow:
1. Read YAML file → `yaml.Unmarshal` → struct tags handle scalar fields, inline map captures step entries
2. Validate `Version` — must be `"1"`
3. For each entry in `Registry`, look up matching step map from `spec.Steps[yamlKey]`
4. Iterate the inner map: key = step name, value = params
5. Strip `@` sigils via `stripSpecRefs()`
6. Pass (stepType, name, params) through `factory.FromSpec()` — each factory normalizes its reference fields
7. Collect all steps → feed to `workflow.NewPipeline()`

### VM step — dependency deduplication

The VM step's `Dependencies()` deduplicates explicit `depends_on` against inferred dependencies from reference fields (`network`, `key`, `image`, `kernel`, `binary`). If a user writes both, the dependency is not duplicated.

## Happy path: Apply

### 1. Resolve spec

`Apply()` calls `ResolveSpec()` which reads the YAML file, validates version, and creates `Step` instances for each resource entry.

### 2. Build pipeline

`workflow.NewPipeline(steps)` validates and topologically sorts steps using `BuildDAG()`. Level 0 = no dependencies (network, key, image, kernel, binary). Level 1 = depends on level 0 (vm). SSH, copy, and exec steps depend on their `depends_on` entries.

### 3. Derive workflow ID

`crypto.WorkflowID(specPath)` produces a 16-hex-char workflow ID from the spec path.

### 4. Read previous state

If the spec was applied before, the previous `WorkflowState` is read from the state directory. Steps receive their previous `saved` state in `Apply()` for re-apply detection.

### 5. Execute pipeline

`Pipeline.Execute()` walks the DAG level by level. Each step's `Apply()` is called with `saved` (previous state) and `write` (StateWriter). Steps call `write()` after their API operation succeeds, persisting state immediately.

The per-step state persistence means that if execution fails partway, the state file already contains all completed steps. Re-running `env apply` skips completed steps via existence checks.

### 6. Output

On success, the state file reflects every provisioned resource.

## Happy path: Destroy

### 1. Resolve identifier

`Destroy()` resolves the identifier (supports exact match, prefix match, and path-based hashing) to find the workflow state directory.

### 2. Reconstruct steps from state

Each `AppliedResource` from the state file is passed through `Registry[resource.Type].FromState()` to reconstruct a minimal Step instance that only needs `Destroy()` and `StateData()`.

### 3. Destroy in reverse order

`Pipeline.Destroy()` runs destroy in reverse topological order. Each step checks its `WasCreated` flag: if true, it tears down the resource; if false, it skips (resource was pre-existing).

### 4. Per-step persistence

After each successful destroy, the step is removed from the state file. If destroy fails partway, the state file contains only remaining resources — re-running picks up where it left off.

### 5. Cleanup

After all destroys succeed, the workflow state directory is removed.

## Naming conventions

The spec has two distinct naming layers:

| Term | Definition | Example | Used in |
|------|-----------|---------|---------|
| **Step name** | The YAML map key that identifies a step | `my-primary-network` | `depends_on`, `removes`, state file, DAG |
| **Resource name** | The name of the actual created resource (optional override) | `primary` | API calls (bridge name, VM name, key name) |

The step name is always the YAML map key. An optional `name` field inside the params overrides the resource name — useful when the step identifier is descriptive but the resource should have a short name:

```yaml
network:
  my-primary-network:        # ← step name
    name: primary            # ← resource name (the bridge is called "primary")
    subnet: "172.27.0.0/24"
```

## Reference format

Cross-resource references use the `@type:name` format. The `@` sigil distinguishes references from literal string values:

```yaml
depends_on:
  - "@network:default"       # ← reference to a network step
  - "@key:main-key"          # ← reference to a key step

vm:
  dev-vm:
    network: "@network:default"  # ← same format in reference fields
```

The type prefix (`network`, `key`, `image`, etc.) disambiguates steps with the same name under different types. Both `depends_on` entries and reference fields (`network`, `key`, `image`, `kernel`, `binary`) use the same format.

All references use the **step name**, never the resource name. If a step has `name: primary` as resource name override, `depends_on` still uses `@network:my-primary-network`.

## YAML spec format

```yaml
version: "1"
ephemeral: false

network:
  default:
    subnet: "172.27.0.0/24"
    nat_enabled: true
    default: true

key:
  main-key:
    algorithm: ed25519
    default: true

image:
  os-image:
    type: alpine
    version: "3.23"

kernel:
  fc-kernel:
    type: firecracker

binary:
  fc-bin:
    version: "1.16.0"
    default: true

vm:
  dev-vm:
    name: production-vm-1           # optional resource name override
    network: "@network:default"
    key: "@key:main-key"
    image: "@image:os-image"
    kernel: "@kernel:fc-kernel"
    binary: "@binary:fc-bin"
    vcpu: 2
    mem: 2048
    depends_on:
      - "@network:default"
      - "@key:main-key"
      - "@image:os-image"
      - "@kernel:fc-kernel"
      - "@binary:fc-bin"

exec:
  setup-app:
    target: dev-vm
    cmd: "curl -sS https://example.com/setup.sh | sh"
    depends_on:
      - "@vm:dev-vm"

ssh:
  verify:
    target: dev-vm
    user: root
    cmd: "uname -a"
    depends_on:
      - "@vm:dev-vm"

copy:
  deploy-bin:
    src: ./mvm
    dest: "dev-vm:/opt/bin/mvm"
    depends_on:
      - "@vm:dev-vm"
```

### EnvSpec struct

```go
type EnvSpec struct {
    Version   string                                 `yaml:"version"`
    Ephemeral bool                                   `yaml:"ephemeral"`
    Steps     map[string]map[string]model.ResourceMap `yaml:",inline"`
}
```

Each type section is a map from step name to its params. The outer key is the step type (`network`, `vm`, etc.), the inner key is the step name (`default`, `dev-vm`, etc.).

### `@type:name` parsing pipeline

References in the spec use `@type:name` format. At parse time, `resolveSpecV1` calls `stripSpecRefs(entry)` which strips the `@` sigil from all string values and string-list entries. This normalizes `"@network:default"` → `"network:default"` for internal use.

Then, for step reference fields (`network`, `key`, `image`, `kernel`, `binary`, `target`, `source`), each factory additionally calls `stripBareName()` to reduce `"network:default"` → `"default"` — the bare name expected by downstream resolvers.

`depends_on` and `removes` entries keep the full `"type:name"` format internally because the DAG matches them against `stepByName` keys which are `FormatStepName(type, name)`.

### Resource name override

When a step entry has a `name` field, it overrides the resource's actual name. The YAML marshal/unmarshal in each factory captures it into the input struct's `Name` field. If the field is absent, the step name (YAML map key) is used as the resource name:

```go
// After yaml.Unmarshal, input.Name is set from spec's "name" field if present
// If empty, fall back to the step name parameter
if input.Name == "" {
    input.Name = name
}
```

This applies to network, key, and VM factories (which create named resources). Image, kernel, and binary factories pass the `name` field through naturally via YAML marshal/unmarshal.

### YAML field mappings

| Input Type | YAML Field | Go Field | Notes |
|-----------|-----------|----------|-------|
| `NetworkCreateInput` | `name` | `Name` | Optional resource name override |
| `KeyCreateInput` | `name` | `Name` | Optional resource name override |
| `VMCreateInput` | `name` | `Name` | Optional resource name override |
| `BinaryPullInput` | `type` | `Type` | Binary type, e.g. `firecracker` |
| `BinaryPullInput` | `version` | `Version` | |
| `BinaryPullInput` | `force` | `DownloadOverride` | |
| `BinaryPullInput` | `default` | `SetDefault` | |
| `KeyCreateInput` | `force` | `Overwrite` | |
| `SSHInput` | `target` | `Identifier` | |
| `ExecInput` | `target` | `Identifier` | Target VM name/ID |
| `ExecInput` | `cmd` | `Command` | Command to execute |
| `ExecInput` | `timeout` | `Timeout` | Command timeout in seconds |
| `ExecInput` | `port` | `Port` | Vsock agent port (default: 1024) |
| `CPInput` | `src` | `Sources` | Single string auto-normalized to `[]string` |
| `CPInput` | *(none)* | `Dst` | Built from `target` + `:` + `dst` in `FromSpec` |

## Failure modes

### Cycle detection

`BuildDAG()` uses Kahn's algorithm and returns an error with the cycle path if a cycle is detected in step dependencies.

### Partial apply failure

If a step's `Apply()` fails, the pipeline returns the error. The state file reflects all steps that completed before the failure. Re-running applies only the remaining steps.

### Partial destroy failure

If a step's `Destroy()` fails, the state file retains the failed step's entry. Re-running destroy picks up from the remaining resources.

### Crash during state write

The `WriteWorkflowState` call is atomic (`.tmp` → `os.Rename`). A crash during this call leaves either the old state file intact or the new state file complete — never a corrupt partial file.

### Missing spec file on destroy

Destroy reconstructs steps from the saved state file — the original spec file is not needed. This means environments created on one machine can be destroyed from another machine (as long as the state directory exists).

## Key files

| File | Purpose |
|------|---------|
| `internal/lib/workflow/step.go` | `Step` interface, `StateWriter` type, `StepFunc` adapter |
| `internal/lib/workflow/pipeline.go` | `Pipeline` struct, `Execute()`, `Destroy()` |
| `internal/lib/workflow/state.go` | `SharedState` (thread-safe cross-step data) |
| `internal/lib/workflow/dag.go` | `BuildDAG()` — Kahn's algorithm topological sort |
| `internal/lib/workflow/persist.go` | `ReadWorkflowState()`, `WriteWorkflowState()` |
| `internal/lib/model/workflow.go` | `ResourceMap`, `ResourceMeta`, `ResourceState`, `AppliedResource`, `WorkflowState` |
| `internal/workflow/env/env.go` | `Apply()`, `Destroy()`, `List()`, `Diff()` — standalone orchestration functions |
| `internal/workflow/env/spec.go` | `EnvSpec`, `UnmarshalYAML()`, `ResolveSpec()` |
| `internal/workflow/env/factory.go` | `StepFactory`, `Registry` (9 step types) |
| `internal/workflow/env/step_*.go` | Step implementations: network, key, image, kernel, binary, vm, ssh, copy, exec |
| `internal/workflow/env/utils.go` | `FormatStepName()`, `BareStepName()`, `StateFromMap()`, `InferStepType()` |
| `internal/cli/env.go` | Cobra commands: `apply`, `ls`, `diff`, `destroy` |

## Remote URL spec support

`mvm env apply`, `diff`, and `destroy` accept a remote URL (`https://` or `http://`) instead of a local file path. The spec is fetched over HTTP and parsed identically to a local file.

### How it works

**Detection in `ResolveSpec`** (`internal/workflow/env/spec.go`):
- Before `os.ReadFile`, check `strings.HasPrefix(specPath, "http://") || strings.HasPrefix(specPath, "https://")`
- For URLs: `download.New().GetBody(ctx, specPath)` → returns `[]byte` → `yaml.Unmarshal`
- For file paths: `os.ReadFile` — unchanged

**Detection in CLI** (`internal/cli/env.go`):
- The `os.Stat(specPath)` guard in `newEnvApplyCmd` is skipped for URLs
- `env diff` and `env destroy` have no file-existence checks — they pass through directly

### What just works without changes

| Concern | Why |
|---|---|
| **Workflow ID** | `crypto.WorkflowID(url)` hashes the URL string. `filepath.Abs` returns an error for URLs; `WorkflowID` falls back to the raw string. Deterministic per URL. |
| **Workflow ID resolution** | `ResolveWorkflowID` treats anything with `/` or `.` as a path (URL has both) and calls `crypto.WorkflowID`. Re-apply and destroy resolve correctly. |
| **State persistence** | `SpecPath` stores the URL — shown in `env ls`, used as-is on re-apply. |
| **`env diff`** | No CLI-level `os.Stat` — benefits from `ResolveSpec` fix directly. |
| **`env destroy`** | `CheckArg` doesn't check file existence. `ResolveWorkflowID` hashes the URL → finds the state → destroys. |
| **All step factories** | They only consume parsed YAML. Source format is invisible downstream. |
| **Existing tests** | All tests pass file paths to `ResolveSpec` — they hit the `os.ReadFile` branch. Zero changes needed. |

### Why `download.New().GetBody()`

- Already used in `kernel/service.go` for fetching kernel config fragments — no new dependency
- One-shot HTTP GET — no disk caching (avoids stale spec), no retry (transient failure → user re-runs)
- Consistent error wrapping: `errs.DomainError` with `CodeDownloadFailed`
- No signature change to `ResolveSpec` — downloader is created inline, not DI'd (the downloader is stateless for `GetBody`)

### Edge cases

| Case | Behavior |
|---|---|
| **URL returns 404** | `GetBody` returns `"Failed to fetch {url}: HTTP 404"` |
| **URL DNS failure** | Timeout/DNS error wrapped in `CodeDownloadFailed` |
| **URL returns non-YAML** | `yaml.Unmarshal` returns `"env spec validation: invalid YAML: ..."` |
| **Re-apply** | State stores URL as `SpecPath`. `WorkflowID(url)` → same ID → finds state → re-fetches → drift detection via `SpecHash` |
| **Destroy by URL** | `mvm env destroy https://...` → `ResolveWorkflowID` hashes URL → finds state → destroys |
| **No args (default discovery)** | Only looks for local `mvmctl.yaml` / `mvmctl.yml` — unchanged |

## Design decisions

**API interface, not concrete Operation.** The `env` package is a pure consumer of the API layer — exactly like the CLI. Steps hold an `api.API` reference (the composite interface), providing a defined contract without coupling to `*api.Operation`. Tests can use mock implementations.

**Standalone functions, not Operation methods.** `Apply()`, `Destroy()`, `List()`, `Diff()` are standalone functions in `internal/workflow/env/env.go`, not methods on `api.Operation`. This keeps the workflow engine independent of the operation struct and avoids circular dependency issues.

**Per-step state persistence.** State is persisted after every successful step, not batched at the end. On crash, re-running picks up from the last persisted step. The only crash window is the atomic `os.Rename` call.

**Singular identifiers everywhere.** YAML keys, step types, `depends_on`, step names, and state files all use singular identifiers. Registry keys match StepType, enabling direct `Registry[stepType]` lookup with no bridge function.

**No init() calls.** The Registry is a package-level map literal with all step types visible in one place. No side effects during initialization.

**Imperative steps always re-run.** SSH, copy, and exec steps have no existence check. They always execute on apply. Destroy is a no-op for all three.

### Step removals (`removes` field)

Every step can declare a `removes` field — a list of `"@type:name"` resources to destroy after the step's `Apply()` succeeds:

```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    removes:
      - "@vm:builder"
```

After `capture-base` finishes importing, `@vm:builder` is destroyed immediately — before downstream steps start. This frees resources mid-pipeline instead of waiting for the final destroy phase.

**Why per-step, not global:** Timing matters. The builder VM might hold RAM and disk that a downstream `vm:final` needs. A global cleanup block at the end can't express this ordering. Placing `removes` on the step that creates the need for cleanup makes the intent explicit and keeps the lifecycle declaration with the consumer, not the resource being destroyed.

**Why not on the removed resource:** Putting `lifespan: [image_import:capture-base]` on the VM couples it to downstream consumers it shouldn't know about. The consumer declaring `removes` is more natural — it's the step that creates the need for cleanup.

**Pattern relationship:** `depends_on` is "need this first", `removes` is "now clean this up". Same level, same logic, opposite direction.

**Dispatch:** The cleanup iterates each step's `removes` after the pipeline succeeds. The `@` sigil and type prefix are stripped at parse time; the remaining `"type:name"` determines which API method to call via a switch/case on `InferStepType()`. Failures are best-effort — logged as warnings, never propagated.

### Ephemeral specs (`ephemeral: true`)

The top-level `ephemeral: true` field tells `Apply()` to automatically run `Destroy()` after a successful pipeline. This tears down all resources and removes the workflow state — same as calling `mvm env destroy`. Useful for CI/CD pipelines that provision a VM, extract build artifacts, and want zero cleanup burden.

The ephemeral destroy runs after the `removes` phase, so mid-pipeline cleanup still happens during the pipeline, and ephemeral handles final sweep + state removal.

```yaml
version: "1"
ephemeral: true

copy:
  retrieve-artifacts:
    dest: ./dist/
    src: "builder:/output/artifact.tar.gz"
    removes:
      - "@vm:builder"
```
