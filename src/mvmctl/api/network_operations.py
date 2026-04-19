"""Network operations - cross-domain orchestration for network management."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

from mvmctl.core._internal._db import Database
from mvmctl.core.network._controller import NetworkController
from mvmctl.core.network._lease_service import LeaseService
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import NetworkItem
from mvmctl.utils.audit import log_audit

if TYPE_CHECKING:
    from mvmctl.api.inputs._network_create_input import NetworkCreateInput
    from mvmctl.api.inputs._network_input import NetworkInput

logger = logging.getLogger(__name__)

__all__ = ["NetworkOperation"]


class NetworkOperation:
    """Orchestration layer for network operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def create(inputs: NetworkCreateInput) -> NetworkItem:
        """Create a new network.

        Args:
            inputs: NetworkCreateInput with name, subnet, etc.

        Returns:
            The created NetworkItem.
        """
        from mvmctl.api.inputs._network_create_input import NetworkCreateRequest

        db = Database()
        repo = NetworkRepository(db)

        # Resolve and validate inputs
        request = NetworkCreateRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        # Build NetworkItem from resolved inputs
        network_item = NetworkItem(
            id=resolved.network_id,
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
            created_at=resolved.created_at,
            updated_at=resolved.created_at,
        )

        # Persist to DB
        repo.upsert(network_item)

        # Setup infrastructure
        service = NetworkService(repo)
        try:
            service.ensure_bridge(resolved.bridge, resolved.subnet)

            if resolved.nat_enabled:
                service.ensure_nat(
                    resolved.bridge,
                    resolved.nat_gateways,
                    subnet=resolved.subnet,
                )
        except NetworkError:
            # If infrastructure setup fails, clean up DB record
            repo.delete(resolved.network_id)
            raise

        # Update bridge_active status
        bridge_active = service.bridge_exists(resolved.bridge)
        repo.update_bridge_active(resolved.network_id, bridge_active)

        # Re-fetch the item to get updated state
        updated_item = repo.get_by_name(resolved.name)
        if updated_item is None:
            raise NetworkError(
                f"Failed to fetch created network '{resolved.name}'"
            )

        log_audit("network.create", f"name={resolved.name}")
        return updated_item

    @staticmethod
    def remove(inputs: NetworkInput) -> None:
        """Remove a network.

        Args:
            inputs: NetworkInput with name/id identifiers.
        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()
        repo = NetworkRepository(db)

        # Resolve identifiers
        request = NetworkRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        for network in resolved.networks:
            # Check for active leases
            lease_service = LeaseService(network.id, LeaseRepository(db))
            leases = lease_service.get_leases()
            active_vm_leases = [
                lease for lease in leases if lease.vm_id is not None
            ]
            if active_vm_leases:
                raise NetworkError(
                    f"Network '{network.name}' has {len(active_vm_leases)} active VM leases. "
                    f"Remove the VMs first."
                )

            # Teardown infrastructure
            service = NetworkService(repo)
            if network.nat_enabled:
                try:
                    service.remove_nat(
                        network.bridge,
                        network.nat_gateways_list,
                        subnet=network.subnet,
                    )
                except NetworkError as e:
                    logger.debug("NAT teardown for %s: %s", network.bridge, e)

            try:
                service.remove_bridge(network.bridge)
            except NetworkError as e:
                logger.debug("Bridge teardown for %s: %s", network.bridge, e)

            # Delete from DB
            repo.delete(network.id)

            log_audit("network.remove", f"name={network.name}")

    @staticmethod
    def list_all() -> list[NetworkItem]:
        """List all networks.

        Returns:
            List of all NetworkItem records.
        """
        db = Database()
        repo = NetworkRepository(db)
        return repo.list_all()

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
    def inspect(inputs: NetworkInput) -> NetworkItem:
        """Inspect a network with enriched data (leases, bridge state).

        Args:
            inputs: NetworkInput with name/id identifiers.

        Returns:
            NetworkItem with enriched lease data.
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
        service = NetworkService(repo)
        bridge_active = service.bridge_exists(network.bridge)
        if bridge_active != network.bridge_active:
            repo.update_bridge_active(network.id, bridge_active)

        # Re-fetch with updated state
        updated = repo.get_by_name(network.name)
        if updated is None:
            raise NetworkError(
                f"Network '{network.name}' not found after update"
            )

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

        log_audit("network.set_default", f"name={resolved.networks[0].name}")

    @staticmethod
    def ensure_default() -> NetworkItem:
        """Ensure the default network exists and is materialized.

        Returns:
            The default NetworkItem.
        """
        from mvmctl.constants import (
            DEFAULT_NETWORK_NAME,
            DEFAULT_NETWORK_SUBNET,
        )

        db = Database()
        repo = NetworkRepository(db)

        # Check if default network exists
        default_network = repo.get_default()
        if default_network is not None:
            # Ensure bridge is materialized
            service = NetworkService(repo)
            try:
                service.ensure_bridge(
                    default_network.bridge, default_network.subnet
                )
                if default_network.nat_enabled:
                    service.ensure_nat(
                        default_network.bridge,
                        default_network.nat_gateways_list,
                        subnet=default_network.subnet,
                    )
            except NetworkError:
                logger.debug("Failed to materialize default network bridge/NAT")

            # Update bridge_active
            bridge_active = service.bridge_exists(default_network.bridge)
            if bridge_active != default_network.bridge_active:
                repo.update_bridge_active(default_network.id, bridge_active)

            return repo.get_default() or default_network

        # Create default network
        from mvmctl.api.inputs._network_create_input import NetworkCreateInput

        service = NetworkService(repo)
        outbound_iface = service.detect_outbound_interface()
        nat_gateways = [outbound_iface] if outbound_iface else []

        create_input = NetworkCreateInput(
            name=DEFAULT_NETWORK_NAME,
            subnet=DEFAULT_NETWORK_SUBNET,
            nat_enabled=True,
            nat_gateways=nat_gateways,
        )

        return NetworkOperation.create(create_input)

    @staticmethod
    def reconcile() -> list[NetworkItem]:
        """Reconcile all networks — compare DB state vs actual bridge state.

        Returns:
            List of all NetworkItem records with updated bridge_active status.
        """
        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)

        networks = repo.list_all()
        for network in networks:
            bridge_active = service.bridge_exists(network.bridge)
            if bridge_active != network.bridge_active:
                repo.update_bridge_active(network.id, bridge_active)

        return repo.list_all()

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
                service.ensure_bridge(network.bridge, network.subnet)
                if network.nat_enabled:
                    service.ensure_nat(
                        network.bridge,
                        network.nat_gateways_list,
                        subnet=network.subnet,
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
