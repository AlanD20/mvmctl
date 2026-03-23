"""Host configuration management for Firecracker prerequisites."""

from __future__ import annotations

import json
import logging
import os
import shutil
import subprocess
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path

from fcm.constants import CLI_NAME, PRIVILEGED_BINARIES, PROJECT_GROUP, PROJECT_NAME, SUDOERS_DROP_IN_PATH
from fcm.exceptions import HostError

logger = logging.getLogger(__name__)

REQUIRED_BINARIES = ["ip", "iptables", "qemu-img"]
ISO_BINARIES = ["mkisofs", "genisoimage"]
SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(f"/etc/sysctl.d/{PROJECT_NAME}.conf")
KVM_MODULES = ["kvm"]
KVM_VENDOR_MODULES = ["kvm_intel", "kvm_amd"]

# Allowlists for restore_host() — only these keys/paths may be restored with root privileges.
RESTORABLE_SYSCTL_KEYS: frozenset[str] = frozenset({"net.ipv4.ip_forward"})
RESTORABLE_FILE_PATHS: frozenset[Path] = frozenset({Path(SUDOERS_DROP_IN_PATH), SYSCTL_CONF})


@dataclass
class HostChange:
    setting: str
    original_value: str | None
    applied_value: str
    mechanism: str


@dataclass
class HostState:
    init_timestamp: str
    changes: list[HostChange]


def _state_dir(cache_dir: Path) -> Path:
    return cache_dir / "host"


def _state_file(cache_dir: Path) -> Path:
    return _state_dir(cache_dir) / "state.json"


def check_kvm_access() -> bool:
    kvm = Path("/dev/kvm")
    return kvm.exists() and os.access(kvm, os.R_OK | os.W_OK)


def check_required_binaries() -> list[str]:
    missing: list[str] = []
    for name in REQUIRED_BINARIES:
        if not shutil.which(name):
            missing.append(name)
    has_iso = any(shutil.which(b) for b in ISO_BINARIES)
    if not has_iso:
        missing.append(" or ".join(ISO_BINARIES))
    return missing


