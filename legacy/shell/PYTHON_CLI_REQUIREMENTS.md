> **⚠️ ARCHIVED — Historical document from an earlier phase.**
> The project has evolved significantly. See [CONTEXT.md](../CONTEXT.md) for current domain language,
> [docs/PROJECT_ARCHITECTURE.md](../docs/PROJECT_ARCHITECTURE.md) for the current architecture,
> and [docs/API.md](../docs/API.md) for the current API reference.
> This file is kept for historical reference only.

# MicroVM Manager (mvmctl) — Python CLI Requirements

This document reconciles all requirements from the legacy development phases (Phase 1–7). It serves as the authoritative specification for the `mvmctl` project. Newer requirements overrule older ones where conflicts existed.

---

## 1. Core Principles & Architecture

### 1.1 Goals
- **Single Entry Point**: All functionality accessible via the `mvm` command.
- **Standalone**: No runtime dependency on legacy bash scripts.
- **Layered Architecture**: 
    - `cli/`: Thin Typer/Click wrappers for argument parsing and output formatting.
    - `api/`: Stable, documented Python API. Privilege checks happen here.
    - `core/`: Business logic, subprocess management, and Firecracker interaction.
    - `models/`: Pure `@dataclass` objects (no side effects).
    - `utils/`: Shared helpers (fs, process, console, http).
- **Idempotency**: All operations (fetching, creation, configuration) must be safe to re-run.
- **No Silent Failures**: Subprocess failures must raise typed exceptions with stderr captured.

### 1.2 Project Identity (The "Single Source of Truth" Rule)
- The project name (`mvmctl`) and version are defined **only** in `pyproject.toml`.
- All runtime constants (binary name, env var prefix `MVM_`, cache dir `~/.cache/mvmctl/`, network prefixes `mvm-`) must derive from this single source.
- Derived Constants Example:
    - `PROJECT_GROUP`: `mvmctl`
    - `SUDOERS_DROP_IN_PATH`: `/etc/sudoers.d/mvmctl`
    - `BRIDGE_PREFIX`: `mvm-`
    - `TAP_PREFIX`: `mvm-tap-`

### 1.3 Directory Layout
- **Config**: `~/.config/mvmctl/config.json` (Managed by `MVM_CONFIG_DIR`).
- **Cache**: `~/.cache/mvmctl/` (Managed by `MVM_CACHE_DIR`).
    - `bin/`: Firecracker/jailer binaries.
    - `kernels/`: Shared kernel images.
    - `images/`: Shared rootfs images.
    - `keys/`: Public SSH key registry.
    - `networks/`: Persistent network configurations and lease tables.
    - `vms/<name>/`: Per-VM runtime state (PID, sockets, logs, cloud-init).
    - `metadata.json`: Single registry for all cached assets (images, kernels, binaries).

---

## 2. Privilege & Security Model

### 2.1 Group-Based Elevation
- **Mechanism**: `mvm` group + sudoers drop-in at `/etc/sudoers.d/mvmctl`.
- **Setup**: `sudo mvm host init` is the only command requiring manual `sudo`. It creates the group and the `NOPASSWD` sudoers file for a specific list of binaries.
- **Privileged Binaries**: Defined in `constants.py`:
    - `/usr/sbin/ip`
    - `/usr/sbin/iptables`
    - `/usr/sbin/iptables-restore`
    - `/usr/sbin/iptables-save`
    - `/usr/sbin/sysctl`
- **Validation**: Before writing the sudoers file, `visudo -c -f <temp_file>` must be used to verify syntax.

### 2.2 Runtime Checks
- API functions must call `check_privileges(binary)` before executing privileged commands.
- It verifies:
    1. Binary exists on host (`shutil.which`).
    2. User is root OR in the `mvm` group.
- If the user lacks permissions, raise `PrivilegeError` and direct them to run `mvm host init` or `newgrp mvm`.

---

## 3. Configuration & State

### 3.1 Configuration Resolution (Priority: High to Low)
1. Explicit CLI flags.
2. `MVM_*` environment variables.
3. `config.json` file.
4. Built-in `DEFAULTS_` constants in `constants.py` which it reads it from `assets/defaults.yaml` file.

### 3.2 Global Asset Metadata
- All cached assets (kernels, images, binaries) are indexed in a single `metadata.json` file in the cache root.
- **ID System**: Assets and VMs are identified by a **full 64-char SHA256 hash** (content/timestamp-based). The CLI displays and accepts a **6-char short ID**.
- **Metadata Structure**: Must track `internal_id`, `name`, `type`, `added_at` (ISO 8601), and specific fields like `version`, `arch`, `filesystem`.

---

## 4. Asset Management

### 4.1 Firecracker Binaries (`mvm bin`)
- **Source**: Templated URLs in `assets/defaults.yaml`.
- **Version Tracking**: Tracks `full_version` (v1.15.0) and `ci_version` (v1.15).
- **Commands**: `ls` (list local/remote), `fetch`, `set-default`, `rm`.
- **Remote**: Sort by semver descending; default limit is 5.
- **Architecture**: Supports `x86_64` (default) and `aarch64`.

