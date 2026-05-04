"""SSH input resolution — Input → Request → ResolvedSSHInput."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from pathlib import Path

from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.core.key._repository import KeyRepository
from mvmctl.core.key._resolver import KeyResolver
from mvmctl.core.key._service import KeyService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.exceptions import SSHError
from mvmctl.models import VMInstanceItem
from mvmctl.utils._validators import NetworkValidator

logger = logging.getLogger(__name__)


@dataclass
class SSHInput:
    """Raw SSH identifiers from CLI."""

    vm_id: str | None = None
    user: str | None = None
    key: Path | None = None
    cmd: str | None = None
    ip: str | None = None
    name: str | None = None
    mac: str | None = None


@dataclass(frozen=True)
class ResolvedSSHInput:
    """Fully resolved SSH connection parameters."""

    target_ip: str
    user: str
    key: Path | None
    cmd: str | None


class SSHRequest:
    """Resolve SSHInput against the database."""

    def __init__(self, inputs: SSHInput, db: Database) -> None:
        self._inputs = inputs
        self._db = db
        self._vm: VMInstanceItem | None = None

    def resolve(self) -> ResolvedSSHInput:
        """Resolve all inputs to explicit values."""
        target_ip = self._resolve_target()
        user = self._resolve_user()
        key = self._resolve_key()
        return ResolvedSSHInput(
            target_ip=target_ip,
            user=user,
            key=key,
            cmd=self._inputs.cmd,
        )

    def _resolve_target(self) -> str:
        """
        Resolve target to an IP address.

        Resolution order:
        1. If --ip provided, use directly
        2. If --name, --mac, or vm_id provided:
           - If it's an IP address, use directly
           - Otherwise resolve as VM entity and return its IPv4
        3. Error if none provided
        """
        if self._inputs.ip is not None:
            return self._inputs.ip

        target = (
            self._inputs.name
            if self._inputs.name is not None
            else (
                self._inputs.mac
                if self._inputs.mac is not None
                else self._inputs.vm_id
            )
        )

        if target is None:
            raise SSHError(
                "Provide either a VM identifier, --name, --mac, or --ip"
            )

        if NetworkValidator.is_ip_address(target):
            return target

        repo = VMRepository(self._db)
        resolver = VMResolver(repo)
        self._vm = resolver.resolve(target)
        if not self._vm.ipv4:
            raise SSHError(f"VM '{target}' has no IP address")
        return self._vm.ipv4

    def _resolve_user(self) -> str:
        if self._inputs.user is not None:
            return self._inputs.user
        # Check VM's stored ssh_user
        if self._vm and self._vm.ssh_user:
            return self._vm.ssh_user
        return str(SettingsService.resolve(self._db, "defaults.vm", "ssh_user"))

    def _resolve_key(self) -> Path | None:
        """
        Resolve SSH private key path via the key domain.

        Resolution order:
        1. If --key provided:
           - Try as key name via KeyResolver
           - Try as filesystem path via KeyService validation
        2. If not provided:
           - Check VM's stored ssh_keys (most specific — exact keys injected)
           - Fall back to default keys
        """
        key_repo = KeyRepository(self._db)
        key_resolver = KeyResolver(key_repo)

        if self._inputs.key is not None:
            key_str = str(self._inputs.key)

            # 1a. Try as registered key name via KeyResolver
            try:
                key_item = key_resolver.resolve(key_str)
                if key_item.private_key_path:
                    path = Path(key_item.private_key_path)
                    if path.exists():
                        return path
            except Exception:
                pass

            # 1b. Try as direct filesystem path — validate via KeyService
            path = Path(key_str)
            if path.exists() and path.is_file():
                key_service = KeyService(key_repo)
                content = path.read_text()
                if key_service._is_private_key(content):
                    return path

            raise SSHError(
                f"Key '{key_str}' not found or is not a valid private key"
            )

        # 2. No key provided — check VM's stored ssh_keys (most specific)
        if self._vm and self._vm.ssh_keys:
            for key_id in self._vm.ssh_keys:
                try:
                    key_item = key_resolver.by_id(key_id)
                    if key_item.private_key_path:
                        path = Path(key_item.private_key_path)
                        if path.exists():
                            return path
                except Exception:
                    continue

        # 3. Fall back to default keys
        defaults = key_resolver.get_defaults()
        for key_item in defaults:
            if key_item.private_key_path:
                path = Path(key_item.private_key_path)
                if path.exists():
                    return path

        return None
