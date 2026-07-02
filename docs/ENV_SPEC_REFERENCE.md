# Environment Spec Reference

Complete reference for the `mvm env` workflow engine YAML spec format.

## Table of Contents

- [Commands](#commands)
- [Spec Structure](#spec-structure)
- [Common Fields](#common-fields)
- [Step Types](#step-types)
  - [network](#network)
  - [key](#key)
  - [image](#image)
  - [kernel](#kernel)
  - [binary](#binary)
  - [vm](#vm)
  - [exec](#exec)
  - [ssh](#ssh)
  - [copy](#copy)
- [Dependencies](#dependencies)
- [Destroy Behavior](#destroy-behavior)
- [Full Example](#full-example)
- [State File](#state-file)

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

image_import:
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

**All identifiers are singular** — YAML keys, step types, `depends_on`, and step names use singular form.

---

## Common Fields

Every step type supports these top-level fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | **Yes** | Step name. Becomes `"type:name"` identifier. |
| `depends_on` | `[]string` | No | List of `"type:name"` dependencies. |
| `removes` | `[]string` | No | List of `"type:name"` resources to destroy after this step succeeds. |
| `env` | `map[string]string` | No | Environment variable overrides for `exec` and `ssh` commands. |

---

## Step Types

### `network`

Create a network for VMs to connect to.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"network:name"`. |
| `subnet` | `string` | **Yes** | — | CIDR notation, e.g. `"172.27.0.0/24"`. |
| `nat_enabled` | `bool` | No | `true` | Enable NAT for internet access. |
| `ipv4_gateway` | `string` | No | auto-computed | Gateway IP. Auto-computed from subnet if omitted. |
| `nat_gateways` | `[]string` | No | auto-detected | Host interfaces for NAT. Auto-detected if empty. |
| `default` | `bool` | No | `false` | Set as default network for VM creation. |

**Example:**
```yaml
network:
  - name: default
    subnet: "172.27.0.0/24"
    nat_enabled: true
    default: true
```

---

### `key`

Generate or import an SSH key pair.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"key:name"`. |
| `algorithm` | `string` | No | `"ed25519"` | Key algorithm. Valid values: `ed25519`, `rsa`, `ecdsa`. |
| `bits` | `int` | No | `0` (auto) | Key bits in RSA. `0` means algorithm default (e.g. 4096 for RSA). |
| `comment` | `string` | No | `"{name}@{hostname}"` | Key comment. |
| `force` | `bool` | No | `false` | Overwrite existing key files. |
| `default` | `bool` | No | `false` | Set as default key for VM creation. |

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

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"image:name"`. |
| `type` | `string` | **Yes** | — | Image type/slug, e.g. `"ubuntu"`, `"alpine"`, `"debian"`. |
| `version` | `string` | No | `""` (latest) | Version tag, e.g. `"24.04"`, `"3.21"`. |
| `force` | `bool` | No | `false` | Force re-pull even if exists. |
| `default` | `bool` | No | `false` | Set as default image. |
| `no_cache` | `bool` | No | `false` | Skip cache layer. |
| `partition` | `int` | No | `0` (auto) | Partition index. `0` = auto-detect. |
| `skip_optimization` | `bool` | No | `false` | Skip image optimization. |
| `disabled_detectors` | `[]string` | No | `[]` | Disable detection methods. Valid values: `type`, `label`, `size`, `filesystem`, `all`. |

**Example:**
```yaml
image:
  - name: os-image
    type: alpine
    version: "3.21"
```

---

### `image_import`

Import a local image file or copy a VM's rootfs into the image cache.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"image_import:name"`. |
| `source` | `string` | **Yes** | — | Source path to a raw image (`.raw`/`.img`), qcow2, tar-rootfs archive (`.tar`/`.tar.gz`/`.tar.xz`/`.tgz`), or a VM selector (name or ID). |
| `format` | `string` | No | `""` (auto) | Image format override: `"raw"`, `"qcow2"`, `"tar"`. |
| `version` | `string` | No | `""` | Version tag for the imported image. |
| `force` | `bool` | No | `false` | Overwrite existing image with the same name. |
| `default` | `bool` | No | `false` | Set as default image. |
| `skip_optimization` | `bool` | No | `false` | Skip filesystem optimization. |
| `disabled_detectors` | `[]string` | No | `[]` | Disable specific detectors. |

**Example:**
```yaml
image_import:
  - name: my-custom-image
    source: /path/to/image.raw
    format: raw
    default: true

  - name: from-vm
    source: my-running-vm

  - name: capture-base
    source: builder
    deps: [exec:setup-builder]
    removes: [vm:builder]     # ← builder VM destroyed after import
```

---

### `kernel`

Download or build a kernel.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"kernel:name"`. |
| `type` | `string` | **Yes** | — | Kernel type. Valid values: `firecracker` (pre-built CI kernel), `official` (built from source). |
| `version` | `string` | No | `""` (latest) | Version tag (e.g. CI version like `1.15` for firecracker, kernel version like `6.19.9` for official). |
| `jobs` | `int` | No | CPU count | Build parallelism (official kernel only). |
| `keep_build_dir` | `bool` | No | `false` | Keep build directory after build. |
| `clean_build` | `bool` | No | `false` | Force clean build (skip cache). |
| `kernel_config` | `string` | No | `""` | Path to custom kernel config file. |
| `default` | `bool` | No | `false` | Set as default kernel. |
| `features` | `string` | No | `""` | Comma-separated features, e.g. `"kvm,nftables,tuntap"`. Use `"all"` or `"*"` to enable every feature in the selected kernel spec. |

**Example:**
```yaml
kernel:
  - name: default-kernel
    type: firecracker
```

---

### `binary`

Download Firecracker binaries.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"binary:name"`. |
| `type` | `string` | No | `"firecracker"` | Binary type. Valid values: `firecracker`. |
| `version` | `string` | **Yes** | — | Version tag, e.g. `"1.15.0"`. |
| `git_ref` | `string` | No | `""` | Build from git ref instead of downloading. |
| `default` | `bool` | No | `false` | Set as default binary. |
| `force` | `bool` | No | `false` | Force re-download. |

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

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"vm:name"`. |
| `network` | `string` | No | Default network | Network name/ID (step reference resolves to ID). |
| `key` | `string` | No | Default key | Single SSH key name (convenience shorthand for `ssh_keys`). |
| `ssh_keys` | `[]string` | No | `[]` | List of SSH key names. |
| `image` | `string` | No | Default image | Image name/ID (step reference resolves to ID). |
| `kernel` | `string` | No | Default kernel | Kernel name/ID (step reference resolves to ID). |
| `binary` | `string` | No | Default binary | Binary name/ID (step reference resolves to ID). |
| `vcpu` | `int` | No | Config default | vCPU count. Range: 1-32. |
| `mem` | `string` | No | Config default | Memory size. Supports `"512M"`, `"1G"`, or bare MiB int (e.g. `2048`). |
| `disk_size` | `string` | No | Config default (min from image) | Disk size. Supports `"20G"`, `"512M"`, etc. |
| `user` | `string` | No | Config default | SSH user for the VM. |
| `pci_enabled` | `bool` | No | Config default | Enable PCI support. |
| `nested_virt` | `bool` | No | Config default | Enable nested virtualization (requires PCI). |
| `cpu_template` | `string` | No | `""` | Path to CPU template JSON file. |
| `console_enable` | `bool` | No | Config default | Enable serial console. |
| `logging_enable` | `bool` | No | Config default | Enable Firecracker logging. |
| `metrics_enable` | `bool` | No | Config default | Enable Firecracker metrics. |
| `guest_ip` | `string` | No | `""` | Request specific guest IP. |
| `guest_mac` | `string` | No | `""` | Request specific MAC address. |
| `boot_args` | `string` | No | Config default | Custom kernel boot args. |
| `volumes` | `[]string` | No | `[]` | Volume names to attach. |
| `count` | `int` | No | `1` | Batch count for creating multiple VMs. |
| `atomic` | `bool` | No | `false` | Atomic batch creation (all or nothing). |
| `skip_cleanup` | `bool` | No | `false` | Skip cleanup on failure. |
| `skip_deblob` | `bool` | No | `false` | Skip image deblobbing. |
| `vsock_port` | `int` | No | Config default | Vsock port for guest agent communication. Default: `1024`. |
| `writeback` | `bool` | No | Config default | Use writeback cache mode for drives (guest fsync honored). |

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

Run a command inside a VM via the vsock guest agent. **Imperative** — always re-runs on re-apply.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"exec:name"`. |
| `target` | `string` | **Yes** | — | VM name, ID prefix, IP, or MAC address. |
| `cmd` | `string` | **Yes** | — | Command to execute. Wrapped in `sh -c` by the agent. |
| `user` | `string` | No | Config default | User to run the command as. |
| `timeout` | `int` | No | `0` | Command timeout in seconds. `0` = no timeout. |
| `port` | `int` | No | `0` | Vsock agent port override. `0` = auto-assigned at runtime (default 1024). |
| `env` | `map[string]string` | No | `{}` | Environment variable overrides passed to the command inside the VM. |
| `ignore_errors` | `bool` | No | `false` | Continue workflow if the command exits with non-zero code. |

**Example:**
```yaml
exec:
  - name: setup-app
    target: dev-vm
    cmd: "./deploy.sh"
    user: root
    timeout: 30
    env:
      DEPLOY_ENV: staging
    depends_on:
      - vm:dev-vm
```

---

### `ssh`

Run a command on a VM via SSH. **Imperative** — always re-runs on re-apply.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"ssh:name"`. |
| `target` | `string` | **Yes** | — | VM name, ID prefix, IP, or MAC address. |
| `user` | `string` | No | VM/config default | SSH user. |
| `key` | `string` | No | VM/config default | Key name or file path. |
| `cmd` | `string` | No | `""` | Command to execute. Empty = interactive shell. |
| `timeout` | `int` | No | `0` | Connection timeout in seconds. |
| `env` | `map[string]string` | No | `{}` | Environment variable overrides prepended to the command using `env K=V cmd`. |
| `ignore_errors` | `bool` | No | `false` | Continue workflow if the command exits with non-zero code. |

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

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | `string` | **Yes** | — | Step name. Becomes `"copy:name"`. |
| `src` | `string \| []string` | **Yes** | — | Source path(s). Single string auto-normalized to `[]string{"path"}`. |
| `dest` | `string` | **Yes** | — | Destination in `"vm-name:/remote/path"` format. |
| `force` | `bool` | No | `false` | Force overwrite existing files. |

**Destination rules:**
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

Steps can also declare resources to clean up after they complete using `removes`:

```yaml
image_import:
  - name: capture-base
    source: builder
    removes: [vm:builder]
```

The `removes` field lists `"type:name"` resources to destroy immediately after this step's `Apply()` succeeds — before downstream steps run. This frees resources mid-pipeline (e.g., tearing down a builder VM after its rootfs is captured).

**Relationship:** `depends_on` = "need this first", `removes` = "now clean this up". Same level, opposite direction.

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

`mvm env destroy` runs each step's `Destroy()` method in reverse dependency order. Behavior depends on step type:

| Step Type | Behavior on Destroy | Rationale |
|-----------|---------------------|-----------|
| `network` | **Deleted** — iptables NAT rules removed, bridge/tap interfaces deleted, DB record removed | Runtime network state — must be torn down |
| `key` | **Deleted** — SSH key files removed from disk, DB record removed | Created per-environment |
| `vm` | **Deleted** — Firecracker process killed, console relay shut down, TAP device removed, IP lease released, volumes detached, VM directory + DB record deleted | Full lifecycle teardown |
| `image` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to re-download, shared across environments |
| `image_import` | **Preserved** — files stay in cache, DB record kept | Asset — imported image is reusable across environments |
| `kernel` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to rebuild/re-download, shared across environments |
| `binary` | **Preserved** — files stay in cache, DB record kept | Asset — expensive to re-download, shared across environments |
| `ssh` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (command already ran) |
| `exec` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (command already ran via vsock) |
| `copy` | **No-op** — no persistent resources to clean up | Ephemeral side-effect (file already transferred) |

**Why image/kernel/binary are preserved:** These are downloaded assets cached for reuse across multiple environments. Deleting them on destroy would force a re-download on the next `env apply`. They are only removed when explicitly deleted via `mvm image rm`, `mvm kernel rm`, or `mvm binary rm`.

### Mid-pipeline removals (`removes` field)

The `removes` field declared on any step destroys resources **immediately after that step completes** — not during `env destroy`. This is useful for freeing resources mid-pipeline:

```yaml
image_import:
  - name: capture-base
    source: builder
    removes: [vm:builder]
```

The builder VM is torn down right after its rootfs is captured, before downstream steps start. Removals use the same API calls as destroy but run at the applying step's position in the DAG, not at the end. Failure to remove is non-fatal (logged as a warning).

---

## Full Example

```yaml
version: "1"

network:
  - name: default
    subnet: "172.27.0.0/24"
    nat_enabled: true
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
    env:
      APP_ENV: production
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
  - name: "network:default"
    type: "network"
    depends_on: ["image:os-image"]
    state:
      spec:
        network_id: "net-abc123"
        subnet: "172.27.0.0/24"
      meta:
        was_created: true
        spec_hash: "a1b2c3..."
```

**Fields:**

| Field | Description |
|-------|-------------|
| `name` | Resource name (e.g. `"network:default"`) |
| `type` | Resource type (e.g. `"network"`) |
| `depends_on` | Explicit dependencies |
| `state.spec` | Step state output — IDs, properties, and configuration from the applied resource |
| `state.meta.was_created` | `true` if created by the workflow, `false` if pre-existing |
| `state.meta.spec_hash` | SHA256 hash of input spec YAML — compared on re-apply for drift detection |

**Drift detection:** On `mvm env diff`, the engine compares each step's spec hash against the saved `spec_hash`. If different, the resource is marked as drifted (shown in yellow).

**Crash resilience:** State is written after every successful step, not batched at the end. If `mvm env apply` crashes partway, the state file already contains all completed steps. Re-running picks up where it left off — completed steps are skipped via existence checks. Same for `mvm env destroy`.
