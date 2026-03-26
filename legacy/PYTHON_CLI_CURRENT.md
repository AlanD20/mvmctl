# MicroVM Manager (mvmctl) — Current Implementation Audit

This document compares the current codebase state against the authoritative requirements in `legacy/PYTHON_CLI_REQUIREMENTS.md`. It identifies discrepancies, missing features, and behavioral mismatches.

---

## 1. Project Identity & Architecture

### 1.1 Project Identity (Matches/Mismatches)
- [x] **Match**: `PROJECT_NAME` is resolved from `importlib.metadata`, using `mvmctl` as the bootstrap.
- [x] **Match**: `CLI_NAME` resolves to `mvm`.
- [!] **Mismatch**: `BRIDGE_NAME` is hardcoded as `mvm-br0` in `constants.py`, but `_default_bridge_name` logic exists to create `mvm-default`. The requirement §5.1 implies all bridges should follow the `mvm-{network_name}` pattern.
- [!] **Mismatch**: `DEFAULTS_` vs `FALLBACK_`. Requirement §3.1 specifies `DEFAULTS_` constants, but the code currently uses `DEFAULT_` and `FALLBACK_`.

### 1.2 Directory Layout
- [x] **Match**: Helper functions in `utils/fs.py` properly resolve cache/config paths using `SUDO_USER` awareness.
- [x] **Match**: `metadata.json` is used as the single registry for all assets.

---

## 2. Privilege & Security Model

- [x] **Match**: `SUDOERS_DROP_IN_PATH` resolves to `/etc/sudoers.d/mvmctl`.
- [x] **Match**: `PROJECT_GROUP` is set to `mvmctl`.
- [x] **Match**: `PRIVILEGED_BINARIES` list is centralized in `constants.py` and loaded from `defaults.yaml`.
- [!] **Mismatch**: While `check_privileges` exists, `host init` does not explicitly call `visudo -c -f` before writing the sudoers file in the current `core/host_privilege.py` (it uses `_write_sudoers` which lacks the validation step).

---

## 3. Asset Management

### 3.1 Metadata System
- [!] **Mismatch**: `migrate_legacy_metadata` still exists in `core/metadata.py` despite the explicit instruction in Requirement §6.3 to remove it.
- [!] **Mismatch**: Asset IDs are currently generated using `hashlib.sha256(f"{name}:{time.time()}".encode()).hexdigest()` in `vm_lifecycle.py`. Requirement §3.2 specifies a full hash of the **file content** + current timestamp for assets.

### 3.2 Kernels & Images
- [x] **Match**: Kernel types `firecracker` and `official` are supported.
- [!] **Missing**: The XML listing logic for `firecracker` kernels (S3 listing to resolve patch versions) is partially implemented in `core/kernel.py` but needs verification for full compliance with the "resolve latest patch" rule.
- [!] **Missing**: Custom kernel build compliance check (warning if `CONFIG_KVM_GUEST` etc. are missing) is not fully enforced in the build pipeline.
- [x] **Match**: Image fetching supports `ubuntu-24.04`, `archlinux`, etc.

---

## 4. Network Management

- [x] **Match**: Default network CIDR is `172.35.0.0/24`.
- [!] **Mismatch**: `TAP_PREFIX` is `mvm-tap`, resulting in `mvm-tap-abc`. Requirement §5.1 specifies `mvm-` as the device prefix, implying `mvm-tap-` is correct but TAP names should follow `mvm-{net}-{vm}-{rand}`. Currently, `_generate_tap_name` uses `mvm-{net_part}-{vm_part}-{rand}` (3-char parts).
- [!] **Mismatch**: Bridge naming. Requirement §5.1 says "All devices use the `mvm-` prefix (e.g., bridge `mvm-default`)". Currently, `constants.py` has `BRIDGE_NAME = f"{device_prefix()}-br0"`.

---

## 5. VM Lifecycle

- [x] **Match**: Graceful shutdown sequence (API `SendCtrlAltDel` -> SIGTERM -> SIGKILL) is correctly implemented in `vm_lifecycle.py`.
- [x] **Match**: Cloud-init ISO seed generation and injection are present.
- [!] **Mismatch**: `vm remove` calls `manager.deregister(vm.id)`, but it should also ensure the IP is released back to the correct network's lease table (this is implemented but needs careful verification for multi-network edge cases).
- [!] **Missing**: `vm create --output-config` and `--import-config` are defined as requirements but the current implementation in `vm_lifecycle.py` does not fully handle the "merge CLI flags with config file" logic for these specific flags.

---

## 6. CLI Conventions

- [x] **Match**: `remove` is the canonical verb with `rm` as an alias.
- [!] **Missing**: Relative timestamp formatting (e.g., "5 minutes ago") is implemented in some places but not universally applied to all date/time outputs across all commands (e.g., `kernel ls`, `image ls`).
- [!] **Missing**: `mvm help <command>` as a top-level alias for `mvm <command> --help` is not globally implemented in the Typer root.