def get_ip_forward_status() -> str:
    try:
        result = subprocess.run(
            ["sysctl", "-n", SYSCTL_KEY],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to read {SYSCTL_KEY}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("sysctl command not found") from e


def _is_module_loaded(module: str) -> bool:
    result = subprocess.run(
        ["lsmod"],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return False
    for line in result.stdout.splitlines():
        if line.split() and line.split()[0] == module:
            return True
    return False


def _load_module(module: str) -> None:
    try:
        subprocess.run(
            ["modprobe", module],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to load kernel module {module}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("modprobe command not found") from e


def _enable_ip_forward() -> HostChange | None:
    current = get_ip_forward_status()
    if current == "1":
        logger.debug("IP forwarding already enabled")
        return None

    try:
        subprocess.run(
            ["sysctl", "-w", f"{SYSCTL_KEY}=1"],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to enable IP forwarding: {e}") from e
    except FileNotFoundError as e:
        raise HostError("sysctl command not found") from e

    return HostChange(
        setting=SYSCTL_KEY,
        original_value=current,
        applied_value="1",
        mechanism="sysctl",
    )


def _get_current_user() -> str:
    """Get the current login name."""
    import pwd

    return pwd.getpwuid(os.getuid()).pw_name


def _group_exists(group_name: str) -> bool:
    import grp

    try:
        grp.getgrnam(group_name)
        return True
    except KeyError:
        return False


def _user_in_group(username: str, group_name: str) -> bool:
    import grp

    try:
        g = grp.getgrnam(group_name)
        return username in g.gr_mem
    except KeyError:
        return False


def _create_group(group_name: str) -> bool:
    """Create system group. Returns True if created, False if already exists."""
    if _group_exists(group_name):
        return False
    try:
        subprocess.run(
            ["groupadd", "--system", group_name],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to create group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("groupadd command not found") from e


def _add_user_to_group(username: str, group_name: str) -> bool:
    """Add user to group. Returns True if added, False if already a member."""
    if _user_in_group(username, group_name):
        return False
    try:
        subprocess.run(
            ["usermod", "-aG", group_name, username],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to add {username} to group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("usermod command not found") from e


def _validate_sudoers_binaries() -> None:
    """Verify all PRIVILEGED_BINARIES exist on the host."""
    for binary in PRIVILEGED_BINARIES:
        if not Path(binary).exists():
            pkg_map = {
                "/usr/sbin/ip": "iproute2",
                "/usr/sbin/iptables": "iptables",
                "/usr/sbin/iptables-restore": "iptables",
                "/usr/sbin/iptables-save": "iptables",
                "/usr/sbin/sysctl": "procps",
            }
            pkg = pkg_map.get(binary, "unknown package")
            raise HostError(f"Required binary not found: {binary} (install {pkg})")


def _generate_sudoers_content(group_name: str) -> str:
    """Generate sudoers drop-in content from PRIVILEGED_BINARIES."""
    binaries_str = ", ".join(PRIVILEGED_BINARIES)
    return (
        f"# Managed by {CLI_NAME} — do not edit manually.\n"
        f"# To remove: {CLI_NAME} host reset\n"
        f"%{group_name} ALL=(root) NOPASSWD: {binaries_str}\n"
    )


def _write_sudoers(path: Path, group_name: str) -> None:
    """Generate, validate with visudo, and write sudoers drop-in file."""
    import tempfile

    content = _generate_sudoers_content(group_name)
    # Write to temp file for validation
    with tempfile.NamedTemporaryFile(mode="w", suffix=".sudoers", delete=False) as f:
        f.write(content)
        tmp_path = f.name
    try:
        result = subprocess.run(
            ["visudo", "-c", "-f", tmp_path],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            raise HostError(f"Generated sudoers file failed visudo validation: {result.stderr}")
    except FileNotFoundError:
        logger.warning("visudo not found — skipping sudoers validation")
    finally:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass
    # Write to final location
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        path.chmod(0o440)
    except OSError as e:
        raise HostError(f"Failed to write sudoers file {path}: {e}") from e


def _remove_sudoers(path: Path) -> bool:
    """Remove sudoers drop-in file. Returns True if removed."""
    if not path.exists():
        return False
    try:
        path.unlink()
        return True
    except OSError as e:
        raise HostError(f"Failed to remove sudoers file {path}: {e}") from e


def _remove_group(group_name: str) -> bool:
    """Remove system group. Returns True if removed."""
    if not _group_exists(group_name):
        return False
    try:
        subprocess.run(
            ["groupdel", group_name],
            capture_output=True,
            text=True,
            check=True,
        )
        return True
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to remove group {group_name}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("groupdel command not found") from e


def _persist_sysctl() -> HostChange | None:
    content = f"{SYSCTL_KEY} = 1\n"
    if SYSCTL_CONF.exists() and SYSCTL_CONF.read_text() == content:
        logger.debug("sysctl persist file already exists with correct content")
        return None

    original: str | None = None
    if SYSCTL_CONF.exists():
        original = SYSCTL_CONF.read_text()

    try:
        SYSCTL_CONF.parent.mkdir(parents=True, exist_ok=True)
        SYSCTL_CONF.write_text(content)
    except OSError as e:
        raise HostError(f"Failed to write {SYSCTL_CONF}: {e}") from e

    return HostChange(
        setting="sysctl_persist_file",
        original_value=original,
        applied_value=str(SYSCTL_CONF),
        mechanism="file_create",
    )


def _ensure_kvm_modules() -> list[HostChange]:
    changes: list[HostChange] = []
    for module in KVM_MODULES:
        if _is_module_loaded(module):
            logger.debug("Module %s already loaded", module)
            continue
        _load_module(module)
        changes.append(
            HostChange(
                setting=f"module:{module}",
                original_value=None,
                applied_value=module,
                mechanism="modprobe",
            )
        )

    vendor_loaded = any(_is_module_loaded(m) for m in KVM_VENDOR_MODULES)
    if not vendor_loaded:
        for module in KVM_VENDOR_MODULES:
            try:
                _load_module(module)
                changes.append(
                    HostChange(
                        setting=f"module:{module}",
                        original_value=None,
                        applied_value=module,
                        mechanism="modprobe",
                    )
                )
                break
            except HostError:
                continue

    return changes


def _save_state(cache_dir: Path, changes: list[HostChange]) -> None:
    state_dir = _state_dir(cache_dir)
    state_dir.mkdir(parents=True, exist_ok=True)
    state = HostState(
        init_timestamp=datetime.now(timezone.utc).isoformat(),
        changes=changes,
    )
    data = {
        "init_timestamp": state.init_timestamp,
        "changes": [asdict(c) for c in state.changes],
    }
    sf = _state_file(cache_dir)
    sf.write_text(json.dumps(data, indent=2) + "\n")
    os.chmod(sf, 0o600)


def init_host(cache_dir: Path) -> list[HostChange]:
    changes: list[HostChange] = []

    if not check_kvm_access():
        raise HostError("/dev/kvm is not accessible — check permissions or load KVM modules")

    missing = check_required_binaries()
    if missing:
        raise HostError(f"Missing required binaries: {', '.join(missing)}")

    # Group and sudoers setup (requires root)
    _validate_sudoers_binaries()

    group_created = _create_group(PROJECT_GROUP)
    if group_created:
        changes.append(
            HostChange(
                setting=f"group:{PROJECT_GROUP}",
                original_value=None,
                applied_value=PROJECT_GROUP,
                mechanism="groupadd",
            )
        )

    username = _get_current_user()
    user_added = _add_user_to_group(username, PROJECT_GROUP)
    if user_added:
        changes.append(
            HostChange(
                setting=f"group_member:{username}",
                original_value=None,
                applied_value=f"{username}:{PROJECT_GROUP}",
                mechanism="usermod",
            )
        )

    sudoers_path = Path(SUDOERS_DROP_IN_PATH)
    if not sudoers_path.exists():
        _write_sudoers(sudoers_path, PROJECT_GROUP)
        changes.append(
            HostChange(
                setting="sudoers_dropin",
                original_value=None,
                applied_value=str(sudoers_path),
                mechanism="file_create",
            )
        )

    change = _enable_ip_forward()
    if change:
        changes.append(change)

    change = _persist_sysctl()
    if change:
        changes.append(change)

    module_changes = _ensure_kvm_modules()
    changes.extend(module_changes)

    _save_state(cache_dir, changes)
    return changes


def prune_host(cache_dir: Path) -> list[str]:
    """Tear down all bridges, TAPs, iptables rules and revert host sysctl changes.

    Does NOT remove VM cache files, images, kernels, or binaries.

    Returns a list of summary strings describing what was torn down.
    """
    # Import here to avoid circular imports
    from fcm.core.network_manager import list_networks, remove_network

    summary: list[str] = []

    # Tear down all named networks (bridges, TAPs, iptables rules)
    try:
        networks = list_networks()
    except subprocess.CalledProcessError:
        networks = []

    for net in networks:
        try:
            remove_network(net.name)
            summary.append(f"Removed network '{net.name}' (bridge: {net.bridge})")
        except subprocess.CalledProcessError as e:
            summary.append(f"Warning: failed to remove network '{net.name}': {e}")

    # Revert sysctl changes using saved snapshot
    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError:
        pass  # No saved state is acceptable

    # Remove the state snapshot file if it exists
    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary


def get_host_state(cache_dir: Path) -> HostState | None:
    path = _state_file(cache_dir)
    if not path.exists():
        return None
    try:
        data = json.loads(path.read_text())
        return HostState(
            init_timestamp=data["init_timestamp"],
            changes=[HostChange(**c) for c in data["changes"]],
        )
    except (json.JSONDecodeError, KeyError, TypeError) as e:
        raise HostError(f"Corrupt state file {path}: {e}") from e


def restore_host(cache_dir: Path) -> list[HostChange]:
    state = get_host_state(cache_dir)
    if not state:
        raise HostError("No saved host state to restore")

    reverted: list[HostChange] = []
    for change in reversed(state.changes):
        if change.mechanism == "sysctl" and change.original_value is not None:
            # S-C1: Validate sysctl key against allowlist before applying
            if change.setting not in RESTORABLE_SYSCTL_KEYS:
                logger.warning(
                    "Skipping disallowed sysctl key '%s' from state file", change.setting
                )
                continue
            try:
                subprocess.run(
                    ["sysctl", "-w", f"{change.setting}={change.original_value}"],
                    capture_output=True,
                    text=True,
                    check=True,
                )
                reverted.append(
                    HostChange(
                        setting=change.setting,
                        original_value=change.applied_value,
                        applied_value=change.original_value,
                        mechanism="sysctl",
                    )
                )
            except subprocess.CalledProcessError as e:
                raise HostError(f"Failed to revert {change.setting}: {e}") from e
            except FileNotFoundError as e:
                raise HostError("sysctl command not found") from e

        elif change.mechanism == "file_create":
            target = Path(change.applied_value).resolve()
            # S-C2: Validate file path against allowlist before writing
            if not any(target == allowed.resolve() for allowed in RESTORABLE_FILE_PATHS):
                logger.warning(
                    "Skipping disallowed file path '%s' from state file", target
                )
                continue
            if target.exists():
                try:
                    if change.original_value is not None:
                        target.write_text(change.original_value)
                    else:
                        target.unlink()
                    reverted.append(
                        HostChange(
                            setting=change.setting,
                            original_value=change.applied_value,
                            applied_value=change.original_value or "(removed)",
                            mechanism="file_remove",
                        )
                    )
                except OSError as e:
                    raise HostError(f"Failed to revert file {target}: {e}") from e

    state_file = _state_file(cache_dir)
    if state_file.exists():
        state_file.unlink()

    return reverted


def clean_host(cache_dir: Path) -> list[str]:
    """Remove all networking config (bridges, TAP devices, iptables rules).

    Does NOT revert sysctl, remove sudoers, or remove project group.
    Returns list of summary strings.
    """
    from fcm.core.network_manager import list_networks, remove_network

    summary: list[str] = []
    try:
        networks = list_networks()
    except subprocess.CalledProcessError:
        networks = []
    for net in networks:
        try:
            remove_network(net.name)
            summary.append(f"Removed network '{net.name}' (bridge: {net.bridge})")
        except subprocess.CalledProcessError as e:
            summary.append(f"Warning: failed to remove network '{net.name}': {e}")
    return summary


def reset_host(cache_dir: Path) -> list[str]:
    """Full rollback to pre-init state.

    Removes networking config, reverts sysctl, removes sudoers drop-in, and removes project group.
    Returns list of summary strings.
    """
    summary = clean_host(cache_dir)

    # Revert sysctl changes
    try:
        reverted = restore_host(cache_dir)
        for change in reverted:
            summary.append(f"Reverted {change.setting}")
    except HostError:
        pass  # No saved state is acceptable

    # Remove sudoers drop-in
    sudoers_path = Path(SUDOERS_DROP_IN_PATH)
    try:
        if _remove_sudoers(sudoers_path):
            summary.append(f"Removed sudoers file {sudoers_path}")
    except HostError as e:
        summary.append(f"Warning: {e}")

    # Remove project group
    try:
        if _remove_group(PROJECT_GROUP):
            summary.append(f"Removed group '{PROJECT_GROUP}'")
    except HostError as e:
        summary.append(f"Warning: {e}")

    # Remove state snapshot
    state_file = _state_file(cache_dir)
    if state_file.exists():
        try:
            state_file.unlink()
            summary.append("Removed host state snapshot")
        except OSError:
            pass

    return summary
