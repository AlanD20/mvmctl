"""Network management for VM networking."""

import logging
import subprocess
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.core._internal._iptables_tracker import (
    IPTablesRuleRepository,
    IPTablesTracker,
)
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkError
from mvmctl.models.network import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRuleItem,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
    NetworkItem,
)
from mvmctl.utils.network import NetworkUtils
from mvmctl.utils.process import privileged_cmd as _privileged_cmd

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)


# MVM iptables chains configuration: (chain_enum, table_enum, jump_from_chain)
_MVM_CHAINS_CONFIG: list[tuple[IPTablesChain, IPTablesTable, str]] = [
    (IPTablesChain.MVM_FORWARD, IPTablesTable.FILTER, "FORWARD"),
    (IPTablesChain.MVM_POSTROUTING, IPTablesTable.NAT, "POSTROUTING"),
    (IPTablesChain.MVM_NOCLOUDNET_INPUT, IPTablesTable.FILTER, "INPUT"),
]


class NetworkService:
    """Manages network interfaces, bridges, TAP devices, and NAT rules.

    Stateless class - all methods require explicit parameters.
    Shares database connection with IPTablesTracker for rule synchronization.
    """

    def __init__(self, repo: NetworkRepository) -> None:
        """Initialize NetworkService.

        Args:
            repo: NetworkRepository instance for network DB operations.
        """
        self._repo = repo
        self._iptables_repo = IPTablesRuleRepository(repo.db)
        self._tracker = IPTablesTracker(repo=self._iptables_repo)

    @property
    def tracker(self) -> IPTablesTracker:
        """Create an IPTablesTracker with the shared repository."""
        return self._tracker

    def list_all(self, verify: bool = True) -> list[NetworkItem]:
        """List all networks, syncing is_present flag with bridge state.

        Checks each network's bridge on the host and bulk-updates is_present
        for any that are missing. Returns the full list with updated state.

        Args:
            verify: If True (default), check bridge existence and update DB.
                   If False, return DB records as-is.
        """
        networks = self._repo.list_all()
        if not verify:
            return networks

        missing_ids: list[str] = []
        for network in networks:
            if not NetworkUtils.bridge_exists(network.bridge):
                missing_ids.append(network.id)

        if missing_ids:
            self._repo.update_many_is_present(missing_ids, False)
            networks = self._repo.list_all()

        return networks

    def ensure_mvm_chains(self) -> None:
        """Ensure MVM iptables chains exist with proper jump rules.

        Idempotent operation - safe to call multiple times.
        Creates MVM chains and sets up jump rules from standard chains.

        Creates three chains:
        - MVM-FORWARD (filter table, jumped from FORWARD)
        - MVM-POSTROUTING (nat table, jumped from POSTROUTING)
        - MVM-NOCLOUDNET-INPUT (filter table, jumped from INPUT)
        """

        for chain_enum, table_enum, jump_from in _MVM_CHAINS_CONFIG:
            self._tracker.ensure_chain(
                chain_name=chain_enum,
                table=table_enum,
                auto_jump_from=jump_from,
                position=1,
            )

    def remove_mvm_chains(self) -> None:
        """Remove all MVM iptables chains and their rules.

        This deletes the custom MVM chains and all their rules.
        Use with caution - this removes all MVM firewall rules.
        """
        for chain_enum, table_enum, _ in _MVM_CHAINS_CONFIG:
            self._tracker.remove_chain(chain_name=chain_enum, table=table_enum)

    def initialize(self) -> None:
        """Initialize MVM iptables chains with proper jump rules.

        Idempotent operation - safe to call multiple times.
        Creates MVM chains and sets up jump rules from standard chains.

        .. deprecated::
            Use :meth:`ensure_mvm_chains` instead.
        """
        self.ensure_mvm_chains()

    def detect_iptables_backend_conflict(self) -> tuple[bool, str]:
        """Detect mixed iptables backend conflict."""
        return NetworkUtils.detect_iptables_backend_conflict()

    def ensure_bridge(
        self,
        bridge: str,
        subnet: str,
    ) -> None:
        """Create and configure the bridge interface."""
        if NetworkUtils.bridge_exists(bridge):
            logger.debug("Bridge %s already exists, reconciling state", bridge)
            reconcile_cmds: list[str] = []
            if not NetworkUtils.bridge_has_subnet(bridge, subnet):
                reconcile_cmds.append(f"addr add {subnet} dev {bridge}")
            reconcile_cmds.append(f"link set {bridge} up")
            try:
                NetworkUtils._run_batch(reconcile_cmds)
            except subprocess.CalledProcessError as e:
                raise NetworkError(f"Failed to setup bridge {bridge}") from e
        else:
            try:
                NetworkUtils._run_batch(
                    [
                        f"link add name {bridge} type bridge",
                        f"addr add {subnet} dev {bridge}",
                        f"link set {bridge} up",
                    ]
                )
            except subprocess.CalledProcessError as e:
                raise NetworkError(f"Failed to setup bridge {bridge}") from e

        # ip forwarding has to be enabled
        self.ensure_ip_forwarding()

        logger.info("Bridge %s created with subnet %s", bridge, subnet)

    def ensure_ip_forwarding(self) -> None:
        """Enable IP forwarding for NAT.

        Idempotent operation - safe to call multiple times.
        Tries /proc/sys first, falls back to sysctl command.
        """
        try:
            Path("/proc/sys/net/ipv4/ip_forward").write_text("1\n")
        except OSError:
            try:
                subprocess.run(
                    _privileged_cmd(["sysctl", "-w", "net.ipv4.ip_forward=1"]),
                    capture_output=True,
                    check=True,
                )
            except subprocess.CalledProcessError as e:
                logger.debug("Failed to enable IP forwarding", exc_info=True)
                raise NetworkError("Failed to enable IP forwarding") from e

    def remove_bridge(self, bridge: str) -> None:
        """Remove the bridge interface and clean up attached TAPs.

        Idempotent operation - safe to call multiple times.
        Removes all TAP devices attached to the bridge before removing the bridge.
        Note: NAT rules are NOT removed by this method; call remove_nat() separately.

        Args:
            bridge: Bridge interface name to remove.
        """
        attached_taps = NetworkUtils.get_bridge_taps(bridge)
        for tap in attached_taps:
            logger.debug("Removing attached TAP %s from bridge %s", tap, bridge)
            self.remove_tap(tap, bridge)

        try:
            NetworkUtils._run_batch(
                [f"link set {bridge} down", f"link delete {bridge} type bridge"]
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to teardown bridge {bridge}") from e

        logger.info("Bridge %s removed", bridge)

    def ensure_nat(
        self,
        bridge: str,
        nat_gateways: list[str],
        *,
        subnet: str,
        network_id: str,
    ) -> None:
        """Ensure NAT rules exist for the bridge subnet.

        Idempotent operation - safe to call multiple times.
        Creates MASQUERADE and FORWARD rules via IPTablesTracker.
        """
        # Ensure MVM chains exist before adding rules
        self.initialize()

        for gateway_iface in nat_gateways:
            context = f"{bridge}:{gateway_iface}"

            # MASQUERADE rule
            masquerade_rule = IPTablesRuleItem(
                table_name=IPTablesTable.NAT,
                chain_name=IPTablesChain.MVM_POSTROUTING,
                rule_type=IPTablesRuleType.MASQUERADE,
                target=IPTablesTarget.ACCEPT,
                network_id=network_id,
                protocol=IPTablesProtocol.ALL,
                source=subnet,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=IPTablesWildcard.ANY_INTERFACE,
                out_interface=gateway_iface,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            result = self._tracker.ensure_rule(masquerade_rule, context=context)
            if not result.success:
                raise NetworkError(
                    f"Failed to add MASQUERADE rule for {bridge} via {gateway_iface}: {result.error_message}"
                )

            # Forward out rule
            forward_out_rule = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_OUT,
                target=IPTablesTarget.ACCEPT,
                network_id=network_id,
                protocol=IPTablesProtocol.ALL,
                source=subnet,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=bridge,
                out_interface=gateway_iface,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            result = self._tracker.ensure_rule(
                forward_out_rule, context=context
            )
            if not result.success:
                raise NetworkError(
                    f"Failed to add FORWARD out rule for {bridge} via {gateway_iface}: {result.error_message}"
                )

            # Forward in rule
            forward_in_rule = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_IN,
                target=IPTablesTarget.ACCEPT,
                network_id=network_id,
                protocol=IPTablesProtocol.ALL,
                source=IPTablesWildcard.ANY_CIDR,
                destination=subnet,
                in_interface=gateway_iface,
                out_interface=bridge,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            result = self._tracker.ensure_rule(forward_in_rule, context=context)
            if not result.success:
                raise NetworkError(
                    f"Failed to add FORWARD in rule for {bridge} via {gateway_iface}: {result.error_message}"
                )

        # ip forwarding has to be enabled
        self.ensure_ip_forwarding()

        logger.info(
            "NAT rules configured for bridge %s via %s (source %s)",
            bridge,
            ", ".join(nat_gateways),
            subnet,
        )

    def remove_nat(
        self,
        bridge: str,
        nat_gateways: list[str] | None = None,
        *,
        subnet: str | None = None,
    ) -> None:
        """Remove NAT (MASQUERADE + FORWARD) rules for the bridge.

        Idempotent operation - safe to call multiple times.
        Uses IPTablesTracker to remove rules from iptables and mark as deleted in DB.

        Args:
            bridge: Bridge interface name (also used as network name to query DB).
            subnet: Subnet CIDR (e.g., "10.0.0.0/24"). If None, queries from database.
            nat_gateways: List of gateway interfaces. If None, queries from database.
        """
        from mvmctl.core.network._resolver import NetworkResolver

        effective_nat_gateways = nat_gateways
        effective_subnet = subnet

        if effective_nat_gateways is None or effective_subnet is None:
            resolver = NetworkResolver(repo=self._repo)
            try:
                network = resolver.by_name(bridge)
                if effective_subnet is None:
                    effective_subnet = network.subnet
                if effective_nat_gateways is None:
                    effective_nat_gateways = network.nat_gateways_list
            except Exception:
                pass

        if effective_nat_gateways is None:
            raise NetworkError(
                f"Could not determine NAT gateways for bridge {bridge}. "
                f"Provide nat_gateways explicitly or ensure network exists in database."
            )
        if effective_subnet is None:
            raise NetworkError(
                f"Could not determine subnet for bridge {bridge}. "
                f"Provide subnet explicitly or ensure network exists in database."
            )

        # Check for attached TAPs — log warning but continue with removal
        attached_taps = NetworkUtils.get_bridge_taps(bridge)
        if attached_taps:
            logger.warning(
                "Removing NAT for bridge %s but %d TAP(s) still attached: %s",
                bridge,
                len(attached_taps),
                ", ".join(attached_taps),
            )

        for gateway_iface in effective_nat_gateways:
            masquerade_rule = IPTablesRuleItem(
                table_name=IPTablesTable.NAT,
                chain_name=IPTablesChain.MVM_POSTROUTING,
                rule_type=IPTablesRuleType.MASQUERADE,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
                protocol=IPTablesProtocol.ALL,
                source=effective_subnet,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=IPTablesWildcard.ANY_INTERFACE,
                out_interface=gateway_iface,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            self._tracker.remove_rule(masquerade_rule)

            forward_out_rule = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_OUT,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
                protocol=IPTablesProtocol.ALL,
                source=effective_subnet,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=bridge,
                out_interface=gateway_iface,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            self._tracker.remove_rule(forward_out_rule)

            forward_in_rule = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_IN,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
                protocol=IPTablesProtocol.ALL,
                source=IPTablesWildcard.ANY_CIDR,
                destination=effective_subnet,
                in_interface=gateway_iface,
                out_interface=bridge,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            self._tracker.remove_rule(forward_in_rule)

        logger.info(
            "NAT rules removed for bridge %s via %s (source %s)",
            bridge,
            ", ".join(effective_nat_gateways),
            effective_subnet,
        )

    def ensure_tap(self, tap: str, bridge: str) -> None:
        """Ensure a TAP device exists and is attached to the bridge with iptables rules.

        Idempotent operation - safe to call multiple times.
        If TAP already exists and is attached to the correct bridge, reconciles iptables rules.

        Creates two FORWARD rules in MVM-FORWARD chain:
        - FORWARD_OUT (bridge -> TAP): traffic exiting bridge toward VM
        - FORWARD_IN (TAP -> bridge): traffic entering bridge from VM

        Rule types are named from the bridge's perspective:
        - "OUT" = leaving the bridge toward the TAP
        - "IN" = entering the bridge from the TAP
        """
        if NetworkUtils.tap_exists(tap):
            current_bridge = NetworkUtils.get_tap_bridge(tap)
            if current_bridge == bridge:
                logger.debug(
                    "TAP device %s already attached to bridge %s", tap, bridge
                )
            else:
                if current_bridge:
                    logger.warning(
                        "TAP device %s exists but attached to different bridge %s, reattaching to %s",
                        tap,
                        current_bridge,
                        bridge,
                    )
                    try:
                        NetworkUtils._run_batch(
                            [
                                f"link set {tap} down",
                                f"link set {tap} master {bridge}",
                                f"link set {tap} up",
                            ]
                        )
                    except subprocess.CalledProcessError as e:
                        raise NetworkError(
                            f"Failed to reattach TAP {tap} to bridge {bridge}"
                        ) from e
                else:
                    try:
                        NetworkUtils._run_batch(
                            [
                                f"link set {tap} master {bridge}",
                                f"link set {tap} up",
                            ]
                        )
                    except subprocess.CalledProcessError as e:
                        raise NetworkError(
                            f"Failed to attach TAP {tap} to bridge {bridge}"
                        ) from e
                logger.info(
                    "TAP device %s reattached to bridge %s", tap, bridge
                )
        else:
            try:
                NetworkUtils._run_batch(
                    [
                        f"tuntap add dev {tap} mode tap",
                        f"link set {tap} master {bridge}",
                        f"link set {tap} up",
                    ]
                )
            except subprocess.CalledProcessError as e:
                raise NetworkError(f"Failed to create TAP {tap}") from e
            logger.info(
                "TAP device %s created and attached to bridge %s", tap, bridge
            )

        self.initialize()

        forward_bridge_to_tap = IPTablesRuleItem(
            table_name=IPTablesTable.FILTER,
            chain_name=IPTablesChain.MVM_FORWARD,
            rule_type=IPTablesRuleType.FORWARD_OUT,
            target=IPTablesTarget.ACCEPT,
            network_id=bridge,
            protocol=IPTablesProtocol.ALL,
            source=IPTablesWildcard.ANY_CIDR,
            destination=IPTablesWildcard.ANY_CIDR,
            in_interface=bridge,
            out_interface=tap,
            sport=IPTablesPort.ANY,
            dport=IPTablesPort.ANY,
            is_active=True,
            network_name=bridge,
        )
        result = self._tracker.ensure_rule(
            forward_bridge_to_tap, context=f"tap:{tap}"
        )
        if not result.success:
            raise NetworkError(
                f"Failed to add FORWARD rule for bridge {bridge} to TAP {tap}: {result.error_message}"
            )

        forward_tap_to_bridge = IPTablesRuleItem(
            table_name=IPTablesTable.FILTER,
            chain_name=IPTablesChain.MVM_FORWARD,
            rule_type=IPTablesRuleType.FORWARD_IN,
            target=IPTablesTarget.ACCEPT,
            network_id=bridge,
            protocol=IPTablesProtocol.ALL,
            source=IPTablesWildcard.ANY_CIDR,
            destination=IPTablesWildcard.ANY_CIDR,
            in_interface=tap,
            out_interface=bridge,
            sport=IPTablesPort.ANY,
            dport=IPTablesPort.ANY,
            is_active=True,
            network_name=bridge,
        )
        result = self._tracker.ensure_rule(
            forward_tap_to_bridge, context=f"tap:{tap}"
        )
        if not result.success:
            self._tracker.remove_rule(forward_bridge_to_tap)
            raise NetworkError(
                f"Failed to add FORWARD rule for TAP {tap} to bridge {bridge}: {result.error_message}"
            )

    def remove_tap(self, tap: str, bridge: str | None = None) -> None:
        """Remove a TAP device and its iptables forwarding rules.

        Idempotent operation - safe to call multiple times.
        If TAP doesn't exist, does nothing.

        Args:
            tap: TAP device name to remove.
            bridge: Bridge name the TAP is attached to. If None, attempts to detect.
        """
        if not NetworkUtils.tap_exists(tap):
            logger.debug("TAP device %s does not exist, skipping removal", tap)
            return

        effective_bridge = (
            bridge if bridge is not None else NetworkUtils.get_tap_bridge(tap)
        )
        if effective_bridge is None:
            logger.warning(
                "Could not determine bridge for TAP %s, skipping rule cleanup",
                tap,
            )
        else:
            forward_bridge_to_tap = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_OUT,
                target=IPTablesTarget.ACCEPT,
                network_id=effective_bridge,
                protocol=IPTablesProtocol.ALL,
                source=IPTablesWildcard.ANY_CIDR,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=effective_bridge,
                out_interface=tap,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=effective_bridge,
            )
            self._tracker.remove_rule(forward_bridge_to_tap)

            forward_tap_to_bridge = IPTablesRuleItem(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_IN,
                target=IPTablesTarget.ACCEPT,
                network_id=effective_bridge,
                protocol=IPTablesProtocol.ALL,
                source=IPTablesWildcard.ANY_CIDR,
                destination=IPTablesWildcard.ANY_CIDR,
                in_interface=tap,
                out_interface=effective_bridge,
                sport=IPTablesPort.ANY,
                dport=IPTablesPort.ANY,
                is_active=True,
                network_name=effective_bridge,
            )
            self._tracker.remove_rule(forward_tap_to_bridge)

        try:
            NetworkUtils._run_batch(
                [f"link set {tap} down", f"link delete {tap}"]
            )
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to remove TAP {tap}") from e

        logger.info("TAP device %s removed", tap)


__all__ = ["NetworkService"]
