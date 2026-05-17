"""Network operations - cross-domain orchestration for network management."""

from __future__ import annotations

import logging
from datetime import UTC, datetime
from typing import TYPE_CHECKING, Any

from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.core.host._helper import HostPrivilegeHelper
from mvmctl.core.host._repository import HostRepository
from mvmctl.core.network._controller import NetworkController
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.core.network._service import NetworkService
from mvmctl.exceptions import NetworkError, NetworkNotFoundError
from mvmctl.models import NetworkItem
from mvmctl.models.result import NeedsInteraction, OperationResult
from mvmctl.utils.auditlog import AuditLog
from mvmctl.utils.crypto import HashGenerator
from mvmctl.utils.network import NetworkUtils

if TYPE_CHECKING:
    from mvmctl.api.inputs._network_create_input import NetworkCreateInput
    from mvmctl.api.inputs._network_input import NetworkInput

logger = logging.getLogger(__name__)

__all__ = ["NetworkOperation"]


class NetworkOperation:
    """
    Orchestration layer for network operations.

    All methods are @staticmethod — they take Input classes as arguments,
    create Request/Resolved internally, and orchestrate across core modules.
    """

    @staticmethod
    def prune(
        dry_run: bool = False,
        include_all: bool = False,
    ) -> OperationResult[list[str]]:
        """Prune unused networks.

        Args:
            dry_run: If True, only report what would be removed.
            include_all: If True, remove ALL networks including default and referenced.

        Returns:
            OperationResult with item list of network names that were removed.
        """
        from mvmctl.core.network._repository import LeaseRepository
        from mvmctl.core.vm._repository import VMRepository

        HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune networks")
        db = Database()
        repo = NetworkRepository(db)
        all_networks = repo.list_all()

        # Get referenced network IDs from VMs
        vm_repo = VMRepository(db)
        vms = vm_repo.list_all()
        referenced_network_ids: set[str] = set()
        for vm in vms:
            if vm.network_id:
                referenced_network_ids.add(vm.network_id)

        lease_repo = LeaseRepository(db)
        default_net_name = str(
            SettingsService.resolve(db, "defaults.network", "name")
        )
        removed: list[str] = []

        for network in all_networks:
            if not include_all:
                if network.name == default_net_name:
                    continue
                if network.id in referenced_network_ids:
                    continue
                leases = lease_repo.list_all(network.id)
                if leases:
                    continue

            # Soft-deleted networks (is_present=0) have no infrastructure —
            # skip API remove and delete the DB record directly
            if not network.is_present:
                if not dry_run:
                    repo.delete(network.id)
                removed.append(network.name)
                continue

            if not dry_run:
                try:
                    from mvmctl.api.inputs._network_input import NetworkInput

                    remove_result = NetworkOperation.remove(
                        NetworkInput(name=[network.name]),
                        force=include_all,
                    )
                    if remove_result.is_error:
                        logger.warning(
                            "Failed to remove network %s: %s",
                            network.name,
                            remove_result.message,
                        )
                    else:
                        removed.append(network.name)
                except Exception as e:
                    logger.warning(
                        "Failed to remove network %s: %s", network.name, e
                    )
            else:
                removed.append(network.name)

        return OperationResult(
            status="success",
            code="cache.pruned",
            message=f"Pruned {len(removed)} network(s)",
            item=removed,
        )

    @staticmethod
    def create(
        inputs: NetworkCreateInput,
    ) -> OperationResult[NetworkItem] | NeedsInteraction:
        """
        Create a new network.

        Args:
            inputs: NetworkCreateInput with name, subnet, etc.

        Returns:
            OperationResult wrapping the created NetworkItem.

        """
        from mvmctl.api.inputs._network_create_input import NetworkCreateRequest

        db = Database()
        repo = NetworkRepository(db)

        # Resolve and validate inputs
        request = NetworkCreateRequest(inputs=inputs, db=db)
        resolved = request.resolve()

        # Compute network ID and timestamp right before creation
        created_at = datetime.now(tz=UTC).isoformat()
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
        except NetworkError as e:
            # If infrastructure setup fails, clean up DB record
            logger.error(
                "Network infrastructure setup failed for '%s': %s",
                resolved.name,
                e,
            )
            repo.delete(network_id)
            return OperationResult(
                status="error",
                code="network.create_failed",
                message=f"Failed to create network '{resolved.name}': {e}",
            )

        # Update bridge_active status
        bridge_active = NetworkUtils.bridge_exists(resolved.bridge)
        repo.update_bridge_active(network_id, bridge_active)

        # Re-fetch the item to get updated state
        updated_item = repo.get_by_name(resolved.name)
        if updated_item is None:
            return OperationResult(
                status="error",
                code="network.create_failed",
                message=f"Failed to fetch created network '{resolved.name}'",
            )

        AuditLog.log("network.create", changes={"name": resolved.name})

        if inputs.set_default:
            try:
                repo.set_default(updated_item.id)
            except Exception:
                logger.warning(
                    "Failed to set network '%s' as default: %s",
                    resolved.name,
                    "unexpected error",
                    exc_info=True,
                )

        return OperationResult(
            status="success",
            code="network.created",
            item=updated_item,
            message=f"Network '{resolved.name}' created",
        )

    @staticmethod
    def remove(
        inputs: NetworkInput, force: bool = False
    ) -> OperationResult[NetworkItem]:
        """
        Remove a network.

        Args:
            inputs: NetworkInput with name/id identifiers.
            force: If True, remove even if referenced by VMs.

        Returns:
            OperationResult indicating removal outcome.

        """
        from mvmctl.api.inputs._network_input import NetworkRequest
        from mvmctl.core.network._resolver import NetworkResolver

        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)

        # Resolve identifiers
        request = NetworkRequest(inputs=inputs, db=db)
        try:
            resolved = request.resolve()
        except (NetworkError, NetworkNotFoundError) as e:
            return OperationResult(
                status="error",
                code="network.remove_failed",
                message=str(e),
                exception=e,
            )

        # Batch-enrich with VM references for VM reference check
        enriched = NetworkResolver(repo, include=["vm"]).enrich(
            resolved.networks
        )

        try:
            for network in enriched:
                service.remove(network, force=force)
        except NetworkError as e:
            error_msg = str(e)
            code = (
                "network.in_use"
                if "in use" in error_msg.lower()
                else "network.remove_failed"
            )
            return OperationResult(
                status="error",
                code=code,
                message=error_msg,
                exception=e,
            )

        names = ", ".join(n.name for n in resolved.networks)
        for network in resolved.networks:
            AuditLog.log(
                "network.remove",
                changes={"id": network.id, "name": network.name},
            )

        return OperationResult(
            status="success",
            code="network.removed",
            message=f"Network(s) '{names}' removed",
        )

    @staticmethod
    def list_all() -> list[NetworkItem]:
        """
        List all networks.

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
        return resolver.enrich(networks)

    @staticmethod
    def get(inputs: NetworkInput) -> NetworkItem:
        """
        Get a single network.

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
        """
        Convert NetworkItem to dictionary for JSON output.

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
            "nat_gateways": network.nat_gateways_list or [],
            "vm_count": len(network.leases) if network.leases else 0,
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
    def to_json(networks: list[NetworkItem]) -> list[dict[str, Any]]:
        """
        Convert network model list to JSON-serializable dicts.

        Args:
            networks: List of NetworkItem records.

        Returns:
            List of network dicts suitable for JSON serialization.

        """
        return [NetworkOperation._network_to_dict(n) for n in networks]

    @staticmethod
    def inspect(inputs: NetworkInput) -> dict[str, Any]:
        """
        Inspect a network with enriched data (leases, bridge state).

        Args:
            inputs: NetworkInput with name/id identifiers.

        Returns:
            Grouped dict representation of the network.

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

        return {
            "network": {
                "id": updated.id,
                "name": updated.name,
                "subnet": updated.subnet,
                "bridge": updated.bridge,
                "ipv4_gateway": updated.ipv4_gateway,
                "is_default": updated.is_default,
                "is_present": updated.is_present,
                "created_at": updated.created_at,
                "updated_at": updated.updated_at,
            },
            "status": {
                "bridge_active": updated.bridge_active,
                "is_present": updated.is_present,
                "is_default": updated.is_default,
            },
            "nat": {
                "nat_enabled": updated.nat_enabled,
                "nat_gateways": updated.nat_gateways_list or [],
            },
            "leases": [
                {
                    "id": lease.id,
                    "vm_id": lease.vm_id,
                    "ipv4": lease.ipv4,
                    "leased_at": lease.leased_at,
                    "expires_at": lease.expires_at,
                }
                for lease in (updated.leases or [])
            ],
        }

    @staticmethod
    def set_default(inputs: NetworkInput) -> OperationResult[NetworkItem]:
        """
        Set a network as the default.

        Args:
            inputs: NetworkInput with name/id identifiers.

        Returns:
            OperationResult with the network that was set as default.

        """
        from mvmctl.api.inputs._network_input import NetworkRequest

        db = Database()
        repo = NetworkRepository(db)

        request = NetworkRequest(inputs=inputs, db=db)
        try:
            resolved = request.resolve()
        except NetworkError as e:
            return OperationResult(
                status="error",
                code="network.default_set_failed",
                message=str(e),
                exception=e,
            )

        if len(resolved.networks) != 1:
            return OperationResult(
                status="error",
                code="network.default_set_failed",
                message=f"Expected exactly one network, got {len(resolved.networks)}",
            )

        network = resolved.networks[0]
        try:
            controller = NetworkController(network, repo)
            controller.set_default()
        except NetworkError as e:
            return OperationResult(
                status="error",
                code="network.default_set_failed",
                message=str(e),
                exception=e,
            )

        AuditLog.log("network.set_default", changes={"name": network.name})

        return OperationResult(
            status="success",
            code="network.default_set",
            item=network,
            message=f"Network '{network.name}' set as default",
        )

    @staticmethod
    def create_default_network() -> OperationResult[NetworkItem]:
        """
        Create the default network if it doesn't exist, ensure one network is default, and materialize its bridge/NAT.

        Idempotent — safe to call multiple times.

        Returns:
            OperationResult with the default NetworkItem.

        """
        from mvmctl.api.inputs._network_create_input import NetworkCreateInput

        db = Database()
        repo = NetworkRepository(db)

        default_name = SettingsService.resolve(db, "defaults.network", "name")
        default_subnet = SettingsService.resolve(
            db, "defaults.network", "subnet"
        )
        default_nat_enabled = SettingsService.resolve(
            db, "defaults.network", "nat_enabled"
        )

        try:
            # 1. Ensure internal default network exists
            internal_network = repo.get_by_name(default_name)
            if internal_network is None:
                outbound_iface = NetworkUtils.detect_outbound_interface()
                nat_gateways = [outbound_iface] if outbound_iface else []

                create_input = NetworkCreateInput(
                    name=default_name,
                    subnet=default_subnet,
                    nat_enabled=default_nat_enabled and len(nat_gateways) > 0,
                    nat_gateways=nat_gateways,
                )
                create_result = NetworkOperation.create(create_input)
                # NeedsInteraction is not expected during default network creation
                if isinstance(create_result, NeedsInteraction):
                    return OperationResult(
                        status="error",
                        code="network.default_created_failed",
                        message=create_result.message,
                    )
                if create_result.status in ("error", "failure"):
                    return create_result
                internal_network = create_result.item
                if internal_network is None:
                    return OperationResult(
                        status="error",
                        code="network.default_created_failed",
                        message=f"Failed to create default network '{default_name}'",
                    )
                HostRepository(db).update_component(
                    "default_network_created", True
                )

            # 2. Ensure there is a default network
            default_network = repo.get_default()
            if default_network is None:
                controller = NetworkController(internal_network, repo)
                controller.set_default()
                default_network = repo.get_default() or internal_network

            # 3. Materialize bridge and NAT
            service = NetworkService(repo)
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
        except NetworkError as e:
            return OperationResult(
                status="error",
                code="network.default_created_failed",
                message=str(e),
                exception=e,
            )

        # Update bridge_active
        bridge_active = NetworkUtils.bridge_exists(default_network.bridge)
        if bridge_active != default_network.bridge_active:
            repo.update_bridge_active(default_network.id, bridge_active)

        final_network = repo.get_default() or default_network
        return OperationResult(
            status="success",
            code="network.default_created",
            item=final_network,
            message=f"Default network '{final_network.name}' ready",
        )

    @staticmethod
    def sync(
        network_id: str | None = None,
    ) -> OperationResult[dict[str, dict[str, int]]]:
        """
        Sync iptables rules for one or all networks.

        First restores any missing bridges and NAT rules (post-reboot recovery),
        then reconciles bridge state (DB vs kernel), then ensures all active
        DB rules exist in host iptables and detects orphaned host rules.

        Merges the legacy ``restore()`` logic — callers only need ``sync()``
        for both post-reboot recovery and routine rule sync.

        Args:
            network_id: Specific network ID to sync, or None for all networks.

        Returns:
            OperationResult wrapping a dict mapping
            network_id -> {"added": int, "verified": int, "orphaned": int}.
            Metadata includes "network_count" and "bridges_reconciled".

        """
        db = Database()
        repo = NetworkRepository(db)
        service = NetworkService(repo)

        try:
            if network_id is not None:
                network = repo.get(network_id)
                if network is None:
                    return OperationResult(
                        status="error",
                        code="network.sync_failed",
                        message=f"Network '{network_id}' not found",
                    )
                networks = [network]
            else:
                networks = repo.list_all()

            # Step 1: Restore missing bridges (post-reboot recovery)
            for network in networks:
                if not NetworkUtils.bridge_exists(network.bridge):
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

            # Step 2: Reconcile bridge state (DB vs kernel)
            bridges_reconciled = 0
            for network in networks:
                bridge_active = NetworkUtils.bridge_exists(network.bridge)
                if bridge_active != network.bridge_active:
                    repo.update_bridge_active(network.id, bridge_active)
                    bridges_reconciled += 1

            # Step 3: Sync firewall rules
            results: dict[str, dict[str, int]] = {}
            for network in networks:
                result = service.sync_iptables_rules(network)
                results[network.id] = result
        except NetworkError as e:
            return OperationResult(
                status="error",
                code="network.sync_failed",
                message=str(e),
                exception=e,
            )

        return OperationResult(
            status="success",
            code="network.synced",
            item=results,
            message="Network synced",
            metadata={
                "network_count": len(results),
                "bridges_reconciled": bridges_reconciled,
            },
        )