### 4.2 Kernels (`mvm kernel`)
- **Types**: 
    - `firecracker`: Pre-built minimal kernels. Fetching requires XML listing from S3 to resolve the latest patch version (e.g., `vmlinux-6.1.102`).
    - `official` (upstream): Built from source. Requires compilation using `config_fragments`.
- **Builds**: 
    - Performed in `/tmp/mvmctl-kernel-build-{uuid}/`.
    - `jobs` defaults to `os.cpu_count()`.
- **Compliance**: Must include `CONFIG_KVM_GUEST`, `CONFIG_VIRTIO_BLK`, `CONFIG_VIRTIO_NET`, `CONFIG_SERIAL_8250_CONSOLE`, `CONFIG_BTRFS_FS`. If missing, fail with a warning.
- **Commands**: `ls`, `fetch --type`, `set-default`, `rm`.
- **Defaulting**: Checksums are mandatory unless explicitly null in `assets/kernels.yaml`.

### 4.3 Images (`mvm image`)
- **Formats**: Supports `qcow2`, `raw`, `tar-rootfs`.
- **Import**: Allows importing local/remote files. For complex images (like Arch Linux), it must support extracting the specific root partition.
- **Commands**: `ls` (local/remote), `fetch`, `import`, `set-default`, `rm`.
- **Metadata**: Tracks filesystem extension and type (ext4, btrfs).
- **Remote Listing**: Must show available images from `assets/images.yaml`, indicating if they are already downloaded.

### 4.4 SSH Keys (`mvm key`)
- **Registry**: Stores **named public keys only**.
- **Commands**: `ls`, `add`, `create` (writes private key to `~/.ssh/`), `remove`, `inspect`.
- **Validation**: `key add` must fail if the provided file is a private key, showing a friendly error.
- **Resolution**: `vm create --ssh-key` accepts a name from the registry or a file path.

---

## 5. Network Management (`mvm network`)

### 5.1 Named Networks
- A network represents a Linux bridge with an associated CIDR and NAT rules.
- **Naming**: All devices use the `mvm-` prefix (e.g., bridge `mvm-default`).
- **Persistence**: Configured via `iptables-save` to survive reboots (excluding TAP devices). Rules must be stored in `/etc/iptables/rules.v4` (or distro equivalent).
- **Default Network**: Created automatically during host init (e.g., `172.35.0.0/24`).
- **Commands**: `ls`, `create`, `remove`, `inspect`. `inspect` must show all attached VMs with full details.

### 5.2 IP & MAC Allocation
- **IPs**: Auto-allocated from the network CIDR; tracked in `leases.json`. No DHCP server is run; IPs are passed via kernel boot args (`ip=`).
- **MACs**: Deterministically generated from VM name if not provided (prefix `02:`).

---

## 6. VM Lifecycle (`mvm vm`)

### 6.1 Creation (`mvm vm create`)
- **Flags**: `--name`, `--network`, `--ip`, `--mac`, `--kernel`, `--image`, `--ssh-key`, `--vcpus`, `--memory`, `--user-data`, `--enable-api-socket`.
- **Defaults**: Uses values from `config.json` (default image/kernel/network).
- **Cloud-init**: Injects `user-data` and `network-config` via an ISO seed image.
- **Config Injection**: 
    - `--output-config`: Saves resolved launch params + Firecracker JSON to a file.
    - `--import-config`: Reads params from file; CLI flags override file values.

### 6.2 Management & Inspection
- **`ls` / `ps`**: Shows relative timestamps (e.g., "5 minutes ago"). Columns: ID, Name, Status, IP, Added.
- **`logs`**: Supports `--follow` and `--type` (boot/console vs os/process).
- **`ssh`**: 
    - Uses `~/.ssh/` keys by default.
    - Supports `--key` for names or specific private key paths.
- **`snapshot` / `load`**: Supports basic VM state persistence via Firecracker API.

### 6.3 Removal (`mvm vm rm`)
- **Multi-VM Support**: `mvm vm rm vm1 vm2` must work.
- **Graceful Shutdown**:
    1. `SendCtrlAltDel` via API socket (if enabled, wait 5s).
    2. `SIGTERM` (wait 1s).
    3. `SIGKILL`.
- **Cleanup**: Removes TAP device, lease, known-hosts entry (`ssh-keygen -R`), and cache directory.
- **`mvm vm prune`**: Removes all stopped VMs.

---

## 7. Host Configuration (`mvm host`)

- **`host init`**: Idempotent setup. Enables IP forwarding, creates group/sudoers, ensures default network, creates custom iptables chains (e.g., `MVM-FORWARD`).
- **`host clean`**: Removes all networking (bridges, TAPs, iptables rules) but leaves sysctl/group/sudoers.
- **`host reset`**: Full rollback of all host changes (removes group, sudoers, sysctl).
- **`host ls`**: Audits current host state vs. pre-init snapshot stored in `host/state.json`.

---

## 8. CLI Conventions & Polish

- **Verbs**: Canonical is `remove` (alias `rm`), `ls` (alias `list`), `prune`.
- **Help**: `mvm help <command>` and `mvm <command> --help` are identical.
- **Formatting**: Human-readable tables by default; `--json` for automation.
- **Relative Time**: All timestamps in CLI output must be relative (e.g., "2 days ago") if under a week.
- **Onboarding**: `mvm configure` guided wizard walks through host init, binary/kernel/image download, and SSH key setup.
