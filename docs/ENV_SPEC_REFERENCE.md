# Environment Spec Reference

Complete reference for the `mvm env` workflow engine YAML spec format.

For the full implementation details, see [docs/implementations/ENVIRONMENT_WORKFLOW_ENGINE.md](implementations/ENVIRONMENT_WORKFLOW_ENGINE.md).

---

## Commands

```bash
mvm env apply <spec-path>     # Provision everything in the spec
mvm env ls                    # List applied environments
mvm env diff <spec-path>      # Show what would change (spec vs state)
mvm env destroy <wf-id|path>  # Tear down exactly what was provisioned
```

---

## Spec Structure

```yaml
version: "1"

network:
  - name: <step-name>
    # ... fields

key:
  - name: <step-name>
    # ... fields

image:
  - name: <step-name>
    # ... fields

kernel:
  - name: <step-name>
    # ... fields

binary:
  - name: <step-name>
    # ... fields

vm:
  - name: <step-name>
    # ... fields

ssh:
  - name: <step-name>
    # ... fields

exec:
  - name: <step-name>
    # ... fields

copy:
  - name: <step-name>
    # ... fields
```

**All identifiers are singular** — YAML keys, step types, `depends_on`, step names.

---

## Common Fields

Every step type supports these top-level fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | **Yes** | Step name. Becomes `"type:name"` identifier. |
| `depends_on` | `[]string` | No | List of `"type:name"` dependencies. |

---

## Step Types

### `network`

Create a network for VMs to connect to.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `subnet` | `string` | **Required** | CIDR notation, e.g. `"172.27.0.0/24"`. |
| `nat` | `bool` | `true` | Enable NAT for internet access. |
| `ipv4_gateway` | `string` | Auto-computed | Gateway IP. Auto-computed from subnet if omitted. |
| `nat_gateways` | `[]string` | Auto-detected | Host interfaces for NAT. Auto-detected if empty. |
| `default` | `bool` | `false` | Set as default network for VM creation. |

**Example:**
```yaml
network:
  - name: default
    subnet: "172.27.0.0/24"
    nat: true
    default: true
```

---

### `key`

Generate or import an SSH key pair.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `algorithm` | `string` | `"ed25519"` | Key algorithm. Valid: `ed25519`, `rsa`, `ecdsa`. |
| `bits` | `int` | `0` (auto) | Key bits. 0 means algorithm default. |
| `comment` | `string` | `"{name}@{hostname}"` | Key comment. |
| `force` | `bool` | `false` | Overwrite existing key files. |
| `default` | `bool` | `false` | Set as default key for VM creation. |

**Example:**
```yaml
key:
  - name: main-key
    algorithm: ed25519
    bits: 256
    comment: "my-key"
    default: true
```

---

### `image`

Download an OS image.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | `string` | **Required** | Image type/slug, e.g. `"ubuntu"`, `"alpine"`. |
| `version` | `string` | `""` | Version tag, e.g. `"24.04"`, `"3.21"`. |
| `force` | `bool` | `false` | Force re-pull even if exists. |
| `default` | `bool` | `false` | Set as default image. |
| `no_cache` | `bool` | `false` | Skip cache layer. |
| `partition` | `int` | `0` (auto) | Partition index. 0 = auto-detect. |
| `skip_optimization` | `bool` | `false` | Skip image optimization. |
| `disabled_detectors` | `[]string` | `[]` | Disable detection methods. Valid: `type`, `label`, `size`, `filesystem`, `all`. |

**Example:**
```yaml
image:
  - name: os-image
    type: alpine
    version: "3.21"
```

---

### `kernel`

Download or build a kernel.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | `string` | **Required** | Kernel type. Valid: `firecracker`, `official`. |
| `version` | `string` | `""` | Version tag. For `firecracker` type, version is ignored. |
| `jobs` | `int` | CPU count | Build parallelism (official kernel only). |
| `keep_build_dir` | `bool` | `false` | Keep build directory after build. |
| `clean_build` | `bool` | `false` | Force clean build. |
| `kernel_config` | `string` | `""` | Path to custom kernel config file. |
| `default` | `bool` | `false` | Set as default kernel. |
| `features` | `string` | `""` | Comma-separated features, e.g. `"kvm,nftables,tuntap"`. |

**Example:**
```yaml
kernel:
  - name: default-kernel
    type: firecracker
```

---

### `binary`

Download Firecracker binaries.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | `string` | `"firecracker"` | Binary type. Only `firecracker` supported. |
| `version` | `string` | **Required** | Version tag, e.g. `"1.15.0"`. |
| `git_ref` | `string` | `""` | Build from git ref instead of downloading. |
| `default` | `bool` | `false` | Set as default binary. |
| `force` | `bool` | `false` | Force re-download. |

**Example:**
```yaml
binary:
  - name: fc-binary
    type: firecracker
    version: "1.15.0"
    default: true
```

