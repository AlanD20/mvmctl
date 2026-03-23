# Firecracker Manager (fcm) — Unified Project Specification

This document serves as the single source of truth for the `firecracker-manager` project, consolidating requirements from the baseline instructions and all implementation phases (1-5).

---

## 1. Core Principles and Identity

### 1.1 Project Identity
* **Single Source of Truth**: The project name (e.g., `fcm`) and version are defined exclusively in `pyproject.toml`.
* **CLI Binary**: The binary name must match the project name.
* **Environment Prefix**: All environment variables use the `<PROJECT_NAME>_` prefix (e.g., `FCM_CACHE_DIR`).
* **Network Prefixes**: Network devices use project-derived prefixes like `<project-name>-br` and `<project-name>-tap`.
* **Build-time Constants**: Project identity values are injected into `constants.py` at build time to avoid hardcoded strings.

### 1.2 Guiding Rules
* **Single Entrypoint**: All bash functionality is unified under the `fcm` subcommand structure.
* **No Magic Paths**: Binary and socket paths are resolved from configuration or flags, never hardcoded.
* **Idempotency**: Operations such as VM creation, asset fetching, and host initialization must be safe to re-run.
* **Zero-Loss Regression**: Every change must be verified against all previous phase requirements.
* **Documentation Sync**: Changes to CLI behavior or API functions must be immediately updated in `README.md`, `docs/API.md`, and `docs/RELEASE.md`.

---

## 2. System Architecture and Layout

### 2.1 Cache Directory Layout
**Default Root**: `~/.cache/<project-name>/` | **Override**: `<PROJECT_NAME>_CACHE_DIR`.

<cache-root>/
  bin/                      # Versioned Firecracker and jailer binaries
    firecracker-v1.x.x
    jailer-v1.x.x
  kernels/                  # Cached kernel binaries (minimal and upstream)
    minimal-v6.x.x
    upstream-<hash-or-tag>
  images/                   # Rootfs images (.ext4)
    ubuntu-cloud-24.04.ext4
    firecracker-ubuntu.ext4
  keys/                     # Named public keys and registry index
    <name>.pub              # The public key file
    registry.json           # Index: name → fingerprint, comment, date added
  networks/                 # Network configuration and IP management
    <network-name>/
      config.json           # Subnet, gateway, and bridge device name
      state.json            # Host changes made for this specific network
      leases.json           # IP allocations: vm name → assigned IP
  vms/                      # Per-VM runtime state subdirectories
    <vm-name>/
      config.json           # Stored metadata, including API socket paths
      firecracker.json      # Generated Firecracker JSON configuration
      firecracker.pid       # PID of the running Firecracker process
      firecracker.api.socket # Firecracker HTTP API Unix socket (if enabled)
      console.log           # Serial console output
      cloud-init/           # Generated cloud-init nocloud seed files
  host/                     # Global host state tracking
    state.json              # Snapshot of pre-init host state for restoration

### 2.2 Privilege Management [Phase 5 Priority]
* **Group-Based Access**: Access is managed via a system group (e.g., `fcm`) and a sudoers drop-in file at `/etc/sudoers.d/<project-name>`.
* **Passwordless Sudo**: Members of the project group are granted `NOPASSWD` access to specific binaries defined in `constants.py`.
* **Sudoers Generation**: The sudoers file is generated programmatically from the `PRIVILEGED_BINARIES` list and validated with `visudo -c -f` before application.
* **Runtime Verification**: API functions calling privileged binaries must use a shared `check_privileges()` utility to verify the user has group membership or root access.

---

## 3. Configuration Management

### 3.1 Precedence Order (High to Low)
1.  Explicit CLI Flags.
2.  Environment Variables (e.g., `FCM_DEFAULT_IMAGE`).
3.  YAML Config File (`./fcm.yaml` or defined by `<PROJECT_NAME>_CONFIG`).
4.  Built-in Defaults.

### 3.2 YAML Configuration Schema
* **`defaults`**: Global defaults for `kernel`, `image`, `ssh_key`, `vcpus`, and `memory`.
* **`network`**: Configuration for `guest_ip_range`, `host_bridge`, and subnets.
* **`boot`**: Includes `lsm_flags` (Default: `"landlock,lockdown,yama,integrity,selinux,bpf"`) and `extra_boot_args`.
* **`firecracker`**: Global toggles for `enable_api_socket` and `enable_pci`.

