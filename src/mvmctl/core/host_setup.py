"""Host initialization routines.

This module contains pure setup operations. The orchestration function init_host()
has been moved to api/host.py.
"""

from __future__ import annotations

import logging
import os
import shutil
import subprocess
from pathlib import Path

from mvmctl.constants import (
    IPTABLES_RULES_V4,
    ISO_BINARIES,
    REQUIRED_BINARIES,
)
from mvmctl.exceptions import HostError
from mvmctl.models.host import HostStateChange

SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path("/etc/sysctl.d/mvmctl.conf")

logger = logging.getLogger(__name__)

KVM_MODULES = ["kvm"]
KVM_VENDOR_MODULES = ["kvm_intel", "kvm_amd"]


def check_kvm_access() -> bool:
    """Check if /dev/kvm is accessible."""
    kvm = Path("/dev/kvm")
    return kvm.exists() and os.access(kvm, os.R_OK | os.W_OK)


def check_required_binaries() -> list[str]:
    """Check for required binaries and return list of missing ones."""
    missing: list[str] = []
    for name in REQUIRED_BINARIES:
        if not shutil.which(name):
            missing.append(name)
    has_iso = any(shutil.which(b) for b in ISO_BINARIES)
    if not has_iso:
        missing.append(" or ".join(ISO_BINARIES))
    return missing


def check_cloud_localds() -> bool:
    """Check if cloud-localds is available."""
    return shutil.which("cloud-localds") is not None


def get_ip_forward_status() -> str:
    """Get the current IP forwarding status."""
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
    """Check if a kernel module is loaded."""
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
    """Load a kernel module."""
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


def _unload_module(module: str) -> None:
    """Unload a kernel module."""
    try:
        subprocess.run(
            ["modprobe", "-r", module],
            capture_output=True,
            text=True,
            check=True,
        )
    except subprocess.CalledProcessError as e:
        raise HostError(f"Failed to unload kernel module {module}: {e}") from e
    except FileNotFoundError as e:
        raise HostError("modprobe command not found") from e


def _detect_kvm_modules() -> list[str]:
    """Detect which KVM modules are available on the system."""
    available_modules: list[str] = []
    for module in KVM_MODULES + KVM_VENDOR_MODULES:
        try:
            result = subprocess.run(
                ["modprobe", "--dry-run", module],
                capture_output=True,
                text=True,
                check=True,
            )
            if result.returncode == 0:
                available_modules.append(module)
        except (OSError, subprocess.CalledProcessError):
            continue
    return available_modules


def _ensure_kvm() -> None:
    """Ensure KVM modules are loaded."""
    for module in KVM_MODULES:
        if not _is_module_loaded(module):
            _load_module(module)

    vendor_loaded = any(_is_module_loaded(m) for m in KVM_VENDOR_MODULES)
    if not vendor_loaded:
        for module in KVM_VENDOR_MODULES:
            try:
                _load_module(module)
                break
            except HostError:
                continue


def _enable_ip_forward() -> HostStateChange | None:
    """Enable IP forwarding if not already enabled."""
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

    return HostStateChange(
        setting=SYSCTL_KEY,
        original_value=current,
        applied_value="1",
        mechanism="sysctl",
    )


def _ensure_kvm_modules() -> list[HostStateChange]:
    """Ensure KVM modules are loaded and return list of changes."""
    changes: list[HostStateChange] = []
    for module in KVM_MODULES:
        if _is_module_loaded(module):
            logger.debug("Module %s already loaded", module)
            continue
        _load_module(module)
        changes.append(
            HostStateChange(
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
                    HostStateChange(
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


def _persist_sysctl() -> HostStateChange | None:
    """Persist sysctl configuration to file.

    Returns:
        HostStateChange if file was created/modified, None if already correct.
    """
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

    return HostStateChange(
        setting="sysctl_persist_file",
        original_value=original,
        applied_value=str(SYSCTL_CONF),
        mechanism="file_create",
    )


def _get_active_tap_names() -> set[str]:
    """Get set of active TAP device names."""
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
    """Strip TAP-related rules from iptables rules text."""
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


def save_iptables_rules() -> HostStateChange | None:
    """Save iptables rules to file.

    Returns:
        HostStateChange if rules were saved, None if already up-to-date.
    """
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
    except OSError as e:
        raise HostError(f"Failed to write {rules_path}: {e}") from e

    logger.info("Persisted iptables rules to %s (TAP rules excluded)", rules_path)
    return HostStateChange(
        setting="iptables_rules_v4",
        original_value=original,
        applied_value=str(rules_path),
        mechanism="iptables_save",
    )