---

### `vm`

Create a virtual machine.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `network` | `string` | Default network | Network name/ID. |
| `key` | `string` | Default key | Single SSH key name (convenience). |
| `ssh_keys` | `[]string` | `[]` | List of SSH key names. |
| `image` | `string` | Default image | Image name/ID. |
| `kernel` | `string` | Default kernel | Kernel name/ID. |
| `binary` | `string` | Default binary | Binary name/ID. |
| `vcpu` | `int` | Config default | vCPU count. |
| `mem` | `string` | Config default | Memory size. Supports `"512M"`, `"1G"`, or MiB int. |
| `disk_size` | `string` | Config default | Disk size. Supports `"20G"`, `"512M"`, etc. |
| `user` | `string` | Config default | SSH user for the VM. |
| `pci_enabled` | `bool` | Config default | Enable PCI passthrough. |
| `nested_virt` | `bool` | Config default | Enable nested virtualization. |
| `cpu_template` | `string` | `""` | Path to CPU template JSON file. |
| `console_enable` | `bool` | Config default | Enable serial console. |
| `logging_enable` | `bool` | Config default | Enable Firecracker logging. |
| `metrics_enable` | `bool` | Config default | Enable Firecracker metrics. |
| `guest_ip` | `string` | `""` | Request specific guest IP. |
| `guest_mac` | `string` | `""` | Request specific MAC address. |
| `boot_args` | `string` | Config default | Custom kernel boot args. |
| `volumes` | `[]string` | `[]` | Volume names to attach. |
| `count` | `int` | `1` | Batch count for creating multiple VMs. |
| `atomic` | `bool` | `false` | Atomic batch creation (all or nothing). |
| `skip_cleanup` | `bool` | `false` | Skip cleanup on failure. |
| `skip_deblob` | `bool` | `false` | Skip image deblobbing. |

**Example:**
```yaml
vm:
  - name: dev-vm
    network: default
    key: main-key
    image: os-image
    kernel: default-kernel
    binary: fc-binary
    vcpu: 2
    mem: 2048
    disk_size: 10G
    depends_on:
      - network:default
      - key:main-key
      - image:os-image
      - kernel:default-kernel
      - binary:fc-binary
```

---

### `exec`

Run a command inside a VM via the vsock guest agent. **Imperative** — always
re-runs on re-apply. Requires the VM to be created with vsock enabled (default).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `target` | `string` | **Required** | VM name, ID prefix, IP, or MAC address. |
| `cmd` | `string` | **Required** | Command to execute. Wrapped in `sh -c`. |
| `user` | `string` | Config default | User to run the command as. |
| `timeout` | `int` | `0` | Command timeout in seconds. 0 = no timeout. |
| `port` | `int` | `1024` | Vsock agent port override. |

**Example:**
```yaml
exec:
  - name: setup-app
    target: dev-vm
    cmd: "curl -sS https://example.com/setup.sh | sh"
    user: root
    timeout: 30
    depends_on:
      - vm:dev-vm
```

---

### `ssh`

Run a command on a VM via SSH. **Imperative** — always re-runs on re-apply.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `target` | `string` | **Required** | VM name, ID prefix, IP, or MAC address. |
| `user` | `string` | VM/config default | SSH user. |
| `key` | `string` | VM/config default | Key name or file path. |
| `cmd` | `string` | `""` | Command to execute. Empty = interactive shell. |
| `timeout` | `int` | `0` | Connection timeout in seconds. |

**Example:**
```yaml
ssh:
  - name: setup-hostname
    target: dev-vm
    user: root
    cmd: "hostnamectl set-hostname my-dev-vm"
    depends_on:
      - vm:dev-vm
```

---

### `copy`

Copy files between host and VM via vsock binary frame protocol. **Imperative** — always re-runs on re-apply.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `src` | `string \| []string` | **Required** | Source path(s). Single string auto-normalized to list. |
| `dest` | `string` | **Required** | Destination in `"vm-name:/remote/path"` format. |
| `force` | `bool` | `false` | Force overwrite existing files. |

**Destination rules** (handled by agent via `os.Stat`):
- Trailing `/` on dest → directory mode (preserves source filename)
- Existing directory → directory mode (preserves source filename)
- Non-existent or file → file mode (uses exact dest path)

**Example:**
```yaml
copy:
  - name: deploy-binary
    src: ./mvm
    dest: dev-vm:/opt/bin/mvm
    force: true
    depends_on:
      - vm:dev-vm
```

---

## Dependencies

Steps can declare dependencies on other steps using `depends_on`. The engine uses these to build a DAG and execute steps in the correct order.

**Format:** `"type:name"` — singular type prefix + step name.

```yaml
vm:
  - name: dev-vm
    depends_on:
      - network:default
      - key:main-key
      - image:os-image
```

