"""Console request resolver — resolves VM + creates console relay manager.

Follows the Input → Request → Resolved pipeline pattern.
"""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.api.inputs._vm_input import VMInput, VMRequest
from mvmctl.core._shared import Database
from mvmctl.models.vm import VMInstanceItem
from mvmctl.services.console_relay import ConsoleRelayManager
from mvmctl.utils.common import CacheUtils


@dataclass
class ConsoleInput:
    """Input for console operations on a VM.

    Uses a single VM identifier (name, ID, MAC, or IP) which is resolved
    through the standard VMRequest pipeline.
    """

    identifier: str


@dataclass(frozen=True)
class ResolvedConsoleInput:
    """Immutable resolved console request — contains the VM and relay manager."""

    vm: VMInstanceItem
    relay: ConsoleRelayManager


class ConsoleRequest:
    """Request to resolve a VM and prepare its console relay manager.

    Follows the Input → Request → Resolved pipeline pattern.
    """

    def __init__(
        self, *, inputs: ConsoleInput, db: Database | None = None
    ) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._result: ResolvedConsoleInput | None = None

    @property
    def result(self) -> ResolvedConsoleInput | None:
        return self._result

    def resolve(self) -> ResolvedConsoleInput:
        """Resolve the VM and create a console relay manager.

        Returns:
            ResolvedConsoleInput with resolved VM and relay manager.

        Raises:
            VMNotFoundError: If VM cannot be found.
        """
        resolved = VMRequest(
            inputs=VMInput(identifiers=[self._inputs.identifier]),
            db=self._db,
        ).resolve()

        vm = resolved.vms[0]
        relay = ConsoleRelayManager(
            id=vm.id,
            path=CacheUtils.get_vm_dir(vm.id),
            name=vm.name,
        )

        self._result = ResolvedConsoleInput(vm=vm, relay=relay)
        return self._result
