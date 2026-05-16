"""Host service — stateless setup and stateful restore operations."""

from __future__ import annotations

import grp
import logging
import os
import shutil
import subprocess
import tempfile
from datetime import UTC, datetime
from pathlib import Path

from mvmctl.constants import (
    CONST_FILE_PERMS_SUDOERS,
    DEFAULT_SUDOERS_DIR,
    DEFAULT_SYSCTL_CONF_DIR,
    DEFAULT_SYSCTL_CONF_PATH,
    ISO_BINARIES,
    PRIVILEGED_BINARIES,
    PROJECT_NAME,
    REQUIRED_BINARIES,
    SUDOERS_DROP_IN_PATH,
)
from mvmctl.core.host._repository import HostRepository
from mvmctl.exceptions import HostError, ProcessError
from mvmctl.models import HostStateChangeItem
from mvmctl.utils._system import run_cmd

logger = logging.getLogger(__name__)

SYSCTL_KEY = "net.ipv4.ip_forward"
SYSCTL_CONF = Path(DEFAULT_SYSCTL_CONF_PATH)


class HostService:
    """Stateless host setup operations and stateful restore."""

    def __init__(self, repo: HostRepository) -> None:
        self._repo = repo

    @staticmethod
    def _run(
        cmd: list[str],
        *,
        failure_msg: str,
        missing_msg: str,
        capture: bool = True,
        check: bool = True,
    ) -> subprocess.CompletedProcess[str]:
        """
        Run a subprocess with consistent error handling.

        Args:
            cmd: Command and arguments as a list.
            failure_msg: Message prefix for HostError on CalledProcessError.
            missing_msg: Message for HostError on FileNotFoundError.
            capture: Whether to capture stdout/stderr. Default True.
            check: Whether to raise on non-zero exit. Default True.

        Returns:
            CompletedProcess result.

        Raises:
            HostError: On CalledProcessError or FileNotFoundError.

        """
        try:
            result = run_cmd(
                cmd,
                check=check,
                capture=capture,
            )
        except ProcessError as e:
            if "Command not found" in str(e):
                raise HostError(missing_msg) from e
            raise HostError(f"{failure_msg}: {e}") from e
        return result

    @staticmethod
    def check_kvm_access() -> bool:
        """Check if /dev/kvm is accessible."""
        kvm = Path("/dev/kvm")
        return kvm.exists() and os.access(kvm, os.R_OK | os.W_OK)

    @staticmethod
    def check_required_binaries() -> list[str]:
        """Check for required binaries and return list of missing ones."""
        missing: list[str] = []
        for name in REQUIRED_BINARIES:
            if not shutil.which(name):
                missing.append(name)
        if ISO_BINARIES:
            has_iso = any(shutil.which(b) for b in ISO_BINARIES)
            if not has_iso:
                missing.append(" or ".join(ISO_BINARIES))
        return missing

    @staticmethod
    def check_cloud_localds() -> bool:
        """Check if cloud-localds is available."""
        return shutil.which("cloud-localds") is not None

    @staticmethod
    def _group_exists(group_name: str) -> bool:
        """Return True if the named group exists on the system."""
        try:
            grp.getgrnam(group_name)
            return True
        except KeyError:
            return False

    @staticmethod
    def _user_in_group(username: str, group_name: str) -> bool:
        """Return True if username is a member of group_name."""
        try:
            g = grp.getgrnam(group_name)
            return username in g.gr_mem
        except KeyError:
            return False

    @staticmethod
    def create_group(group_name: str) -> bool:
        """Create a system group. Return True if created, False if it already existed."""
        if HostService._group_exists(group_name):
            return False
        HostService._run(
            ["groupadd", "--system", group_name],
            failure_msg=f"Failed to create group {group_name}",
            missing_msg="groupadd command not found",
        )
        return True

    @staticmethod
    def add_user_to_group(username: str, group_name: str) -> bool:
        """Add a user to a group. Return True if added, False if already a member."""
        if HostService._user_in_group(username, group_name):
            return False
        HostService._run(
            ["usermod", "-aG", group_name, username],
            failure_msg=f"Failed to add {username} to group {group_name}",
            missing_msg="usermod command not found",
        )
        return True

    @staticmethod
    def remove_user_from_group(username: str, group_name: str) -> bool:
        """
        Remove a user from a system group.

        Returns:
            True if the user was removed from the group, False if not a member.

        """
        try:
            grp_info = grp.getgrnam(group_name)
            if username not in grp_info.gr_mem:
                return False
        except KeyError:
            return False

        HostService._run(
            ["gpasswd", "-d", username, group_name],
            failure_msg=f"Failed to remove user {username} from group {group_name}",
            missing_msg="gpasswd command not found",
        )
        return True

    @staticmethod
    def validate_sudoers_binaries() -> None:
        """Verify that all privileged binaries referenced in the sudoers drop-in exist on disk.

        In development mode (``pip install -e .``, ``uv run mvm``), service
        binaries are not embedded and are not expected on disk.  The
        ``LoopMountManager`` falls back to
        ``sys.executable services/loopmount/process.py`` at runtime, so
        there is nothing to validate.  Only in a compiled distribution do
        we require the extracted binaries to be present.
        """
        from mvmctl.constants import (
            PRIVILEGED_SERVICE_BINARIES,
            is_compiled_mode,
        )
        from mvmctl.utils.common import CacheUtils

        for binary, pkg in PRIVILEGED_BINARIES.items():
            if not Path(binary).exists():
                raise HostError(
                    f"Required binary not found: {binary} (install {pkg})"
                )

        # Service binaries only exist in compiled mode
        if not is_compiled_mode():
            return

        for name in PRIVILEGED_SERVICE_BINARIES:
            path = CacheUtils.get_bin_dir() / name
            if not path.exists():
                raise HostError(
                    f"Required service binary not found: {path}. "
                    f"Run 'mvm init' to extract service binaries."
                )

    @staticmethod
    def _generate_sudoers_content(group_name: str) -> str:
        """Generate the sudoers drop-in content granting the group passwordless access.

        Notes:
            Group name format validation is handled by the API layer
            (HostOperation.init) before this method is called.

        """
        from mvmctl.constants import PRIVILEGED_SERVICE_BINARIES, PROJECT_NAME

        # System binaries (static paths)
        binaries = list(PRIVILEGED_BINARIES.keys())

        # Service binaries (dynamic paths resolved at runtime)
        from mvmctl.utils.common import CacheUtils

        for name in PRIVILEGED_SERVICE_BINARIES:
            binaries.append(str(CacheUtils.get_bin_dir() / name))

        binaries_str = ", ".join(binaries)
        return (
            f"# Managed by {PROJECT_NAME} — do not edit manually.\n"
            f"# To remove: {PROJECT_NAME} host reset\n"
            f"%{group_name} ALL=(root) NOPASSWD: {binaries_str}\n"
        )

    @staticmethod
    def write_sudoers(path: Path, group_name: str) -> bool:
        """Write and validate the sudoers drop-in file for the given group."""
        HostService.validate_sudoers_binaries()
        content = HostService._generate_sudoers_content(group_name)
        with tempfile.NamedTemporaryFile(
            mode="w", suffix=".sudoers", delete=False
        ) as f:
            f.write(content)
            tmp_path = f.name
        try:
            result = run_cmd(
                ["visudo", "-c", "-f", tmp_path],
                check=False,
            )
            if result.returncode != 0:
                raise HostError(
                    f"Generated sudoers file failed visudo validation: {result.stderr}"
                )
        except ProcessError:
            raise HostError("visudo not found — cannot validate sudoers syntax")
        finally:
            try:
                os.unlink(tmp_path)
            except OSError:
                pass
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
            path.chmod(CONST_FILE_PERMS_SUDOERS)
        except OSError as e:
            raise HostError(f"Failed to write sudoers file {path}: {e}") from e
        return True

    @staticmethod
    def remove_sudoers(path: Path) -> bool:
        """Remove the sudoers drop-in file if it exists."""
        if not path.exists():
            return False
        try:
            path.unlink()
            return True
        except OSError as e:
            raise HostError(f"Failed to remove sudoers file {path}: {e}") from e

    @staticmethod
    def remove_group(group_name: str) -> bool:
        """Delete a system group if it exists."""
        if not HostService._group_exists(group_name):
            return False
        HostService._run(
            ["groupdel", group_name],
            failure_msg=f"Failed to remove group {group_name}",
            missing_msg="groupdel command not found",
        )
        return True

    @staticmethod
    def _get_ip_forward_status() -> str:
        """Get the current IP forwarding status."""
        result = HostService._run(
            ["sysctl", "-n", SYSCTL_KEY],
            failure_msg=f"Failed to read {SYSCTL_KEY}",
            missing_msg="sysctl command not found",
        )
        return result.stdout.strip()

    @staticmethod
    def enable_ip_forward() -> HostStateChangeItem | None:
        """Enable IP forwarding if not already enabled."""
        current = HostService._get_ip_forward_status()
        if current == "1":
            logger.debug("IP forwarding already enabled")
            return None
        HostService._run(
            ["sysctl", "-w", f"{SYSCTL_KEY}=1"],
            failure_msg="Failed to enable IP forwarding",
            missing_msg="sysctl command not found",
        )
        return HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting=SYSCTL_KEY,
            mechanism="sysctl",
            applied_value="1",
            reverted=False,
            change_order=0,
            created_at="",
            original_value=current,
        )

    @staticmethod
    def persist_sysctl() -> HostStateChangeItem | None:
        """Persist sysctl configuration to file."""
        content = f"{SYSCTL_KEY} = 1\n"
        if SYSCTL_CONF.exists() and SYSCTL_CONF.read_text() == content:
            logger.debug(
                "sysctl persist file already exists with correct content"
            )
            return None
        original: str | None = None
        if SYSCTL_CONF.exists():
            original = SYSCTL_CONF.read_text()
        try:
            SYSCTL_CONF.parent.mkdir(parents=True, exist_ok=True)
            SYSCTL_CONF.write_text(content)
        except OSError as e:
            raise HostError(f"Failed to write {SYSCTL_CONF}: {e}") from e
        return HostStateChangeItem(
            session_id="",
            init_timestamp="",
            setting="sysctl_persist_file",
            mechanism="file_create",
            applied_value=str(SYSCTL_CONF),
            reverted=False,
            change_order=0,
            created_at="",
            original_value=original,
        )

    @staticmethod
    def _is_module_loaded(module: str) -> bool:
        """Check if a kernel module is loaded."""
        try:
            result = run_cmd(
                ["lsmod"],
                check=False,
            )
            if result.returncode != 0:
                return False
            return any(
                line.split()[0] == module
                for line in result.stdout.splitlines()
                if line
            )
        except (OSError, ProcessError):
            return False

    @staticmethod
    def _load_module(
        module: str,
        *,
        repo: HostRepository | None = None,
        session_id: str = "",
        change_order: int = 0,
        init_timestamp: str = "",
        created_at: str = "",
    ) -> HostStateChangeItem:
        """Load a kernel module and optionally record the change."""
        HostService._run(
            ["modprobe", module],
            failure_msg=f"Failed to load kernel module {module}",
            missing_msg="modprobe command not found",
        )
        change = HostStateChangeItem(
            session_id=session_id,
            init_timestamp=init_timestamp,
            setting="kernel_module_load",
            mechanism="modprobe",
            applied_value=module,
            reverted=False,
            change_order=change_order,
            created_at=created_at,
            original_value=None,
        )
        if repo is not None:
            repo.add_change(change)
        return change

    @staticmethod
    def ensure_kvm_modules(
        repo: HostRepository | None = None,
        session_id: str = "",
        change_order_start: int = 0,
    ) -> tuple[list[HostStateChangeItem], int]:
        """Ensure KVM modules are loaded and return list of changes."""
        changes: list[HostStateChangeItem] = []
        now = datetime.now(UTC).isoformat() if session_id else ""
        next_order = change_order_start

        # Detect available vendor modules: check lsmod first (modules may be
        # loaded even if the module file is absent under custom kernels),
        # then fall back to modprobe --dry-run for modules not yet loaded.
        vendor_modules: list[str] = []
        for mod in ("kvm_intel", "kvm_amd"):
            if HostService._is_module_loaded(mod) or (
                run_cmd(
                    ["modprobe", "--dry-run", mod],
                    check=False,
                ).returncode
                == 0
            ):
                vendor_modules.append(mod)

        if not vendor_modules:
            raise HostError(
                "No KVM vendor modules available. Ensure virtualization is "
                "enabled in BIOS and KVM kernel modules are installed."
            )

        kvm_modules = ["kvm"]
        for module in kvm_modules:
            if HostService._is_module_loaded(module):
                logger.debug("Module %s already loaded", module)
                continue
            change = HostService._load_module(
                module,
                repo=repo,
                session_id=session_id,
                change_order=next_order,
                init_timestamp=now,
                created_at=now,
            )
            changes.append(change)
            next_order += 1

        vendor_loaded = any(
            HostService._is_module_loaded(m) for m in vendor_modules
        )
        if not vendor_loaded:
            for module in vendor_modules:
                try:
                    change = HostService._load_module(
                        module,
                        repo=repo,
                        session_id=session_id,
                        change_order=next_order,
                        init_timestamp=now,
                        created_at=now,
                    )
                    changes.append(change)
                    next_order += 1
                    break
                except HostError:
                    continue
        return changes, next_order

    def restore_state(self) -> list[HostStateChangeItem]:
        """Revert host changes recorded in the database."""
        self._repo.initialize_state()
        changes = self._repo.list_changes(include_reverted=False)
        if not changes:
            raise HostError("No saved host state to restore")
        reverted: list[HostStateChangeItem] = []
        reverted_at = datetime.now(UTC).isoformat()
        restorable_sysctl: frozenset[str] = frozenset({SYSCTL_KEY})
        restorable_files: frozenset[Path] = frozenset(
            {
                Path(SUDOERS_DROP_IN_PATH),
                Path(f"{DEFAULT_SYSCTL_CONF_DIR}/{PROJECT_NAME}.conf"),
            }
        )

        for change in reversed(changes):
            was_reverted = False
            if (
                change.mechanism == "sysctl"
                and change.original_value is not None
            ):
                if change.setting not in restorable_sysctl:
                    logger.warning(
                        "Skipping disallowed sysctl key '%s' from state",
                        change.setting,
                    )
                    continue
                HostService._run(
                    [
                        "sysctl",
                        "-w",
                        f"{change.setting}={change.original_value}",
                    ],
                    failure_msg=f"Failed to revert {change.setting}",
                    missing_msg="sysctl command not found",
                )
                reverted.append(
                    HostStateChangeItem(
                        session_id=change.session_id,
                        init_timestamp=change.init_timestamp,
                        setting=change.setting,
                        mechanism="sysctl",
                        applied_value=change.original_value,
                        reverted=False,
                        change_order=change.change_order,
                        created_at=change.created_at,
                        original_value=change.applied_value,
                    )
                )
                was_reverted = True

            elif change.mechanism == "file_create":
                target = Path(change.applied_value).resolve()
                if not any(
                    target == allowed.resolve() for allowed in restorable_files
                ):
                    logger.warning(
                        "Skipping disallowed file path '%s' from state", target
                    )
                    continue
                if target.exists():
                    try:
                        if change.original_value is not None:
                            if str(target).startswith(DEFAULT_SUDOERS_DIR):
                                result = run_cmd(
                                    ["visudo", "-c", "-f", "-"],
                                    input=change.original_value,
                                    check=False,
                                )
                                if result.returncode != 0:
                                    raise HostError(
                                        "Sudoers content from state failed visudo validation: "
                                        f"{result.stderr}"
                                    )
                            target.write_text(change.original_value)
                        else:
                            target.unlink()
                        reverted.append(
                            HostStateChangeItem(
                                session_id=change.session_id,
                                init_timestamp=change.init_timestamp,
                                setting=change.setting,
                                mechanism="file_remove",
                                applied_value=change.original_value
                                or "(removed)",
                                reverted=False,
                                change_order=change.change_order,
                                created_at=change.created_at,
                                original_value=change.applied_value,
                            )
                        )
                        was_reverted = True
                    except OSError as e:
                        raise HostError(
                            f"Failed to revert file {target}: {e}"
                        ) from e

            if was_reverted and change.id is not None:
                self._repo.mark_change_reverted(change.id, reverted_at)

        return reverted


__all__ = ["HostService"]
