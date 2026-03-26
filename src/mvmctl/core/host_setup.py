"""Host initialization routines."""

from __future__ import annotations

import logging
import os
import shutil
import subprocess
from pathlib import Path

from mvmctl.constants import (
    CONST_FILE_PERMS_STATE_FILE,
    IPTABLES_RULES_V4,
    ISO_BINARIES,
    PROJECT_GROUP,
    REQUIRED_BINARIES,
    SUDOERS_DROP_IN_PATH,
)
from mvmctl.core.host_privilege import (
    _add_user_to_group,
    _create_group,
    _generate_sudoers_content,
    _get_current_user,
    _validate_sudoers_binaries,
    _write_sudoers,
)
from mvmctl.core.host_state import SYSCTL_CONF, SYSCTL_KEY, HostChange, _save_state
from mvmctl.core.network import setup_mvm_chains
from mvmctl.exceptions import HostError

logger = logging.getLogger(__name__)

_CHAIN_EXISTS_MARKER = "MVM chains already exist"

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


def _get_active_tap_names() -> set[str]:
    try:
        result = subprocess.run(
            ["ip", "-o", "link", "show", "type", "tuntap"],
            capture_output=True,
            text=True,
            check=False,
        )
        names: set[str] = set()
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                names.add(parts[1].rstrip(":"))
        return names
    except FileNotFoundError:
        return set()


def _strip_tap_rules(rules_text: str) -> str:
    tap_names = _get_active_tap_names()
    if not tap_names:
        return rules_text
    filtered: list[str] = []
    for line in rules_text.splitlines(keepends=True):
        if any(tap in line for tap in tap_names):
            logger.debug("Excluding transient TAP rule from persistence: %s", line.strip())
            continue
        filtered.append(line)
    return "".join(filtered)


def save_iptables_rules() -> HostChange | None:
    rules_path = Path(IPTABLES_RULES_V4)

    try:
        result = subprocess.run(
            ["iptables-save"],
            capture_output=True,
            text=True,
            check=True,
        )
    except (subprocess.CalledProcessError, FileNotFoundError):
        logger.warning(
            "iptables-save unavailable — rules will not survive reboot. "
            "Install iptables-persistent (Debian/Ubuntu) or iptables-services (RHEL)."
        )
        return None

    raw = result.stdout
    if not isinstance(raw, str):
        logger.debug("iptables-save stdout is not a str (likely mocked); skipping persistence")
        return None
    filtered = _strip_tap_rules(raw)

    original: str | None = None
    if rules_path.exists():
        try:
            original = rules_path.read_text()
        except OSError:
            original = None
    if original == filtered:
        logger.debug("iptables rules already up-to-date in %s", rules_path)
        return None

    try:
        rules_path.parent.mkdir(parents=True, exist_ok=True)
        rules_path.write_text(filtered)
        rules_path.chmod(CONST_FILE_PERMS_STATE_FILE)
    except OSError as e:
        raise HostError(f"Failed to write {rules_path}: {e}") from e

    logger.info("Persisted iptables rules to %s (TAP rules excluded)", rules_path)
    return HostChange(
        setting="iptables_rules_v4",
        original_value=original,
        applied_value=str(rules_path),
        mechanism="iptables_save",
    )


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
    sudoers_stale = True
    try:
        if sudoers_path.exists():
            existing = sudoers_path.read_text()
            expected = _generate_sudoers_content(PROJECT_GROUP)
            sudoers_stale = existing != expected
    except (PermissionError, OSError):
        pass
    if sudoers_stale:
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

    chains_already_exist = setup_mvm_chains()
    if chains_already_exist:
        logger.warning("MVM iptables chains already exist; keeping existing chain state")
        changes.append(
            HostChange(
                setting="iptables_chains",
                original_value=None,
                applied_value=_CHAIN_EXISTS_MARKER,
                mechanism="noop",
            )
        )

    iptables_change = save_iptables_rules()
    if iptables_change:
        changes.append(iptables_change)

    _save_state(cache_dir, changes)
    return changes