**Inferred dependencies:** The VM step automatically infers dependencies from reference fields (`network`, `key`, `image`, `kernel`, `binary`). Explicit `depends_on` entries are deduplicated against inferred ones.

**Execution order:**
- **Apply:** Level 0 (no deps) → Level 1 → Level 2 → ...
- **Destroy:** Reverse order (Level N → ... → Level 0)

Steps within the same level run in parallel.

---

## Destroy Behavior

`mvm env destroy` runs each step's `Destroy()` method in reverse dependency order. What happens depends on the **step type**:

| Step Type | Behavior on Destroy | Rationale |
|-----------|---------------------|-----------|
| `network` | **Deleted** — iptables NAT rules removed, bridge/tap interfaces deleted, DB record removed | Runtime network state — must be torn down |
| `key` | **Deleted** — SSH key files removed from disk, DB record removed | Created per-environment |
| `vm` | **Deleted** — Firecracker process killed, console relay shut down, TAP device removed, IP lease released, volumes detached, VM directory + DB record deleted | Full lifecycle teardown |
| `image` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to re-download, shared across environments |
| `kernel` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to rebuild/re-download, shared across environments |
| `binary` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to re-download, shared across environments |
| `ssh` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (command already ran inside the VM) |
| `exec` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (command already ran inside the VM via vsock) |
| `copy` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (file was already transferred) |

**Why image/kernel/binary are preserved:** These are _downloaded assets_ cached for reuse across multiple environments. Deleting them on destroy would force a re-download on the next `env apply`, which is slow and unnecessary. They are only removed when explicitly deleted via `mvm image rm`, `mvm kernel rm`, or `mvm binary rm`.

---

## Full Example

```yaml
version: "1"

network:
  - name: default
    subnet: "172.27.0.0/24"
    nat: true
    default: true

key:
  - name: main-key
    algorithm: ed25519
    default: true

image:
  - name: os-image
    type: alpine
    version: "3.21"

kernel:
  - name: default-kernel
    type: firecracker

binary:
  - name: fc-binary
    type: firecracker
    version: "1.15.0"
    default: true

vm:
  - name: dev-vm
    network: default
    key: main-key
    image: os-image
    kernel: default-kernel
    binary: fc-binary
    vcpu: 2
    mem: 2048
    disk_size: 10G
    depends_on:
      - network:default
      - key:main-key
      - image:os-image
      - kernel:default-kernel
      - binary:fc-binary

ssh:
  - name: setup-hostname
    target: dev-vm
    user: root
    cmd: "hostnamectl set-hostname my-dev-vm"
    depends_on:
      - vm:dev-vm

exec:
  - name: bootstrap-app
    target: dev-vm
    cmd: "curl -sS https://example.com/bootstrap.sh | sh"
    user: root
    timeout: 60
    depends_on:
      - vm:dev-vm

copy:
  - name: deploy-binary
    src: ./mvm
    dest: dev-vm:/opt/bin/mvm
    depends_on:
      - vm:dev-vm
```

---

## State File

After running `mvm env apply`, the engine persists the full state to `~/.cache/mvmctl/workflows/<workflow-id>/state.yaml`.

**Structure:**

```yaml
workflow_id: "ec729934a8fb9c67"
spec_path: "./my-env.yaml"
schema_version: "1.0"
created_at: "2026-06-12T10:00:00Z"
updated_at: "2026-06-12T10:05:00Z"
resources:
  - name: "network:default"        # resource name
    type: "network"                 # resource type
    state:
      spec:                         # full input spec from YAML
        name: "default"
        subnet: "172.27.0.0/24"
        nat: true
        default: true
      output:                       # data produced by Apply
        network_id: "net-abc123"
      meta:
        was_created: true           # did we create this resource?
        spec_hash: "a1b2c3..."      # hash for drift detection
```

**Fields:**

| Field | Description |
|-------|-------------|
| `name` | Resource name (e.g. `"network:default"`) |
| `type` | Resource type (e.g. `"network"`) |
| `depends_on` | Explicit dependencies (optional) |
| `state.spec` | Full input spec from YAML — enables drift detection |
| `state.output` | Data produced by Apply (IDs, paths, etc.) |
| `state.meta.was_created` | `true` if we created the resource, `false` if pre-existing |
| `state.meta.spec_hash` | SHA256 hash of input spec — compared on re-apply for drift detection |

**Drift detection:** On `mvm env diff`, the engine hashes the current spec and compares against the saved `spec_hash`. If different, the resource is marked as drifted (shown in yellow).

**Crash resilience:** State is written after every successful step, not batched at the end. If `mvm env apply` crashes or fails partway, the state file already contains all completed steps. Re-running picks up where it left off — completed steps are skipped via existence checks. Same for `mvm env destroy` — if it fails partway, re-running destroys only the remaining resources.
