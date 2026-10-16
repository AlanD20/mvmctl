# Environment Spec Reference

Complete reference for the `mvm env` workflow engine YAML spec format.

## Table of Contents

- [Overview](#overview)
- [Compatibility](#compatibility)
- [Top-Level Fields](#top-level-fields)
- [Syntax](#syntax)
  - [Common fields](#common-fields)
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
  - [Explicit removals (`removes`)](#explicit-removals-removes)
  - [Ephemeral specs (`ephemeral: true`)](#ephemeral-specs-ephemeral-true)
- [Full Example](#full-example)
- [State File](#state-file)

---

## Overview

An env spec is a YAML file that describes an environment — networks, SSH keys, images, kernels, binaries, VMs, and the commands that configure them. Running `mvm env apply spec.yaml` provisions everything in dependency order. Section examples below are fragments to combine under one `version: "1"` header; the [full example](#full-example) includes its prerequisites.

The format uses **typed top-level sections** (one per step type) with **map keys as step names**:

```yaml
version: "1"
ephemeral: false

network:
  my-net:
    subnet: "172.27.0.0/24"
```

The map key (`my-net`) is the step name used in `depends_on`, `removes`, and saved state. Keep resource names equal to step names when using cross-step references.

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

## Compatibility

Only spec version `"1"` is supported. The available sections are `network`, `key`, `image`, `image_import`, `kernel`, `binary`, `vm`, `exec`, `ssh`, and `copy`. There are no policy, volume, or snapshot creation steps. Create policies and data volumes with their CLI commands before applying a spec that uses them.

VM steps use the current VM creation rules, including networkless placement, raw data volumes, and the Firecracker `1.17.0` launch requirement. `exec.allow_vm_exec` can opt a command into VM-to-VM execution, but it does not create the required directional exec policy. The removed VM-wide `trusted` field grants no authority. Unknown fields are not consistently rejected; a successful parse does not prove that an option took effect.

Declare one VM per step. Although `count` and `atomic` decode as VM creation inputs, workflow state records only the first returned VM. Reapplying skips an existing VM rather than updating its settings. Exec, SSH, and copy steps run again. `mvm env diff spec.yaml` compares the spec with saved state; it does not inspect live configuration drift.

`mvm env apply` and `mvm env destroy` look for `mvmctl.yaml`, then `mvmctl.yml`, when no argument is given. Apply also accepts an HTTP or HTTPS spec URL. `mvm env diff` requires a spec path or workflow ID. The aliases `up` and `down` mean `apply` and `destroy`.

---

## Top-Level Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `version` | `string` | — | **Required.** Currently only `"1"`. |
| `ephemeral` | `bool` | `false` | Attempt teardown after pipeline success or failure. Shared assets and pre-existing resources are preserved; cleanup errors can leave resources or state behind. |

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

Each section is a map from step name to parameters, not a YAML list. The syntax block above is a template; the [full example](#full-example) is a complete spec.

### Common fields

Every step supports dependency and removal lists:

| Field | Type | Description |
|-------|------|-------------|
| `depends_on` | `[]string` | List of `"@type:name"` dependencies. |
| `removes` | `[]string` | Resources to remove after the entire pipeline succeeds. |

`network`, `key`, `vm`, and `image_import` accept a `name` override, defaulting to the step name. `image.name` is an optional image name; omission uses image acquisition naming. Other steps do not use `name` as a resource-name override.

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

References do not create dependencies automatically. Add `depends_on` for every prerequisite; independent steps can run concurrently. Scalar fields such as `network`, `key`, `image`, `kernel`, `binary`, and `target` accept bare names. Use `@type:name` in dependency and removal lists. Use bare key names in `ssh_keys`, existing volume names or IDs in `volumes`, and `vm-name:/path` for copy endpoints.

---

## Step Types

### `network`

Create a network for VMs to connect to.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `subnet` | `string` | **Yes** | — | Canonical masked IPv4 CIDR wholly inside RFC1918 space, prefix length at most `/30`; e.g. `"172.27.0.0/24"`. |
| `nat_enabled` | `bool` | No | `true` | Enable NAT for internet access. |
| `ipv4_gateway` | `string` | No | First usable address | Canonical usable IPv4 address inside the subnet. |
| `default` | `bool` | No | `false` | Set as default network. |

Omit `nat_gateways`: it is rejected. The privileged network service selects the NAT uplink.

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

Generate an SSH key pair.

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
| `set_default` | `bool` | No | `false` | Set as default image. |
| `partition` | `int` | No | `0` (auto) | Non-negative partition index. |
| `skip_optimization` | `bool` | No | `false` | Skip filesystem optimization. |
| `disabled_detectors` | `[]string` | No | `[]` | Disable specific detectors. |

**Example:**
```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    depends_on: ["@vm:builder"]
    removes:
      - "@vm:builder"
```

---

### `kernel`

Download or build a kernel.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | `string` | **Yes** | — | Kernel type: `firecracker` (pre-built) or `official` (build from source). |
| `version` | `string` | No | Configured kernel version | Official build version. Firecracker prebuilt selection ignores this field. |
| `jobs` | `int` | No | Configured build jobs, then CPU count | Build parallelism (official only). |
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
| `version` | `string` | No | Latest | Download version; use `"1.17.0"` for VM launch. |
| `default` | `bool` | No | `false` | Set as default binary. |
| `force` | `bool` | No | `false` | Force re-download. |

**Example:**
```yaml
binary:
  fc-bin:
    version: "1.17.0"
    default: true
```

---

### `vm`

Create a virtual machine.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `network` | `string` | No | Default network | Network step reference. |
| `key` | `string` | No | Default key | SSH key step reference (shorthand for `ssh_keys`). |
| `ssh_keys` | `[]string` | No | Default keys | Existing SSH key names or IDs; use bare names for keys created by this spec. |
| `image` | `string` | No | Default image | Image step reference. |
| `kernel` | `string` | No | Default kernel | Kernel step reference. |
| `binary` | `string` | No | Default binary | Binary step reference. |
| `vcpu` | `int` | No | Config default | vCPU count. Range: 1-32. |
| `mem` | `string` | No | Config default | Memory, e.g. `"512M"`, `"1G"`, or bare MiB int. |
| `disk_size` | `string` | No | Image minimum rootfs size | Disk size, e.g. `"10G"`, `"512M"`. |
| `user` | `string` | No | Config default | SSH user. |
| `pci_enabled` | `bool` | No | Config default | Enable PCI. |
| `nested_virt` | `bool` | No | Config default | Enable nested virtualization. |
| `cpu_template` | `string` | No | `""` | Path to CPU template JSON. |
| `cpu_config` | `map` | No | Absent | Inline CPU configuration; mutually exclusive with `cpu_template`. |
| `console_enable` | `bool` | No | Config default | Enable serial console. |
| `logging_enable` | `bool` | No | Config default | Enable logging. |
| `metrics_enable` | `bool` | No | Config default | Enable metrics. |
| `guest_ip` | `string` | No | `""` | Request specific guest IP. |
| `guest_mac` | `string` | No | `""` | Request specific MAC. |
| `boot_args` | `string` | No | Config default | Custom kernel boot args. |
| `lsm_flags` | `string` | No | Config default | Kernel LSM arguments. |
| `no_network` | `bool` | No | `false` | Create without a network interface. Conflicts with `network`, `guest_ip`, `guest_mac`, network boot arguments, and NET cloud-init. |
| `skip_ci_network_config` | `bool` | No | `false` | Skip generated cloud-init network configuration. |
| `custom_cloud_init_config` | `string` | No | Absent | Path to custom cloud-init user data. |
| `cloud_init_mode` | `string` | No | `off` | One of `off`, `inject`, `net`, `iso`. |
| `cloud_init_iso_path` | `string` | No | Absent | Existing cloud-init ISO path; also set `cloud_init_mode: iso`. |
| `keep_cloud_init_iso` | `bool` | No | `false` | Keep a generated cloud-init ISO. |
| `volumes` | `[]string` | No | `[]` | Existing raw data-volume names or IDs to attach. No volume creation step exists. |
| `count` | `int` | No | `1` | Keep at `1`: workflow state tracks one VM per step. |
| `atomic` | `bool` | No | `false` | Batch rollback option; use separate VM steps instead of batches in specs. |
| `skip_cleanup` | `bool` | No | `false` | Skip cleanup on failure. |
| `skip_deblob` | `bool` | No | `false` | Skip image deblobbing. |
| `vsock_port` | `int` | No | Config default | Vsock port. Default: `1024`. |
| `writeback` | `bool` | No | Config default | Writeback cache mode. |

Omit `nocloud_net_port`: any explicit value is rejected. NET cloud-init uses reserved gateway TCP port `8000`. QCOW2 remains supported for image import; the raw-only restriction applies to attached data volumes.

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
| `timeout` | `int` | No | `0` (configured probe timeout) | Positive values override connection/probe timeout in seconds. Command execution has no absolute timeout; cancellation stops it. |
| `port` | `int` | No | `0` (auto) | Vsock port override. |
| `env` | `map[string]string` | No | `{}` | Environment variables. |
| `ignore_errors` | `bool` | No | `false` | Continue on non-zero exit. |
| `no_sync` | `bool` | No | `false` | Skip guest filesystem sync after the command. |
| `allow_vm_exec` | `bool` | No | `false` | Allow this command to request VM-to-VM execution under an existing directional exec policy. |

`mvm env apply --env KEY=VALUE` overrides the same key in exec-step `env` maps. It does not alter SSH steps or interpolate YAML fields.

**Example:**
```yaml
exec:
  setup-app:
    target: dev-vm
    cmd: "uname -a"
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
| `timeout` | `int` | No | Unset | SSH connection timeout in seconds. |
| `env` | `map[string]string` | No | `{}` | Environment variables. |

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
| `dest` | `string` | **Yes** | — | `"vm-name:/remote/path"` when copying into a VM, or a local path when copying out. |
| `force` | `bool` | No | `false` | Force overwrite. |
| `no_sync` | `bool` | No | `false` | Skip guest filesystem sync after an upload. |

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

`mvm env destroy` tears down resources created by the workflow in reverse dependency order. Resources that existed before apply are preserved. Exec, SSH, and copy side effects are not undone.

| Step Type | Destroy Behavior |
|-----------|-----------------|
| `network` | Deleted — bridge, NAT rules, DB record |
| `key` | Deleted — key files, DB record |
| `vm` | Deleted — runtime stopped, TAP removed, data volumes detached, VM record removed |
| `image` | Preserved — cached asset, not destroyed |
| `image_import` | Preserved — cached asset, not destroyed |
| `kernel` | Preserved — cached asset, not destroyed |
| `binary` | Preserved — cached asset, not destroyed |
| `ssh` | No-op — ephemeral side-effect |
| `exec` | No-op — ephemeral side-effect |
| `copy` | No-op — ephemeral side-effect |

### Explicit removals (`removes`)

The `removes` field requests removal after the whole pipeline succeeds. It does not free resources between steps. For example, this fragment captures a builder image before requesting removal of the builder VM:

```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    depends_on: ["@vm:builder"]
    removes:
      - "@vm:builder"
```

### Ephemeral specs (`ephemeral: true`)

Set `ephemeral: true` beside `version` to attempt teardown after pipeline success or failure. Cached images, kernels, and binaries remain available. Cleanup errors are logged and can leave resources or state behind; inspect `mvm env ls` and retry `mvm env destroy` when necessary.

---

## Full Example

Save this spec as `mvmctl.yaml`, then run `mvm env apply`. It creates a VM and runs `uname -a` after the VM step completes. Local KVM and host initialization are prerequisites.

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
    version: "1.17.0"
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
    cmd: "uname -a"
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

State is written after successful steps. Reapplying checks existing resources and reruns command and copy steps; it is not a transactional resume or live configuration reconciliation.
