# Environment Spec Reference

Complete reference for the `mvm env` workflow engine YAML spec format.

## Table of Contents

- [Overview](#overview)
- [Syntax](#syntax)
- [Step Types](#step-types)
  - [network](#network)
  - [key](#key)
  - [image](#image)
  - [image_import](#image_import)
  - [kernel](#kernel)
  - [binary](#binary)
  - [vm](#vm)
  - [exec](#exec)
  - [ssh](#ssh)
  - [copy](#copy)
- [References](#references)
- [Destroy Behavior](#destroy-behavior)
- [Full Example](#full-example)
- [State File](#state-file)

---

## Overview

An env spec is a YAML file that describes an environment — networks, SSH keys, images, kernels, binaries, VMs, and the commands that configure them. Running `mvm env apply spec.yaml` provisions everything in dependency order.

The format uses **typed top-level sections** (one per step type) with **map keys as step names**:

```yaml
version: "1"
ephemeral: false

network:
  my-net:
    subnet: "172.27.0.0/24"
```

The map key (`my-net`) IS the step name — used in `depends_on`, `removes`, and the state file. An optional `name` field inside the params overrides the resource name (e.g., the bridge name for a network, the VM name, the key name).

Cross-step references use the **`@type:name`** format:

```yaml
vm:
  dev-vm:
    network: "@network:my-net"
    depends_on:
      - "@network:my-net"
```

The `@` sigil makes references visually distinct from literal values.

---

## Top-Level Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `version` | `string` | — | **Required.** Currently only `"1"`. |
| `ephemeral` | `bool` | `false` | Auto-run `env destroy` after successful apply. |

---

## Syntax

```yaml
version: "1"

network:
  <name>:               # ← map key = step name
    subnet: ...          # ← step-specific fields

key:
  <name>:
    algorithm: ...
```

**Every step type section is a map from step name to its params.** No list dashes. An optional `name` field inside the params overrides the resource name (see Common fields below).

### Common fields

Every step supports these fields:

| Field | Type | Description |
|-------|------|-------------|
| `name` | `string` | Resource name override. Defaults to the step name (map key). |
| `depends_on` | `[]string` | List of `"@type:name"` dependencies. |
| `removes` | `[]string` | List of `"@type:name"` resources to destroy after this step succeeds. |

---

## References

Cross-resource references use the `@type:name` format:

```yaml
depends_on:
  - "@network:default"
  - "@key:main-key"
  - "@image:os-image"

vm:
  dev-vm:
    network: "@network:default"
    key: "@key:main-key"
    image: "@image:os-image"
```

The `@` prefix distinguishes references from literal string values. The type prefix (`network`, `key`, `image`, etc.) disambiguates steps with the same name under different types.

**Bare names:** Reference fields like `network`, `key`, `image`, `kernel`, `binary`, and `target` accept bare step names (without `@` or `type:`) for convenience. However, `depends_on` and `removes` entries must use the full `@type:name` format because the engine matches them against step identifiers in the DAG.

---

## Step Types

### `network`

Create a network for VMs to connect to.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `subnet` | `string` | **Yes** | — | CIDR notation, e.g. `"172.27.0.0/24"`. |
| `nat_enabled` | `bool` | No | `true` | Enable NAT for internet access. |
| `ipv4_gateway` | `string` | No | auto-computed | Gateway IP. |
| `nat_gateways` | `[]string` | No | auto-detected | Host interfaces for NAT. |
| `default` | `bool` | No | `false` | Set as default network. |

**Example:**
```yaml
network:
  default:
    subnet: "172.27.0.0/24"
    nat_enabled: true
    default: true
```

---

### `key`

Generate or import an SSH key pair.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `algorithm` | `string` | No | `"ed25519"` | Key algorithm. Valid: `ed25519`, `rsa`, `ecdsa`. |
| `bits` | `int` | No | `0` (auto) | Key bits (for RSA). |
| `comment` | `string` | No | `"{name}@{hostname}"` | Key comment. |
| `force` | `bool` | No | `false` | Overwrite existing key files. |
| `default` | `bool` | No | `false` | Set as default key. |

**Example:**
```yaml
key:
  main-key:
    algorithm: ed25519
    default: true
```

---

### `image`

Download an OS image.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | `string` | **Yes** | — | Image type/slug, e.g. `"ubuntu"`, `"alpine"`, `"debian"`. |
| `version` | `string` | No | `""` (latest) | Version tag, e.g. `"24.04"`, `"3.21"`. |
| `force` | `bool` | No | `false` | Force re-pull even if exists. |
| `default` | `bool` | No | `false` | Set as default image. |
| `no_cache` | `bool` | No | `false` | Skip cache layer. |
| `partition` | `int` | No | `0` (auto) | Partition index. |
| `skip_optimization` | `bool` | No | `false` | Skip image optimization. |
| `disabled_detectors` | `[]string` | No | `[]` | Disable detection methods. |

**Example:**
```yaml
image:
  os-image:
    type: alpine
    version: "3.23"
```

---

### `image_import`

Import a local image file or copy a VM's rootfs.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `source` | `string` | **Yes** | — | File path, or `"@vm:<name>"` to import a VM's rootfs. |
| `format` | `string` | No | `""` (auto) | Format override: `raw`, `qcow2`, `tar`. |
| `version` | `string` | No | `""` | Version tag for the imported image. |
| `force` | `bool` | No | `false` | Overwrite existing image. |
| `default` | `bool` | No | `false` | Set as default image. |
| `skip_optimization` | `bool` | No | `false` | Skip filesystem optimization. |
| `disabled_detectors` | `[]string` | No | `[]` | Disable specific detectors. |

**Example:**
```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    removes:
      - "@vm:builder"
```

---

### `kernel`

Download or build a kernel.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | `string` | **Yes** | — | Kernel type: `firecracker` (pre-built) or `official` (build from source). |
| `version` | `string` | No | `""` (latest) | Version tag. |
| `jobs` | `int` | No | CPU count | Build parallelism (official only). |
| `keep_build_dir` | `bool` | No | `false` | Keep build directory. |
| `clean_build` | `bool` | No | `false` | Force clean build. |
| `kernel_config` | `string` | No | `""` | Path to custom kernel config file. |
| `default` | `bool` | No | `false` | Set as default kernel. |
| `features` | `string` | No | `""` | Comma-separated features, e.g. `"kvm,nftables"`. Use `"all"` or `"*"` to enable all features. |

**Example:**
```yaml
kernel:
  fc-kernel:
    type: firecracker
```

---

### `binary`

Download Firecracker binaries.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | `string` | No | `"firecracker"` | Binary type. |
| `version` | `string` | **Yes** | — | Version tag, e.g. `"1.16.0"`. |
| `git_ref` | `string` | No | `""` | Build from git ref instead of downloading. |
| `default` | `bool` | No | `false` | Set as default binary. |
| `force` | `bool` | No | `false` | Force re-download. |

**Example:**
```yaml
binary:
  fc-bin:
    version: "1.16.0"
    default: true
```

---

### `vm`

Create a virtual machine.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `network` | `string` | No | Default network | Network step reference. |
| `key` | `string` | No | Default key | SSH key step reference (shorthand for `ssh_keys`). |
| `ssh_keys` | `[]string` | No | `[]` | List of SSH key step references. |
| `image` | `string` | No | Default image | Image step reference. |
| `kernel` | `string` | No | Default kernel | Kernel step reference. |
| `binary` | `string` | No | Default binary | Binary step reference. |
| `vcpu` | `int` | No | Config default | vCPU count. Range: 1-32. |
| `mem` | `string` | No | Config default | Memory, e.g. `"512M"`, `"1G"`, or bare MiB int. |
| `disk_size` | `string` | No | Config default | Disk size, e.g. `"10G"`, `"512M"`. |
| `user` | `string` | No | Config default | SSH user. |
| `pci_enabled` | `bool` | No | Config default | Enable PCI. |
| `nested_virt` | `bool` | No | Config default | Enable nested virt (requires PCI). |
| `cpu_template` | `string` | No | `""` | Path to CPU template JSON. |
| `console_enable` | `bool` | No | Config default | Enable serial console. |
| `logging_enable` | `bool` | No | Config default | Enable logging. |
| `metrics_enable` | `bool` | No | Config default | Enable metrics. |
| `guest_ip` | `string` | No | `""` | Request specific guest IP. |
| `guest_mac` | `string` | No | `""` | Request specific MAC. |
| `boot_args` | `string` | No | Config default | Custom kernel boot args. |
| `volumes` | `[]string` | No | `[]` | Volume step references to attach. |
| `count` | `int` | No | `1` | Batch count. |
| `atomic` | `bool` | No | `false` | Atomic batch (all or nothing). |
| `skip_cleanup` | `bool` | No | `false` | Skip cleanup on failure. |
| `skip_deblob` | `bool` | No | `false` | Skip image deblobbing. |
| `vsock_port` | `int` | No | Config default | Vsock port. Default: `1024`. |
| `writeback` | `bool` | No | Config default | Writeback cache mode. |

**Example:**
```yaml
vm:
  dev-vm:
    network: "@network:default"
    key: "@key:main-key"
    image: "@image:os-image"
    kernel: "@kernel:fc-kernel"
    binary: "@binary:fc-bin"
    vcpu: 2
    mem: 2048
    disk_size: "10G"
    depends_on:
      - "@network:default"
      - "@key:main-key"
      - "@image:os-image"
      - "@kernel:fc-kernel"
      - "@binary:fc-bin"
```

---

### `exec`

Run a command inside a VM via vsock agent.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `target` | `string` | **Yes** | — | VM name, ID, IP, or MAC. |
| `cmd` | `string` | **Yes** | — | Command to execute. |
| `user` | `string` | No | Config default | User to run as. |
| `timeout` | `int` | No | `0` (no timeout) | Command timeout in seconds. |
| `port` | `int` | No | `0` (auto) | Vsock port override. |
| `env` | `map[string]string` | No | `{}` | Environment variables. |
| `ignore_errors` | `bool` | No | `false` | Continue on non-zero exit. |

**Example:**
```yaml
exec:
  setup-app:
    target: dev-vm
    cmd: "./deploy.sh"
    user: root
    timeout: 30
    depends_on:
      - "@vm:dev-vm"
```

---

### `ssh`

Run a command on a VM via SSH.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `target` | `string` | **Yes** | — | VM name, ID, IP, or MAC. |
| `user` | `string` | No | Config default | SSH user. |
| `key` | `string` | No | Config default | Key step reference or file path. |
| `cmd` | `string` | No | `""` | Command. Empty = interactive shell. |
| `timeout` | `int` | No | `0` | Timeout in seconds. |
| `env` | `map[string]string` | No | `{}` | Environment variables. |
| `ignore_errors` | `bool` | No | `false` | Continue on non-zero exit. |

**Example:**
```yaml
ssh:
  verify:
    target: dev-vm
    user: root
    cmd: "uname -a"
    depends_on:
      - "@vm:dev-vm"
```

---

### `copy`

Copy files between host and VM via vsock.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `src` | `string \| []string` | **Yes** | — | Source path(s). Single string auto-normalized to list. |
| `dest` | `string` | **Yes** | — | Destination in `"vm-name:/remote/path"` format. |
| `force` | `bool` | No | `false` | Force overwrite. |

**Example:**
```yaml
copy:
  deploy-bin:
    src: ./mvm
    dest: "dev-vm:/opt/bin/mvm"
    force: true
    depends_on:
      - "@vm:dev-vm"
```

---

## Destroy Behavior

`mvm env destroy` runs each step's `Destroy()` in reverse dependency order:

| Step Type | Destroy Behavior |
|-----------|-----------------|
| `network` | Deleted — bridge, NAT rules, DB record |
| `key` | Deleted — key files, DB record |
| `vm` | Deleted — Firecracker process, console relay, TAP, volumes, DB record |
| `image` | Preserved — cached asset, not destroyed |
| `image_import` | Preserved — cached asset, not destroyed |
| `kernel` | Preserved — cached asset, not destroyed |
| `binary` | Preserved — cached asset, not destroyed |
| `ssh` | No-op — ephemeral side-effect |
| `exec` | No-op — ephemeral side-effect |
| `copy` | No-op — ephemeral side-effect |

### Mid-pipeline removals (`removes`)

The `removes` field destroys resources immediately after a step completes — useful for freeing builder VMs mid-pipeline:

```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    removes:
      - "@vm:builder"
```

### Ephemeral specs (`ephemeral: true`)

Auto-runs `env destroy` after successful apply:

```yaml
version: "1"
ephemeral: true

vm:
  builder:
    image: "@image:os-image"
    ...

exec:
  build-artifact:
    target: builder
    cmd: "make build"

copy:
  retrieve:
    dest: ./dist/
    src: "builder:/output/artifact.tar.gz"
    removes:
      - "@vm:builder"
```

---

## Full Example

```yaml
version: "1"

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
  bootstrap:
    target: dev-vm
    cmd: "curl -sS https://example.com/bootstrap.sh | sh"
    depends_on:
      - "@vm:dev-vm"
```

---

## State File

After `mvm env apply`, state is persisted to `~/.cache/mvmctl/workflows/<wf-id>/state.yaml`:

```yaml
workflow_id: "ec729934a8fb9c67"
spec_path: "./my-env.yaml"
schema_version: "1.0"
created_at: "2026-07-05T10:00:00Z"
updated_at: "2026-07-05T10:05:00Z"
resources:
  - name: "network:default"
    type: "network"
    depends_on: []
    state:
      spec:
        subnet: "172.27.0.0/24"
      output:
        network_id: "net-abc123"
      meta:
        was_created: true
        spec_hash: "a1b2c3..."
```

| Field | Description |
|-------|-------------|
| `name` | Internal name (`type:name` format, no `@` sigil) |
| `type` | Resource type |
| `depends_on` | Explicit dependencies |
| `state.spec` | Input spec fields from the YAML |
| `state.output` | Created resource state (IDs, properties) |
| `state.meta.was_created` | `true` if created by workflow |
| `state.meta.spec_hash` | Hash of input spec YAML for drift detection |

State is written after every successful step (crash-resilient). Re-running picks up where it left off.