---

## 4. CLI Command Specification

### 4.1 Global Standards
* **Help Consistency**: `fcm help <command>`, `fcm <command> --help`, and `fcm <command> help` must yield identical output at all levels.
* **Formatting**: Outputs default to human-readable tables (Rich); a `--json` flag is required for all listing commands.

### 4.2 VM Management (`vm`)
| Command | Alias | Description |
| :--- | :--- | :--- |
| `ls` | `list` | List VMs with status, IP, PID, and API socket info. |
| `create` | — | Spawn a new microVM. Automates rootfs copying and cloud-init. |
| `remove` | `rm` | **Graceful Shutdown**: Sends `SendCtrlAltDel` via API socket, then SIGTERM/SIGKILL if needed. |
| `ssh` | — | Connect to a VM via name or IP. |
| `logs` | — | Stream serial console output; supports `--follow` / `-f`. |

**`vm create` Critical Flags**:
* `--network <name>`: Attach to a named network (Default: `"default"`).
* `--ip <address>`: Static IP (within network CIDR) or auto-allocated if omitted.
* `--mac <mac>`: Static MAC or random locally-administered (02:xx) if omitted.
* `--ssh-key <name|path>`: Named key from registry or direct path.
* `--user-data <path>`: Custom cloud-init; merges with SSH key if both provided.
* `--enable-api-socket`: Enables the HTTP API socket in the VM cache dir.

### 4.3 Network Management (`network`)
* **Named Networks**: Modeled after `docker network`; encapsulates bridge devices, CIDRs, and NAT rules.
* **Persistence**: Stores lease tables in `networks/<name>/leases.json` to survive reboots.
* **Subcommands**: `ls`, `create` (with `--cidr` and `--gateway`), `remove` (alias `rm`), and `inspect`.

### 4.4 Asset Management (`asset`)
* **Binaries**: `ls`, `fetch <version>`, `use <version>`, and `remove` for Firecracker versions.
* **Kernels**: `fetch` (minimal), `build` (upstream from source), `ls`, and `remove`.
* **Images**: `fetch <type>` (Ubuntu, Arch, Debian), `ls`, and `remove`.

### 4.5 SSH Key Registry (`key`)
* **Public Key Store**: Caches named public keys; private keys remain on the user's filesystem.
* **Subcommands**: `ls`, `add <name> <path>`, `create <name>`, `remove` (alias `rm`), and `inspect`.

### 4.6 Host Configuration (`host`) [Phase 5 Naming]
* **`init`**: **Requires Sudo.** Sets up group, sudoers, sysctl, and the `default` network.
* **`clean`**: Removes all network artifacts (bridges, taps, NAT).
* **`reset`**: **Full Rollback.** Reverts sysctl, removes sudoers, group, and networking.
* **`ls`**: Audits the current host state against the pre-init snapshot.

### 4.7 Guided Onboarding (`configure`)
* An interactive wizard that guides users through `host init`, binary downloads, image selection, and SSH key creation.
* **Idempotency**: Skips already completed steps by checking existing cache state.

---

## 5. Implementation Standards

### 5.1 `constants.py` Structure
Central repository for operational constants derived from `PROJECT_NAME`:
* `PROJECT_GROUP`, `SUDOERS_DROP_IN_PATH`.
* `DEFAULT_NETWORK_NAME`, `DEFAULT_NETWORK_CIDR`.
* `BRIDGE_PREFIX`, `TAP_PREFIX`.
* `PRIVILEGED_BINARIES` list for sudoers and runtime checks.

### 5.2 Internal Python API
* **Thin CLI**: The CLI acts as a presentation layer; all business logic lives in the `api/` module.
* **Data Models**: Use Pydantic or Dataclasses for structured returns.
* **Error Handling**: Raise typed exceptions from `exceptions.py` (e.g., `FCMError`, `PrivilegeError`).

### 5.3 Testing and Distribution
* **Testing**: Mandatory 80% coverage; mocks required for subprocesses, networking, and KVM.
* **Distribution**: PyInstaller `--onefile` builds for Ubuntu 22.04 and 24.04; support for `pip`, `pipx`, and `uvx`.
* **CI/CD**: Automatic linting (Ruff), type checking (Mypy), and testing on push; version-tagged releases for binaries.
