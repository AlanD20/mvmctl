"""Host initialization routines."""

from __future__ import annotations

import logging
import os
import shutil
import subprocess
from pathlib import Path

from fcm.constants import PROJECT_GROUP, SUDOERS_DROP_IN_PATH, REQUIRED_BINARIES, ISO_BINARIES
from fcm.exceptions import HostError
from fcm.core.host_state import HostChange, SYSCTL_KEY, SYSCTL_CONF, _save_state
from fcm.core.host_privilege import (
    _validate_sudoers_binaries,
    _create_group,
    _get_current_user,
    _add_user_to_group,
    _write_sudoers,
)

logger = logging.getLogger(__name__)

KVM_MODULES = ["kvm"]
KVM_VENDOR_MODULES = ["kvm_intel", "kvm_amd"]


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
    try:
        result = subprocess.run(
            ["lsmod"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return False
        return any(line.split()[0] == module for line in result.stdout.splitlines() if line)
    except (OSError, subprocess.CalledProcessError):
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


def init_host(cache_dir: Path) -> list[HostChange]:
    changes: list[HostChange] = []

    if os.getuid() != 0:
        raise HostError("Root privileges required")

    if not check_kvm_access():
        raise HostError("/dev/kvm is not accessible — check permissions or load KVM modules")

    missing = check_required_binaries()
    if missing:
        raise HostError(f"Missing required binaries: {', '.join(missing)}")

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
    sudoers_exists = False
    try:
        sudoers_exists = sudoers_path.exists()
    except PermissionError:
        # Directory not readable (e.g., /etc/sudoers.d/ is root-only)
        # Treat as non-existent; _write_sudoers will fail appropriately if
        # the caller lacks privileges to create the file.
        pass
    if not sudoers_exists:
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
