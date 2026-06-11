# mvmctl Go API Reference

## Introduction

Every CLI command maps 1:1 to a method on the `api.Operation` struct in `pkg/api/`.
The CLI is a thin presentation layer on top of these methods — it handles argument
parsing, output formatting, and exit codes, then calls the same functions documented here.

You can import the API directly to build automation scripts, GUIs, or TUIs without
going through the CLI.

## Table of Contents

- [Introduction](#introduction)
- [Import Pattern](#import-pattern)
- [Operation Struct](#operation-struct)
- [Input/Request/Resolved Pattern](#inputrequestresolved-pattern)
- [Domain Methods](#domain-methods)
  - [VM Operations](#vm-operations-vmgo)
  - [Network Operations](#network-operations-networkgo)
  - [Image Operations](#image-operations-imagego)
  - [Kernel Operations](#kernel-operations-kernelgo)
  - [Binary Operations](#binary-operations-binarygo)
  - [Key Operations](#key-operations-keygo)
  - [Volume Operations](#volume-operations-volumego)
  - [Host Operations](#host-operations-hostgo)
  - [Config Operations](#config-operations-configgo)
  - [Console Operations](#console-operations-consolego)
  - [Logs Operations](#logs-operations-logsgo)
  - [SSH Operations](#ssh-operations-sshgo)
  - [CP Operations](#cp-operations-cpgo)
  - [Cache Operations](#cache-operations-cachego)
  - [Init Operations](#init-operations-initgo)
- [Input Types](#input-types)
- [Response Types](#response-types)
- [Error Handling](#error-handling)
- [CLI Integration](#cli-integration)

---

## Import Pattern

```go
import (
    "mvmctl/pkg/api"
    "mvmctl/pkg/api/inputs"
    "mvmctl/pkg/api/responses"
    "mvmctl/pkg/errs"
)
```

---

## Operation Struct

`api.Operation` is the single composition root for all API operations. Every method
receives the full dependency set.

```go
type Operation struct {
    Connection      *db.Handle
    CacheDir        string
    Enr             *enricher.Enricher
    Repos           Repos
    Services        Services
    ProvisionerType provisioner.ProvisionerType
    AuditLog        *logging.AuditLog
}
```

### Construction

```go
func NewOperation(ctx context.Context, conn *db.Handle, cacheDir string) *Operation
```

`NewOperation` wires all repositories, services, enricher, and provisioner type.
It validates that all required services are non-nil and panics on nil.

### Dependency Groups

**Repos** — all database repositories:

| Field | Type | Domain |
|-------|------|--------|
| `VM` | `vm.Repository` | VM instances |
| `Network` | `network.Repository` | Networks |
| `Lease` | `network.LeaseRepository` | Network leases |
| `Image` | `image.Repository` | Images |
| `Kernel` | `kernel.Repository` | Kernels |
| `Binary` | `binary.Repository` | Firecracker binaries |
| `Key` | `key.Repository` | SSH keys |
| `Volume` | `volume.Repository` | Volumes |
| `Host` | `host.Repository` | Host state |
| `Config` | `config.SettingsRepository` | User settings |

**Services** — all domain services:

| Field | Type | Domain |
|-------|------|--------|
| `Binary` | `*binary.Service` | Binary management |
| `Image` | `*image.Service` | Image management |
| `Kernel` | `*kernel.Service` | Kernel management |
| `Network` | `*network.Service` | Network management |
| `Host` | `*host.Service` | Host management |
| `Config` | `*config.Service` | Configuration |
| `Key` | `*key.Service` | SSH key management |
| `Volume` | `*volume.Service` | Volume management |
| `Cache` | `*cache.Service` | Cache management |
| `CP` | `*ssh.CPService` | File copy (SCP) |

---

## Input/Request/Resolved Pattern

Every public-facing domain follows a three-struct pattern in `pkg/api/inputs/`:

1. **`*Input`** — Raw CLI input. Thin struct with typed fields. Optional fields are `*T`.
2. **`*Request`** — Accepts Input and dependencies (DB, repos, enricher). `Resolve(ctx)` looks up DB-backed records, validates, returns Resolved.
3. **`Resolved*`** — Immutable output. Every field explicit and validated.

Example flow:

```
VMInput{Identifiers, Force}
  → VMRequest{db, input, resolver, enricher}.Resolve(ctx)
    → ResolvedVMInput{VMs []*model.VM, Force bool}
```

---

## Domain Methods

All methods are on `*api.Operation`. Each takes `ctx context.Context` as the first parameter.

### VM Operations (`vm.go`)

| Method | Signature |
|--------|-----------|
| `VMCreate` | `VMCreate(ctx context.Context, input inputs.VMCreateInput, onProgress event.OnProgressCallback) ([]*model.VM, error)` |
| `VMRemove` | `VMRemove(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMPrune` | `VMPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `VMList` | `VMList(ctx context.Context, statuses ...string) []*model.VM` |
| `VMGet` | `VMGet(ctx context.Context, input inputs.VMInput) (*model.VM, error)` |
| `VMInspect` | `VMInspect(ctx context.Context, input inputs.VMInput) (*responses.VMInspect, error)` |
| `VMStart` | `VMStart(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMStop` | `VMStop(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMSnapshot` | `VMSnapshot(ctx context.Context, input inputs.VMInput, memFile string, stateFile string) error` |
| `VMLoad` | `VMLoad(ctx context.Context, input inputs.VMInput, memFile string, stateFile string, resume bool) error` |
| `VMReboot` | `VMReboot(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMPause` | `VMPause(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMResume` | `VMResume(ctx context.Context, input inputs.VMInput) *errs.BatchResult` |
| `VMAttachVolume` | `VMAttachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error` |
| `VMDetachVolume` | `VMDetachVolume(ctx context.Context, input inputs.VMInput, volumeName string) error` |

### Network Operations (`network.go`)

| Method | Signature |
|--------|-----------|
| `NetworkCreate` | `NetworkCreate(ctx context.Context, input inputs.NetworkCreateInput) (*model.Network, error)` |
| `NetworkRemove` | `NetworkRemove(ctx context.Context, input inputs.NetworkInput, force bool) error` |
| `NetworkListAll` | `NetworkListAll(ctx context.Context) ([]*model.Network, error)` |
| `NetworkGet` | `NetworkGet(ctx context.Context, input inputs.NetworkInput) (*model.Network, error)` |
| `NetworkToJSON` | `NetworkToJSON(networks []*model.Network) []map[string]any` |
| `NetworkInspect` | `NetworkInspect(ctx context.Context, input inputs.NetworkInput) (*responses.NetworkInspect, error)` |
| `NetworkSetDefault` | `NetworkSetDefault(ctx context.Context, input inputs.NetworkInput) error` |
| `NetworkSync` | `NetworkSync(ctx context.Context, input inputs.NetworkInput) (map[string]map[string]int, error)` |
| `NetworkPrune` | `NetworkPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `NetworkCreateDefaultNetwork` | `NetworkCreateDefaultNetwork(ctx context.Context) (*model.Network, error)` |

### Image Operations (`image.go`)

| Method | Signature |
|--------|-----------|
| `ImagePrune` | `ImagePrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `ImagePull` | `ImagePull(ctx context.Context, input inputs.ImagePullInput, onProgress event.OnProgressCallback) (*model.ImageItem, error)` |
| `ImageImport` | `ImageImport(ctx context.Context, input inputs.ImageImportInput, onProgress event.OnProgressCallback) (*model.ImageItem, error)` |
| `ImageWarm` | `ImageWarm(ctx context.Context, input inputs.ImageInput, all bool, onProgress event.OnProgressCallback) ([]string, error)` |
| `ImageRemove` | `ImageRemove(ctx context.Context, input inputs.ImageInput, force bool) *errs.BatchResult` |
| `ImageListAll` | `ImageListAll(ctx context.Context, remote bool, typeFilter string, noCache bool, onProgress event.OnProgressCallback) ([]*model.ImageItem, []model.VersionInfo, error)` |
| `ImageGet` | `ImageGet(ctx context.Context, input inputs.ImageInput) (*model.ImageItem, error)` |
| `ImageInspect` | `ImageInspect(ctx context.Context, input inputs.ImageInput) (*responses.ImageInspect, error)` |
| `ImageSetDefault` | `ImageSetDefault(ctx context.Context, input inputs.ImageInput) error` |

### Kernel Operations (`kernel.go`)

| Method | Signature |
|--------|-----------|
| `KernelPrune` | `KernelPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `KernelPull` | `KernelPull(ctx context.Context, input inputs.KernelPullInput, onProgress event.OnProgressCallback) (*model.KernelItem, error)` |
| `KernelImport` | `KernelImport(ctx context.Context, input inputs.KernelImportInput) (*model.KernelItem, error)` |
| `KernelRemove` | `KernelRemove(ctx context.Context, input inputs.KernelInput) *errs.BatchResult` |
| `KernelList` | `KernelList(ctx context.Context, remote bool, noCache bool, onProgress event.OnProgressCallback) ([]*model.KernelItem, []model.VersionInfo, error)` |
| `KernelGet` | `KernelGet(ctx context.Context, identifier string) (*model.KernelItem, error)` |
| `KernelInspect` | `KernelInspect(ctx context.Context, identifier string) (*responses.KernelInspect, error)` |
| `KernelSetDefault` | `KernelSetDefault(ctx context.Context, identifier string) error` |

### Binary Operations (`binary.go`)

| Method | Signature |
|--------|-----------|
| `BinaryPrune` | `BinaryPrune(ctx context.Context, dryRun bool, force bool) ([]string, error)` |
| `BinaryPull` | `BinaryPull(ctx context.Context, input inputs.BinaryPullInput, onProgress event.OnProgressCallback) ([]*model.BinaryItem, error)` |
| `BinaryRemove` | `BinaryRemove(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult` |
| `BinaryRemoveByVersion` | `BinaryRemoveByVersion(ctx context.Context, version string, force bool) error` |
| `BinaryList` | `BinaryList(ctx context.Context, remote bool, limit *int, onProgress event.OnProgressCallback) ([]*model.BinaryItem, []model.VersionInfo, error)` |
| `BinaryGet` | `BinaryGet(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error)` |
| `BinarySetDefault` | `BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error)` |
| `BinaryEnsureDefault` | `BinaryEnsureDefault(ctx context.Context) (*model.BinaryItem, error)` |

### Key Operations (`key.go`)

| Method | Signature |
|--------|-----------|
| `KeyListAll` | `KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error)` |
| `KeyGet` | `KeyGet(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error)` |
| `KeyCreate` | `KeyCreate(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error)` |
| `KeyImport` | `KeyImport(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error)` |
| `KeyRemove` | `KeyRemove(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult` |
| `KeyInspect` | `KeyInspect(ctx context.Context, input inputs.KeyInput) (*responses.KeyInspect, error)` |
| `KeyExport` | `KeyExport(ctx context.Context, input inputs.KeyInput, destination string, overwrite bool) ([]string, error)` |
| `KeySetDefaults` | `KeySetDefaults(ctx context.Context, input inputs.KeyInput) error` |
| `KeyGetDefaults` | `KeyGetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error)` |
| `KeyClearDefaults` | `KeyClearDefaults(ctx context.Context) error` |

### Volume Operations (`volume.go`)

| Method | Signature |
|--------|-----------|
| `VolumeListAll` | `VolumeListAll(ctx context.Context) []*model.VolumeItem` |
| `VolumeCreate` | `VolumeCreate(ctx context.Context, input inputs.VolumeCreateInput) (*model.VolumeItem, error)` |
| `VolumeRemove` | `VolumeRemove(ctx context.Context, input inputs.VolumeInput, force bool) *errs.BatchResult` |
| `VolumeInspect` | `VolumeInspect(ctx context.Context, input inputs.VolumeInput) (*responses.VolumeInspect, error)` |
| `VolumeResize` | `VolumeResize(ctx context.Context, input inputs.VolumeCreateInput) error` |
| `VolumeGet` | `VolumeGet(ctx context.Context, input inputs.VolumeInput) (*model.VolumeItem, error)` |

### Host Operations (`host.go`)

| Method | Signature |
|--------|-----------|
| `HostInit` | `HostInit(ctx context.Context, onProgress event.OnProgressCallback) (any, error)` |
| `HostGetState` | `HostGetState(ctx context.Context) (*model.HostStateItem, error)` |
| `HostDetectResources` | `HostDetectResources(ctx context.Context) (*model.HostResources, error)` |
| `HostNetworkSetup` | `HostNetworkSetup(ctx context.Context) error` |
| `HostInfo` | `HostInfo(ctx context.Context) (*responses.HostInfo, error)` |
| `HostRefreshCapacity` | `HostRefreshCapacity(ctx context.Context) (*responses.HostInfo, error)` |
| `HostCheckKVMAccess` | `HostCheckKVMAccess() bool` |
| `HostCheckRequiredBinaries` | `HostCheckRequiredBinaries() []string` |
| `HostGetIPForwardStatus` | `HostGetIPForwardStatus(ctx context.Context) (string, error)` |
| `HostStatusCheck` | `HostStatusCheck(ctx context.Context) *responses.HostStatusCheck` |
| `HostClean` | `HostClean(ctx context.Context) ([]string, error)` |
| `HostReset` | `HostReset(ctx context.Context) ([]string, error)` |
| `HostGetRunningVMs` | `HostGetRunningVMs(ctx context.Context) ([]*model.VM, error)` |
| `HostIsInitialized` | `HostIsInitialized(ctx context.Context) bool` |
| `HostCheckReadiness` | `HostCheckReadiness(ctx context.Context) *model.ProbeResult` |

### Config Operations (`config.go`)

| Method | Signature |
|--------|-----------|
| `ConfigGet` | `ConfigGet(ctx context.Context, category, key string) (any, error)` |
| `ConfigSet` | `ConfigSet(ctx context.Context, category, key string, value any) error` |
| `ConfigReset` | `ConfigReset(ctx context.Context, category, key string, allOverrides bool) (int, error)` |
| `ConfigListAll` | `ConfigListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error)` |

### Console Operations (`console.go`)

| Method | Signature |
|--------|-----------|
| `ConsoleGetState` | `ConsoleGetState(ctx context.Context, identifier string) (*responses.ConsoleStateResult, error)` |
| `ConsoleGetConnectionInfo` | `ConsoleGetConnectionInfo(ctx context.Context, identifier string) (*model.ConsoleConnectionInfo, error)` |
| `ConsoleKill` | `ConsoleKill(ctx context.Context, identifier string) error` |
| `ConsoleAttachConsole` | `ConsoleAttachConsole(ctx context.Context, socketPath string, stdin io.Reader, stdout io.Writer) error` |

### Logs Operations (`logs.go`)

| Method | Signature |
|--------|-----------|
| `LogStream` | `LogStream(ctx context.Context, input inputs.LogInput, callback func(string) error) error` |
| `LogStreamChannel` | `LogStreamChannel(ctx context.Context, input inputs.LogInput) (<-chan string, <-chan error, error)` |

### SSH Operations (`ssh.go`)

| Method | Signature |
|--------|-----------|
| `SSHConnect` | `SSHConnect(ctx context.Context, input inputs.SSHInput) error` |

### CP Operations (`cp.go`)

| Method | Signature |
|--------|-----------|
| `CPCopy` | `CPCopy(ctx context.Context, input inputs.CPInput, onProgress event.OnDownloadCallback) (*responses.CPCopyResult, error)` |

### Cache Operations (`cache.go`)

| Method | Signature |
|--------|-----------|
| `CacheCheckPrivileges` | `CacheCheckPrivileges(binary, operation string) error` |
| `CacheSessionHasGroup` | `CacheSessionHasGroup() bool` |
| `CacheInitAll` | `CacheInitAll(ctx context.Context, onProgress event.OnProgressCallback) (*responses.CacheInitResult, error)` |
| `CachePruneVMs` | `CachePruneVMs(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult` |
| `CachePruneNetworks` | `CachePruneNetworks(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `CachePruneImages` | `CachePruneImages(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `CachePruneKernels` | `CachePruneKernels(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `CachePruneBinaries` | `CachePruneBinaries(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)` |
| `CachePruneMisc` | `CachePruneMisc(ctx context.Context, dryRun bool) (map[string]any, error)` |
| `CachePruneAll` | `CachePruneAll(ctx context.Context, dryRun bool, includeAll bool) (*model.PruneAllResult, error)` |
| `CacheClean` | `CacheClean(ctx context.Context, dryRun bool) (*model.CleanResult, error)` |

### Init Operations (`init.go`)

| Method | Signature |
|--------|-----------|
| `InitCheckReadiness` | `InitCheckReadiness(ctx context.Context) *model.ProbeResult` |
| `InitSetupHost` | `InitSetupHost(ctx context.Context) error` |
| `InitRun` | `InitRun(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, downloadVersion string, onProgress event.OnProgressCallback) *InitResult` |
| `InitRunFull` | `InitRunFull(ctx context.Context, skipHost bool, skipNetwork bool, nonInteractive bool, sudoCompleted bool, hostSetupMessage string, downloadVersion string, guestfsEnabled *bool, onProgress event.OnProgressCallback) *InitResult` |

#### Init result types

```go
type InitStepResult struct {
    Step    string `json:"step"`
    Success bool   `json:"success"`
    Message string `json:"message"`
}

type InitResult struct {
    Steps            []InitStepResult       `json:"steps"`
    HostReady        bool                   `json:"host_ready"`
    NeedsInteraction *errs.NeedsInteraction `json:"needs_interaction,omitempty"`
}
```

---

## Input Types

All input types are in `pkg/api/inputs/`. Each is paired with a `*Request` and `Resolved*` type.

### Input structs (CLI-facing)

| Type | File | Used by |
|------|------|---------|
| `VMInput` | `vm_input.go` | `VMRemove`, `VMStart`, `VMStop`, `VMPause`, `VMResume`, `VMReboot`, `VMGet`, `VMInspect`, `VMSnapshot`, `VMLoad`, `VMAttachVolume`, `VMDetachVolume` |
| `VMCreateInput` | `vm_create.go` | `VMCreate` |
| `NetworkInput` | `network_input.go` | `NetworkRemove`, `NetworkGet`, `NetworkInspect`, `NetworkSetDefault`, `NetworkSync`, `NetworkPrune` |
| `NetworkCreateInput` | `network_create.go` | `NetworkCreate` |
| `ImageInput` | `image_input.go` | `ImageRemove`, `ImageGet`, `ImageInspect`, `ImageSetDefault`, `ImageWarm`, `ImagePrune` |
| `ImagePullInput` | `image_acquire.go` | `ImagePull` |
| `ImageImportInput` | `image_acquire.go` | `ImageImport` |
| `KernelInput` | `kernel_input.go` | `KernelRemove`, `KernelGet`, `KernelInspect`, `KernelSetDefault`, `KernelPrune` |
| `KernelPullInput` | `kernel_pull.go` | `KernelPull` |
| `KernelImportInput` | `kernel_import.go` | `KernelImport` |
| `BinaryInput` | `binary_input.go` | `BinaryRemove`, `BinaryGet`, `BinarySetDefault`, `BinaryPrune` |
| `BinaryPullInput` | `binary_pull.go` | `BinaryPull` |
| `KeyInput` | `key_input.go` | `KeyGet`, `KeyRemove`, `KeyInspect`, `KeyExport`, `KeySetDefaults` |
| `KeyCreateInput` | `key_create.go` | `KeyCreate` |
| `KeyImportInput` | `key_import.go` | `KeyImport` |
| `VolumeInput` | `volume_input.go` | `VolumeRemove`, `VolumeInspect`, `VolumeGet` |
| `VolumeCreateInput` | `volume_create.go` | `VolumeCreate`, `VolumeResize` |
| `ConfigInput` | `config_input.go` | (internal) `ConfigGet`, `ConfigSet`, `ConfigReset`, `ConfigListAll` |
| `ConsoleInput` | `console_input.go` | (internal) `ConsoleGetState`, `ConsoleGetConnectionInfo`, `ConsoleKill` |
| `LogInput` | `logs_input.go` | `LogStream`, `LogStreamChannel` |
| `SSHInput` | `ssh_input.go` | `SSHConnect` |
| `CPInput` | `cp_input.go` | `CPCopy` |

### Request structs (resolution pipeline)

| Type | File |
|------|------|
| `VMRequest` | `vm_input.go` |
| `VMCreateRequest` | `vm_create.go` |
| `NetworkRequest` | `network_input.go` |
| `NetworkCreateRequest` | `network_create.go` |
| `ImageRequest` | `image_input.go` |
| `ImageAcquireRequest` | `image_acquire.go` |
| `KernelRequest` | `kernel_input.go` |
| `KernelPullRequest` | `kernel_pull.go` |
| `KernelImportRequest` | `kernel_import.go` |
| `BinaryRequest` | `binary_input.go` |
| `BinaryPullRequest` | `binary_pull.go` |
| `KeyRequest` | `key_input.go` |
| `KeyCreateRequest` | `key_create.go` |
| `VolumeRequest` | `volume_input.go` |
| `VolumeCreateRequest` | `volume_create.go` |
| `ConfigRequest` | `config_input.go` |
| `ConsoleRequest` | `console_input.go` |
| `LogRequest` | `logs_input.go` |
| `SSHRequest` | `ssh_input.go` |
| `CPRequest` | `cp_input.go` |

### Resolved structs (immutable output)

| Type | File |
|------|------|
| `ResolvedVMInput` | `vm_input.go` |
| `ResolvedVMCreateInput` | `vm_create.go` |
| `ResolvedNetworkInput` | `network_input.go` |
| `ResolvedNetworkCreateRequest` | `network_create.go` |
| `ResolvedImageInput` | `image_input.go` |
| `ResolvedImageAcquireInput` | `image_acquire.go` |
| `ResolvedKernelInput` | `kernel_input.go` |
| `ResolvedKernelPullRequest` | `kernel_pull.go` |
| `ResolvedKernelImportInput` | `kernel_import.go` |
| `ResolvedBinaryInput` | `binary_input.go` |
| `ResolvedBinaryPullInput` | `binary_pull.go` |
| `ResolvedKeyInput` | `key_input.go` |
| `ResolvedKeyCreateInput` | `key_create.go` |
| `ResolvedVolumeInput` | `volume_input.go` |
| `ResolvedVolumeCreateInput` | `volume_create.go` |
| `ResolvedConfigInput` | `config_input.go` |
| `ResolvedConsoleInput` | `console_input.go` |
| `ResolvedLogInput` | `logs_input.go` |
| `ResolvedSSHInput` | `ssh_input.go` |
| `ResolvedCPInput` | `cp_input.go` |

---

## Response Types

Domain-specific response types live in `pkg/api/responses/`:

| Type | File | Used by |
|------|------|---------|
| `VMInspect` | `vm.go` | `VMInspect` |
| `NetworkInspect` | `network.go` | `NetworkInspect` |
| `ImageInspect` | `image.go` | `ImageInspect` |
| `KernelInspect` | `kernel.go` | `KernelInspect` |
| `KeyInspect` | `key.go` | `KeyInspect` |
| `VolumeInspect` | `volume.go` | `VolumeInspect` |
| `HostInfo` | `host.go` | `HostInfo`, `HostRefreshCapacity` |
| `HostStatusCheck` | `host.go` | `HostStatusCheck` |
| `ConsoleStateResult` | `misc.go` | `ConsoleGetState` |
| `CPCopyResult` | `misc.go` | `CPCopy` |
| `CacheInitResult` | `misc.go` | `CacheInitAll` |

All response types have `json:"field"` struct tags for direct `json.MarshalIndent` output.

### VMInspect sub-types

| Type | Purpose |
|------|---------|
| `VMItemInfo` | VM metadata (name, ID, status, PID, SSH keys, timestamps) |
| `VMResourcesInfo` | vCPUs, memory, disk |
| `VMNetworkingInfo` | IPv4, MAC, network, TAP device |
| `VMAssetsInfo` | Image, kernel, binary references |
| `VMFilesystemInfo` | VM directory, rootfs path, config path, log paths |
| `VMConsoleInfo` | Relay running, PID, socket path |
| `VMVolume` | Attached volume info |

### NetworkInspect sub-types

| Type | Purpose |
|------|---------|
| `NetworkItemInfo` | Network metadata (name, subnet, bridge, gateway) |
| `NetworkStatusInfo` | Bridge active, is present, is default |
| `NetworkNATInfo` | NAT enabled, NAT gateways |
| `NetworkLease` | IPv4, VM ID, leased at, expires at |

### HostInfo sub-types

| Type | Purpose |
|------|---------|
| `HostOSInfo` | OS name, version, architecture |
| `HostCPUInfo` | Model, cores, threads, frequency |
| `HostVirtInfo` | Virtualization support, nested virt |
| `HostHugepagesInfo` | Hugepages status |
| `HostDepsInfo` | KVM access, required binaries |
| `HostSystemInfo` | IP forwarding, bridges, TAP devices |
| `HostMemoryInfo` | Total, used, available |
| `HostStorageInfo` | Cache directory, free space |
| `HostKernelInfo` | Version, modules |
| `HostLimitsInfo` | Max VMs, memory, CPUs |
| `HostCapacityCurrentInfo` | Current VM count, memory, CPUs |
| `HostCapacityInfo` | Capacity analysis |
| `HostSetupInfo` | Initialized, initialized at |
| `HostStatusCheck` | KVM OK, missing binaries, IP forward, group/sudoers state |

---

## Error Handling

All API methods return `error`. Errors are always `*errs.DomainError`:

```go
result, err := op.VMCreate(ctx, input, nil)
if err != nil {
    var de *errs.DomainError
    if errors.As(err, &de) {
        switch de.Code {
        case errs.CodeVMAlreadyExists:
            // handle duplicate
        case errs.CodeVMNotFound:
            // handle not found
        }
    }
}
```

### Error codes

Dot-separated with domain prefix. See `pkg/errs/codes.go` for the full list.

Common codes:

| Code | Domain |
|------|--------|
| `vm.not_found` | VM not found |
| `vm.already_exists` | VM name collision |
| `vm.create.failed` | VM creation failed |
| `vm.snapshot.failed` | Snapshot failed |
| `vm.load.snapshot.failed` | Load snapshot failed |
| `vm.atomic.failed` | Atomic batch creation failed |
| `network.create.failed` | Network creation failed |
| `network.remove.failed` | Network removal failed |
| `network.bridge.failed` | Bridge setup failed |
| `network.nat.failed` | NAT setup failed |
| `network.not_found` | Network not found |
| `image.pull.failed` | Image pull failed |
| `image.import.failed` | Image import failed |
| `image.not_found` | Image not found |
| `image.warm.failed` | Image warming failed |
| `image.corrupt` | Image extraction/optimization failed |
| `kernel.pull.failed` | Kernel pull/build failed |
| `kernel.import.failed` | Kernel import failed |
| `kernel.not_found` | Kernel not found |
| `binary.pull.failed` | Binary download/build failed |
| `binary.not_found` | Binary not found |
| `binary.already_exists` | Binary already downloaded |
| `binary.remove.failed` | Binary removal failed |
| `binary.default.set.failed` | Set default binary failed |
| `binary.ensure.default.failed` | Ensure default binary failed |
| `binary.no_ci_version` | Binary has no CI version |
| `binary.version_gate` | Version gate not met |
| `key.create.failed` | Key creation failed |
| `key.add.failed` | Key import failed |
| `key.not_found` | Key not found |
| `key.export.failed` | Key export failed |
| `key.default.set.failed` | Set default key failed |
| `key.defaults.clear.failed` | Clear default keys failed |
| `volume.not_found` | Volume not found |
| `volume.resize.failed` | Volume resize failed |
| `host.init.failed` | Host initialization failed |
| `host.info.failed` | Host info retrieval failed |
| `host.capacity.failed` | Capacity detection failed |
| `console.relay.failed` | Console relay failed |
| `console.not_running` | No console relay running |
| `console.kill.failed` | Console kill failed |
| `cp.error` | File copy failed |
| `cp.destination.not_dir` | CP destination not a directory |
| `ssh.error` | SSH connection failed |
| `config.error` | Configuration error |
| `cache.clean.failed` | Cache clean failed |
| `privilege.required` | Elevated privileges required |
| `database.error` | Database operation failed |
| `validation.failed` | Input validation failed |
| `internal` | Unexpected internal error |
| `root.partition.detection` | Root partition detection failed |
| `tie.detected` | Multiple partition candidates |

### Error classes

| Class | Meaning |
|-------|---------|
| `ClassValidation` | Input validation failure |
| `ClassConflict` | Resource already exists or state conflict |
| `ClassRetryable` | Transient failure, safe to retry |
| `ClassInternal` | Unexpected internal error |
| `ClassNeedsInteraction` | Requires user action (e.g., sudo prompt) |

### Result types

| Type | Purpose |
|------|---------|
| `errs.OperationResult` | Single operation result with `Status`, `Code`, `Message`, `Item`, `Exception` |
| `errs.BatchResult` | Collection of `OperationResult` items from batch operations. Has `HasErrors()`, `Errors()`, `IsOK()` helpers. |
| `errs.NeedsInteraction` | Returned when operation requires user action (sudo, confirm). Has `Code`, `Message`, `InputType`, `Context`. |

---

## CLI Integration

The CLI layer (`internal/cli/`) is the sole layer for user-facing output. It calls
API methods and formats results:

```go
// In internal/cli/vm.go:
func runVMList(cmd *cobra.Command, args []string) error {
    op := common.GetOperation(cmd)
    vms := op.VMList(cmd.Context())
    // Format as table or JSON based on --json flag
    return common.RenderList(vms)
}
```

CLI handles:
- Argument parsing (Cobra)
- Output formatting (tables, JSON)
- Confirmation prompts (`--force` / `-f` to skip)
- Verbose/debug modes (`--verbose`, `--debug`)
