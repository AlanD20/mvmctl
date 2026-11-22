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
  - [floating_ip](#floating_ip)
  - [floating_ip_grant](#floating_ip_grant)
  - [key](#key)
  - [image](#image)
  - [image_import](#image_import)
  - [kernel](#kernel)
  - [binary](#binary)
  - [vm](#vm)
  - [volume](#volume)
  - [traffic_policy](#traffic_policy)
  - [exec_policy](#exec_policy)
  - [ready](#ready)
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

The map key (`my-net`) is the step name used in references, `depends_on`, `removes`, and saved state. A resource `name` override does not change that step name.

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

Only spec version `"1"` is supported. The available sections are `network`, `floating_ip`, `floating_ip_grant`, `key`, `image`, `image_import`, `kernel`, `binary`, `vm`, `volume`, `traffic_policy`, `exec_policy`, `ready`, `exec`, `ssh`, and `copy`. Snapshot creation and restore are separate CLI operations.

VM steps use the current VM creation rules, including networkless placement, raw data volumes, and the Firecracker `1.17.0` launch requirement. `exec.allow_vm_exec` opts a command into VM-to-VM execution; a directional exec policy is also required. Neither a traffic grant nor an exec grant enables that option implicitly.

Validation rejects unknown sections and fields, malformed or missing references, missing dependency paths, self-dependencies, cycles, unsupported values, and VM `count` values other than `1` before applying steps. Nested `env` and `cpu_config` maps retain their own keys.

Reapply ensures recorded resource identities. It does not silently replace missing resources or mutate changed owned declarations: resolve the reported conflict or explicitly destroy and recreate the affected environment, preserving needed data first. Exec, SSH, copy, and readiness steps run again. Write commands and readiness checks so repeated execution is safe. `mvm env diff spec.yaml` compares the spec with saved state; it does not inspect live configuration drift.

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

`network`, `floating_ip`, `key`, `vm`, `volume`, and `image_import` accept a `name` override, defaulting to the step name. `image.name` is an optional image name; omission uses image acquisition naming. Other steps do not use `name` as a resource-name override.

`network`, `floating_ip`, `key`, and `vm` also accept `existing: <name-or-id>` to select a pre-existing resource without taking
ownership. Do not combine `existing` with creation fields; `floating_ip` still requires `network`. On reapply the saved identity is required; there is no
fallback to a replacement with the same name. The creation-field requirements below apply when `existing` is absent.

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

References do not create dependencies automatically. Each referenced step must be reachable through `depends_on`, directly or transitively; independent steps can run concurrently. References resolve recorded resource identities, including when the resource has a different `name`. Use `@key:name` in `ssh_keys` and `@volume:name` in `volumes`, or literal names/IDs for existing resources. Copy endpoints can use `@vm:name:/path` or a literal `vm-name:/path`. Commands, local paths, environment values, and other literal strings keep their `@` characters.
There is no generic output interpolation: `@floating_ip:api` resolves only in typed resource fields, not inside
`cmd` or `env` strings. To pass a resource ID to a guest helper, inspect it on the host and supply the validated ID
as an ordinary explicit command argument.

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

### `floating_ip`

Reserve an IPv4 address without associating it. An explicit network is required; environment steps do not use
the CLI's default network. See [Guest Control and floating IPs](GUEST_CONTROL.md) for delegation and fencing.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `network` | `string` | Yes | — | Network selector or `@network:name`. |
| `name` | `string` | No | Step name | 1–63 characters, starting with a lowercase letter or digit; then lowercase letters, digits, `_` or `-`. Reserved names and IP literals are rejected; e.g. `api`. |
| `ipv4` | `string` | No | Allocate | Canonical usable IPv4 in the selected subnet, e.g. `10.25.0.100`; excludes the gateway, primary and floating reservations. |
| `existing` | `string` | No | — | Select and preserve an existing reservation by name, ID, prefix or IPv4. Keep `network`; omit `name` and `ipv4`. |

This complete spec allocates an address before any VM uses the network:

```yaml
version: "1"
network:
  cluster:
    subnet: 10.25.0.0/24
floating_ip:
  api:
    network: "@network:cluster"
    depends_on: ["@network:cluster"]
```

The workflow saves its chosen immutable resource ID before creation. Retries use that ID, including after an
unknown outcome. Reapply refuses a replacement with the same name or address. Destroy removes only its saved
owned reservation and preserves `existing` selections; active ownership must be fenced before retirement.

### `floating_ip_grant`

Store one guest method permission for an exact resource and VM incarnation. The VM must be eligible in the same
network and operator. This step does not associate an address, start a lease loop or prove session readiness.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `network` | `string` | Yes | — | Network selector or `@network:name`. |
| `resource` | `string` | Yes | — | Floating-IP selector or `@floating_ip:name`. |
| `vm` | `string` | Yes | — | VM selector or `@vm:name`. |
| `method` | `string` | Yes | — | One of `inspect`, `associate`, `renew`, `release`. |
| `lease` | `string` | For associate/renew | — | Positive whole-second duration, e.g. `10s` or `1m`; forbidden for inspect/release. |

For an existing network `cluster` and running VM `controller-1` in that network, this spec reserves `api` and
delegates association. Add separate grant steps for other methods the application needs:

```yaml
version: "1"
network:
  cluster:
    existing: cluster
vm:
  controller:
    existing: controller-1
floating_ip:
  api:
    network: "@network:cluster"
    depends_on: ["@network:cluster"]
floating_ip_grant:
  controller-associate:
    network: "@network:cluster"
    resource: "@floating_ip:api"
    vm: "@vm:controller"
    method: associate
    lease: 10s
    depends_on: ["@floating_ip:api", "@vm:controller"]
```

The grant ID is saved before the effect. Reapply requires the exact grant lifetime; a stopped, restarted or
restored incarnation is not silently adopted. Destroy revokes that saved grant, including after an uncertain
creation result or VM stop. It does not revoke a different grant with matching names or permissions. There is
no `existing` selection for grant steps.

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

Create a virtual machine. Set initial labels with `labels: {cluster: demo, role: worker}`.
The `mvm vm create` equivalent is `--label cluster=demo --label role=worker`.
A VM can have up to 32 labels. Label keys and values are lowercase and contain 1–63 ASCII
letters, digits, dots, underscores, or hyphens. Keys and values must start and end with a letter or digit.
For an existing VM, use `mvm vm labels inspect` to read the revision, then
`mvm vm labels set <vm> --revision <revision> key=value` to replace its labels.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `labels` | `map[string]string` | No | Empty | Initial VM labels, committed before first launch exposes the VM. |
| `network` | `string` | No | Default network | Network step reference. |
| `key` | `string` | No | Default key | SSH key step reference (shorthand for `ssh_keys`). |
| `ssh_keys` | `[]string` | No | Default keys | `@key:name` references or existing SSH key names/IDs. |
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
| `volumes` | `[]string` | No | `[]` | `@volume:name` references or existing raw data-volume names/IDs to attach. |
| `count` | `int` | No | `1` | Only `1` is accepted. Declare a separate named step for each VM. |
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

### `volume`

Create a raw data volume or select an existing volume without taking ownership. Volumes are retained by default, including during ephemeral cleanup, and retained owned volumes keep their state entry. Use `retain: false` at creation for disposable workflow data. Existing volumes are never deleted by the workflow; combining `existing` with `retain: false` is rejected.

To retire retained data after teardown, preserve anything needed, remove the exact saved volume with
`mvm volume rm <volume-id>`, then rerun `mvm env destroy <workflow-id>` to clear its absent state entry.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | `string` | Step name | Name for a new volume. |
| `size` | `string` | — | Required when creating, e.g. `"10G"`. |
| `read_only` | `bool` | API default | Create a read-only volume. |
| `shareable` | `bool` | API default | Permit sharing subject to normal volume rules. |
| `writeback` | `bool` | API default | Writeback cache mode. |
| `existing` | `string` | Absent | Select an existing volume by name or ID. Do not combine with creation options. |
| `retain` | `bool` | `true` | Preserve workflow-created data on destroy; `false` explicitly permits its deletion. |

```yaml
volume:
  data:
    size: "10G"
    retain: true
vm:
  database:
    volumes: ["@volume:data"]
    depends_on: ["@volume:data"]
```

This fragment uses configured VM defaults. It creates an unformatted raw disk; format and mount it through guest provisioning as needed. There is no QCOW2 data-volume mode.

### `traffic_policy`

Allow TCP or UDP traffic to a destination VM, floating IP or the source network's host gateway. Same-network VM traffic also requires grants. A policy does not grant command execution.

| Field | Type | Description |
|-------|------|-------------|
| `source_network` | `string` | Source network selector or `@network:name`; mutually exclusive with `source_vm`. |
| `source_vm` | `string` | Source VM selector or `@vm:name`; mutually exclusive with `source_network`. |
| `source_labels` | `map` | Require all listed labels on the source VM. |
| `destination_labels` | `map` | Require all listed labels on the destination VM. |
| `destination_vm` | `string` | Destination VM selector or `@vm:name`; mutually exclusive with the other destination fields. |
| `destination_host` | `bool` | Set `true` for the source managed network's host gateway, not the host's LAN address or localhost. |
| `destination_floating_ip` | `string` | Floating-IP selector or `@floating_ip:name`; mutually exclusive with VM/host destinations. Requires `destination_network`. |
| `destination_network` | `string` | Explicit network selector or `@network:name`; required only with `destination_floating_ip`, forbidden otherwise. |
| `protocol` | `string` | `tcp` or `udp`. |
| `port` | `string` or `int` | Destination port or inclusive range, e.g. `5432` or `"8000-8080"`; bounds 1–65535. |
| `existing` | `string` | Select an existing policy by exact ID, verified against the declared source, destination, protocol, and port/range. |

Both creation and existing selection require exactly one source, one destination, protocol, and port/range.
Label selectors pair with another label selector or an exact VM. With `existing`, these fields must match the selected grant. Existing selection preserves the policy on destroy.

Operators may select any host port, but must explicitly declare its protocol and port/range. The host service must
listen on the gateway or a suitable wildcard address. Host-to-VM access remains outside policy management.

```yaml
traffic_policy:
  database-access:
    source_vm: "@vm:app"
    destination_vm: "@vm:database"
    protocol: tcp
    port: 5432
    depends_on: ["@vm:app", "@vm:database"]
  host-database-access:
    source_vm: "@vm:app"
    destination_host: true
    protocol: tcp
    port: 5432
    depends_on: ["@vm:app"]
```

A floating-IP destination binds the immutable reservation and its exact address. Access follows the active owner;
it does not expose that VM's primary address or other aliases. No delivery is granted while unassociated. Ownership
requires separate delegation; a traffic policy does not create it.

This complete spec selects an existing network and reservation and permits only TCP 6443 from that network:

```yaml
version: "1"
network:
  cluster:
    existing: cluster
floating_ip:
  api:
    network: "@network:cluster"
    existing: api
    depends_on: ["@network:cluster"]
traffic_policy:
  api-access:
    source_network: "@network:cluster"
    destination_floating_ip: "@floating_ip:api"
    destination_network: "@network:cluster"
    protocol: tcp
    port: 6443
    depends_on: ["@floating_ip:api"]
```

Floating-IP policy steps save exact resolved endpoint IDs and an ownership claim before applying the grant. Retries
and destroy use those saved IDs, so a renamed or recreated endpoint is not adopted. Existing policy selections
must match the declared floating resource and network and are preserved on destroy.

### `exec_policy`

Create a directional VM-to-VM execution grant for one non-root destination user. This does not create that user, enable `exec.allow_vm_exec`, or grant host access. With `existing`, supply the matching source, destination, and target user to verify and preserve the selected policy.

| Field | Type | Description |
|-------|------|-------------|
| `source_vm` | `string` | Source VM selector or `@vm:name`. |
| `source_labels` | `map` | Require all listed labels on the source VM. |
| `destination_labels` | `map` | Require all listed labels on the destination VM. |
| `destination_vm` | `string` | Destination VM selector or `@vm:name`. |
| `target_user` | `string` | Required non-root guest user for creation or existing selection. |
| `existing` | `string` | Select an existing policy by exact ID, verified against the declared endpoints and target user. |

```yaml
exec_policy:
  app-maintenance:
    source_labels: {role: app}
    destination_vm: "@vm:database"
    target_user: app
    depends_on: ["@vm:database"]
```

### `ready`

Wait for a guest command to exit successfully through the vsock agent. Checks rerun on every apply. The deadline includes connection attempts, command execution, and retry waits; cancellation stops the check. Invalid input and authority failures are terminal errors.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `target` | `string` | — | Required VM selector or `@vm:name`. |
| `cmd` | `string` | — | Required guest check command; zero exit status means ready. |
| `user` | `string` | Config default | Guest user for the check. |
| `timeout` | `int` | `60` | Positive total deadline in seconds. |
| `interval` | `int` | `1` | Positive retry interval in seconds. |

```yaml
ready:
  database-up:
    target: "@vm:database"
    cmd: "test -f /run/database-ready"
    timeout: 60
    interval: 1
    depends_on: ["@vm:database"]
exec:
  configure-app:
    target: "@vm:app"
    cmd: "echo database-ready"
    depends_on: ["@vm:app", "@ready:database-up", "@traffic_policy:database-access"]
```

The marker is an example: use a command that checks your application's actual readiness. Consumers must depend on the ready step. This is a guest check, not a host shell or host-port probe. Its total deadline is specific to readiness; ordinary `exec.timeout` and CLI `--timeout` keep their connection/probe meaning.

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
| `volume` | Retained by default; delete only workflow-created volumes declared `retain: false`, after dependent VMs |
| `traffic_policy` | Revoke the exact workflow-created grant before its endpoints; preserve existing policies |
| `exec_policy` | Revoke the exact workflow-created grant before its endpoints; preserve existing policies |
| `ready` | No-op — checks have no teardown action |
| `image` | Preserved — cached asset, not destroyed |
| `image_import` | Preserved — cached asset, not destroyed |
| `kernel` | Preserved — cached asset, not destroyed |
| `binary` | Preserved — cached asset, not destroyed |
| `ssh` | No-op — ephemeral side-effect |
| `exec` | No-op — ephemeral side-effect |
| `copy` | No-op — ephemeral side-effect |

### Explicit removals (`removes`)

The `removes` field requests removal after the whole pipeline succeeds. It does not free resources between steps.
Kernels are always retained by environments: a kernel reference in `removes` is rejected before effects. Manage
kernel retirement separately through the kernel CLI. For example, this fragment captures a builder image before
requesting removal of the builder VM:

```yaml
image_import:
  capture-base:
    source: "@vm:builder"
    depends_on: ["@vm:builder"]
    removes:
      - "@vm:builder"
```

### Ephemeral specs (`ephemeral: true`)

Set `ephemeral: true` beside `version` to attempt teardown after pipeline success or failure. Cached images, kernels, binaries, retained volumes, and pre-existing resources remain available. Cleanup failures are returned alongside any original apply failure and can leave resources or state behind; inspect `mvm env ls` and retry `mvm env destroy` when necessary.

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

Resource identities and ownership are persisted after acquisition. Saved `was_created` stays bound to that identity; finding a different object with the same name never transfers ownership. Policy cleanup checks the exact policy incarnation and durable workflow claim atomically; that claim only restricts cleanup and grants no new policy authority. Persistence and cleanup failures are reported, with remaining state retained for retry. Reapply reruns commands, copies, and readiness; it is not a transactional resume or live configuration reconciliation.
