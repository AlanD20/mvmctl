"""Network operations - cross-domain orchestration for network management."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import TYPE_CHECKING, Any

from mvmctl.constants import DEFAULT_NETWORK_NAME, DEFAULT_NETWORK_SUBNET
from mvmctl.core._internal._db import Database
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.network._controller import NetworkController
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import NetworkItem
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.full_hash import HashGenerator
from mvmctl.utils.network import NetworkUtils

if TYPE_CHECKING:
    from mvmctl.api.inputs._network_create_input import NetworkCreateInput
    from mvmctl.api.inputs._network_input import NetworkInput

logger = logging.getLogger(__name__)

__all__ = ["NetworkOperation"]


@dataclass
class NetworkCreateResult:
    """Result of network create operation."""

    result: NetworkItem


class NetworkOperation:
    """Orchestration layer for network operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def create(inputs: NetworkCreateInput) -> NetworkCreateResult:
        """Create a new network.

        Args:
            inputs: NetworkCreateInput with name, subnet, etc.

        Returns:
            NetworkCreateResult with the created NetworkItem.
        """
        from mvmctl.api.inputs._network_create_input import NetworkCreateRequest

        db = Database()
        repo = NetworkRepository(db)

        # Resolve and validate inputs
        request = NetworkCreateRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        # Compute network ID and timestamp right before creation
        created_at = datetime.now(tz=timezone.utc).isoformat()
        network_id = HashGenerator.network(
            resolved.name, resolved.subnet, created_at
        )

        # Build NetworkItem from resolved inputs
        network_item = NetworkItem(
            id=network_id,
            name=resolved.name,
            subnet=resolved.subnet,
            bridge=resolved.bridge,
            ipv4_gateway=resolved.ipv4_gateway,
            bridge_active=False,  # Will be set to True after bridge setup
            nat_enabled=resolved.nat_enabled,
            nat_gateways=",".join(resolved.nat_gateways)
            if resolved.nat_gateways
            else None,
            is_default=False,
            is_present=True,
            created_at=created_at,
            updated_at=created_at,
        )

        # Persist to DB so that we have the record to ensure creation
        repo.upsert(network_item)

        # Setup infrastructure
        service = NetworkService(repo)
        try:
            bridge_addr = NetworkUtils.compute_bridge_address(
                resolved.ipv4_gateway, resolved.subnet
            )
            service.ensure_bridge(resolved.bridge, bridge_addr)

            if resolved.nat_enabled:
                service.ensure_nat(
                    resolved.bridge,
                    resolved.nat_gateways,
                    subnet=resolved.subnet,
                    network_id=network_id,
                )
        except NetworkError:
            # If infrastructure setup fails, clean up DB record
            repo.delete(network_id)
            raise

        # Update bridge_active status
        bridge_active = NetworkUtils.bridge_exists(resolved.bridge)
        repo.update_bridge_active(network_id, bridge_active)

        # Re-fetch the item to get updated state
        updated_item = repo.get_by_name(resolved.name)
        if updated_item is None:
            raise NetworkError(
                f"Failed to fetch created network '{resolved.name}'"
            )

        AuditLog.log("network.create", changes={"name": resolved.name})
        return NetworkCreateResult(result=updated_item)

    @staticmethod
    def remove(inputs: NetworkInput, force: bool = False) -> None:
        """Remove a network.

        Args:
            inputs: NetworkInput with name/id identifiers.
            force: If True, remove even if referenced by VMs.
        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)

        # Resolve identifiers
        request = NetworkRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        service.remove_many(resolved.networks, force=force)

        for network in resolved.networks:
            AuditLog.log(
                "network.remove",
                changes={"id": network.id, "name": network.name},
            )

    @staticmethod
    def list_all() -> list[NetworkItem]:
        """List all networks.

        Returns:
            List of all NetworkItem records with lease enrichment.
        """
        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)
        networks = service.list_all(verify=True)

        if not networks:
            return []

        # Enrich with leases
        from mvmctl.core.network._resolver import NetworkResolver

        resolver = NetworkResolver(repo, include=["leases"])
        return resolver._enrich(networks)

    @staticmethod
    def get(inputs: NetworkInput) -> NetworkItem:
        """Get a single network.

        Args:
            inputs: NetworkInput with name/id identifiers.

        Returns:
            The resolved NetworkItem.
        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()

        request = NetworkRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.networks) != 1:
            raise NetworkError(
                f"Expected exactly one network, got {len(resolved.networks)}"
            )

        return resolved.networks[0]

    @staticmethod
    def _network_to_dict(network: NetworkItem) -> dict[str, Any]:
        """Convert NetworkItem to dictionary for JSON output.

        Includes every field from the model.
        """
        return {
            "id": network.id,
            "name": network.name,
            "subnet": network.subnet,
            "bridge": network.bridge,
            "ipv4_gateway": network.ipv4_gateway,
            "bridge_active": network.bridge_active,
            "nat_enabled": network.nat_enabled,
            "is_default": network.is_default,
            "is_present": network.is_present,
            "created_at": network.created_at,
            "updated_at": network.updated_at,
            "full_name": network.full_name,
            "nat_gateways": network.nat_gateways_list or [],
            "leases": [
                {
                    "id": lease.id,
                    "network_id": lease.network_id,
                    "vm_id": lease.vm_id,
                    "ipv4": lease.ipv4,
                    "leased_at": lease.leased_at,
                    "expires_at": lease.expires_at,
                }
                for lease in (network.leases or [])
            ],
            "iptables_rules": [
                {
                    "id": rule.id,
                    "table_name": rule.table_name.value,
                    "chain_name": rule.chain_name,
                    "rule_type": rule.rule_type.value,
                    "protocol": rule.protocol.value,
                    "source": rule.source,
                    "destination": rule.destination,
                    "in_interface": rule.in_interface,
                    "out_interface": rule.out_interface,
                    "target": rule.target.value,
                    "sport": rule.sport,
                    "dport": rule.dport,
                    "network_id": rule.network_id,
                    "is_active": rule.is_active,
                    "network_name": rule.network_name,
                    "comment_tag": rule.comment_tag,
                    "command_string": rule.command_string,
                    "created_at": rule.created_at,
                    "last_verified_at": rule.last_verified_at,
                }
                for rule in (network.iptables_rules or [])
            ],
        }

    @staticmethod
    def inspect(
        inputs: NetworkInput, is_json: bool = False
    ) -> NetworkItem | dict[str, Any]:
        """Inspect a network with enriched data (leases, bridge state).

        Args:
            inputs: NetworkInput with name/id identifiers.
            is_json: If True, return a dict suitable for JSON serialization.

        Returns:
            NetworkItem or dict representation depending on is_json.
        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()
        repo = NetworkRepository(db)

        # Resolve with lease enrichment
        request = NetworkRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.networks) != 1:
            raise NetworkError(
                f"Expected exactly one network, got {len(resolved.networks)}"
            )

        network = resolved.networks[0]

        # Update bridge_active status
        bridge_active = NetworkUtils.bridge_exists(network.bridge)
        if bridge_active != network.bridge_active:
            repo.update_bridge_active(network.id, bridge_active)

        # Re-fetch with updated state
        updated = repo.get_by_name(network.name)
        if updated is None:
            raise NetworkError(
                f"Network '{network.name}' not found after update"
            )

        if is_json:
            return NetworkOperation._network_to_dict(updated)
        return updated

    @staticmethod
    def set_default(inputs: NetworkInput) -> None:
        """Set a network as the default.

        Args:
            inputs: NetworkInput with name/id identifiers.
        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()
        repo = NetworkRepository(db)

        request = NetworkRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        if len(resolved.networks) != 1:
            raise NetworkError(
                f"Expected exactly one network, got {len(resolved.networks)}"
            )

        controller = NetworkController(resolved.networks[0], repo)
        controller.set_default()

        AuditLog.log(
            "network.set_default", changes={"name": resolved.networks[0].name}
        )

    @staticmethod
    def create_default_network() -> NetworkItem:
        """Create the default network if it doesn't exist, ensure one network is default, and materialize its bridge/NAT.

        Idempotent — safe to call multiple times.

        Returns:
            The default NetworkItem.
        """
        from mvmctl.api.inputs._network_create_input import NetworkCreateInput

        db = Database()
        repo = NetworkRepository(db)

        # 1. Ensure internal default network exists
        internal_network = repo.get_by_name(DEFAULT_NETWORK_NAME)
        if internal_network is None:
            outbound_iface = NetworkUtils.detect_outbound_interface()
            nat_gateways = [outbound_iface] if outbound_iface else []

            create_input = NetworkCreateInput(
                name=DEFAULT_NETWORK_NAME,
                subnet=DEFAULT_NETWORK_SUBNET,
                nat_enabled=len(nat_gateways) > 0,
                nat_gateways=nat_gateways,
            )
            result = NetworkOperation.create(create_input).result
            internal_network = result
            HostRepository(db).update_component("default_network_created", True)

        # 2. Ensure there is a default network
        default_network = repo.get_default()
        if default_network is None:
            controller = NetworkController(internal_network, repo)
            controller.set_default()
            default_network = repo.get_default() or internal_network

        # 3. Materialize bridge and NAT
        service = NetworkService(repo)
        try:
            bridge_addr = NetworkUtils.compute_bridge_address(
                default_network.ipv4_gateway, default_network.subnet
            )
            service.ensure_bridge(default_network.bridge, bridge_addr)
            if default_network.nat_enabled:
                service.ensure_nat(
                    default_network.bridge,
                    default_network.nat_gateways_list,
                    subnet=default_network.subnet,
                    network_id=default_network.id,
                )
        except NetworkError:
            logger.debug("Failed to materialize default network bridge/NAT")

        # Update bridge_active
        bridge_active = NetworkUtils.bridge_exists(default_network.bridge)
        if bridge_active != default_network.bridge_active:
            repo.update_bridge_active(default_network.id, bridge_active)

        return repo.get_default() or default_network

    @staticmethod
    def reconcile() -> list[NetworkItem]:
        """Reconcile all networks — compare DB state vs actual bridge state.

        Returns:
            List of all NetworkItem records with updated bridge_active status.
        """
        db = Database()
        repo = NetworkRepository(db)

        networks = repo.list_all()
        for network in networks:
            bridge_active = NetworkUtils.bridge_exists(network.bridge)
            if bridge_active != network.bridge_active:
                repo.update_bridge_active(network.id, bridge_active)

        return networks

    @staticmethod
    def restore() -> list[str]:
        """Restore all networks from DB after reboot.

        Returns:
            List of status messages for each restored network.
        """
        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)

        networks = repo.list_all()
        restored = []

        for network in networks:
            try:
                bridge_addr = NetworkUtils.compute_bridge_address(
                    network.ipv4_gateway, network.subnet
                )
                service.ensure_bridge(network.bridge, bridge_addr)
                if network.nat_enabled:
                    service.ensure_nat(
                        network.bridge,
                        network.nat_gateways_list,
                        subnet=network.subnet,
                        network_id=network.id,
                    )
                repo.update_bridge_active(network.id, True)
                restored.append(f"Restored network '{network.name}'")
            except NetworkError as e:
                logger.warning(
                    "Failed to restore network '%s': %s", network.name, e
                )
                restored.append(
                    f"Failed to restore network '{network.name}': {e}"
                )

        return restored
