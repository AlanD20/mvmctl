"""CP input resolution — Input → Request → ResolvedCPInput."""

from __future__ import annotations

import logging
import os
from dataclasses import dataclass

from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.key._service import KeyService
from mvmctl.core.ssh._cp import CPService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.exceptions import CPError, SSHError
from mvmctl.models import VMInstanceItem

logger = logging.getLogger(__name__)


@dataclass
class CPInput:
    """Raw copy identifiers from CLI."""

    sources: list[str]
    dst: str
    user: str | None = None
    key: str | None = None
    force: bool = False


@dataclass
class ResolvedCPInfo:
    """Resolved connection info for one side of a copy operation."""

    identifier: str
    ip: str
    user: str
    key_path: str | None
    remote_path: str
    is_directory: bool | None = None
    total_bytes: int | None = None


@dataclass(frozen=True)
class ResolvedCPInput:
    """Fully resolved copy parameters."""

    direction: str  # "host_to_vm" | "vm_to_host" | "vm_to_vm"
    local_paths: list[str] | None = None
    src_info: ResolvedCPInfo | None = None
    dst_info: ResolvedCPInfo | None = None
    force: bool = False


class CPRequest:
    """Resolve CPInput against the database and filesystem."""

    def __init__(self, inputs: CPInput, db: Database) -> None:
        self._inputs = inputs
        self._db = db

    def resolve(self) -> ResolvedCPInput:
        """Resolve all inputs to explicit values."""
        sources = self._inputs.sources
        dst_vm, dst_path = CPService._parse_vm_path(self._inputs.dst)

        src_info: ResolvedCPInfo | None = None
        dst_info: ResolvedCPInfo | None = None
        local_paths: list[str] | None = None
        direction: str

        if len(sources) > 1:
            # Multi-source only works for host → VM
            if dst_vm is None:
                raise CPError(
                    "Multiple sources require a VM destination "
                    "(use vm_name:/path format)",
                    code="cp.multi_source_no_vm_destination",
                )
            direction = "host_to_vm"
            local_paths = sources
            if dst_vm is None:
                raise CPError(
                    "Internal error: destination VM not resolved",
                    code="cp.resolve_failed",
                )
            dst_info = self._resolve_vm_side(dst_vm, dst_path, is_source=False)
        else:
            # Single source — determine direction by parsing source
            src_path = sources[0]
            src_vm, src_remote_path = CPService._parse_vm_path(src_path)

            if src_vm and dst_vm:
                direction = "vm_to_vm"
            elif src_vm:
                direction = "vm_to_host"
            elif dst_vm:
                direction = "host_to_vm"
            else:
                raise CPError(
                    "At least one path must reference a VM "
                    "(use vm_name:/path format)",
                    code="cp.no_vm_specified",
                )

            if direction == "host_to_vm":
                # src is local, dst is VM
                local_paths = [src_path]
                if dst_vm is None:
                    raise CPError(
                        "Internal error: destination VM not resolved",
                        code="cp.resolve_failed",
                    )
                dst_info = self._resolve_vm_side(
                    dst_vm, dst_path, is_source=False
                )
            elif direction == "vm_to_host":
                # src is VM, dst is local
                if src_vm is None:
                    raise CPError(
                        "Internal error: source VM not resolved",
                        code="cp.resolve_failed",
                    )
                src_info = self._resolve_vm_side(
                    src_vm, src_remote_path, is_source=True
                )
                local_paths = [dst_path]
            elif direction == "vm_to_vm":
                # both are VMs
                if src_vm is None or dst_vm is None:
                    raise CPError(
                        "Internal error: source or destination VM not resolved",
                        code="cp.resolve_failed",
                    )
                src_info = self._resolve_vm_side(
                    src_vm, src_remote_path, is_source=True
                )
                dst_info = self._resolve_vm_side(
                    dst_vm, dst_path, is_source=False
                )

        return ResolvedCPInput(
            direction=direction,
            local_paths=local_paths,
            src_info=src_info,
            dst_info=dst_info,
            force=self._inputs.force,
        )

    def _resolve_vm_side(
        self, vm_ident: str, remote_path: str, is_source: bool
    ) -> ResolvedCPInfo:
        """Resolve a VM-side path to connection info."""
        vm = self._resolve_vm(vm_ident)
        user = self._resolve_user(vm)
        key_path = self._resolve_key(vm)

        if not vm.ipv4:
            raise CPError(
                f"VM '{vm_ident}' has no IP address assigned",
                code="cp.vm_no_ip",
            )

        return ResolvedCPInfo(
            identifier=vm_ident,
            ip=vm.ipv4,
            user=user,
            key_path=key_path,
            remote_path=remote_path,
        )

    def _resolve_vm(self, identifier: str) -> VMInstanceItem:
        """Resolve a VM by name, IP, MAC, or ID prefix."""
        repo = VMRepository(self._db)
        resolver = VMResolver(repo)
        try:
            return resolver.resolve(identifier)
        except Exception as e:
            raise CPError(
                f"Could not resolve VM '{identifier}': {e}",
                code="cp.vm_not_found",
            ) from e

    def _resolve_user(self, vm: VMInstanceItem) -> str:
        """Resolve SSH user."""
        if self._inputs.user is not None:
            return self._inputs.user
        if vm.ssh_user:
            return vm.ssh_user
        return str(SettingsService.resolve(self._db, "defaults.vm", "ssh_user"))

    def _resolve_key(self, vm: VMInstanceItem) -> str | None:
        """
        Resolve SSH private key path.

        Resolution order:
        1. If ``--key`` provided: try as registered key name, then as filesystem path.
        2. Check VM's stored ``ssh_keys``.
        3. Fall back to default keys.
        """
        key_repo = KeyRepository(self._db)
        key_resolver = KeyResolver(key_repo)

        if self._inputs.key is not None:
            key_str = self._inputs.key

            # Try as registered key name
            try:
                key_item = key_resolver.resolve(key_str)
                if key_item.private_key_path and os.path.exists(
                    key_item.private_key_path
                ):
                    return str(key_item.private_key_path)
            except Exception:
                pass

            # Try as filesystem path
            if os.path.exists(key_str) and os.path.isfile(key_str):
                key_service = KeyService(key_repo)
                with open(key_str) as f:
                    content = f.read()
                if key_service._is_private_key(content):
                    return key_str

            raise SSHError(
                f"Key '{key_str}' not found or is not a valid private key"
            )

        # Check VM's stored ssh_keys
        if vm.ssh_keys:
            for key_id in vm.ssh_keys:
                try:
                    key_item = key_resolver.by_id(key_id)
                    if key_item.private_key_path and os.path.exists(
                        key_item.private_key_path
                    ):
                        return str(key_item.private_key_path)
                except Exception:
                    continue

        # Fall back to default keys
        defaults = key_resolver.get_defaults()
        for key_item in defaults:
            if key_item.private_key_path and os.path.exists(
                key_item.private_key_path
            ):
                return str(key_item.private_key_path)

        return None
