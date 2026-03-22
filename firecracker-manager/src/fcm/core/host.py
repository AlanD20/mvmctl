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

from fcm.constants import PROJECT_NAME
from fcm.exceptions import HostError

logger = logging.getLogger(__name__)

REQUIRED_BINARIES = ["ip", "iptables", "qemu-img"]
ISO_BINARIES = ["mkisofs", "genisoimage"]
SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(f"/etc/sysctl.d/{PROJECT_NAME}.conf")
KVM_MODULES = ["kvm"]
KVM_VENDOR_MODULES = ["kvm_intel", "kvm_amd"]


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
    _state_file(cache_dir).write_text(json.dumps(data, indent=2) + "\n")


def init_host(cache_dir: Path) -> list[HostChange]:
    changes: list[HostChange] = []

    if not check_kvm_access():
        raise HostError("/dev/kvm is not accessible — check permissions or load KVM modules")

    missing = check_required_binaries()
    if missing:
        raise HostError(f"Missing required binaries: {', '.join(missing)}")

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
            target = Path(change.applied_value)
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
