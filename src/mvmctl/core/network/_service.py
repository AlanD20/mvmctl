"""Network management for VM networking."""

from __future__ import annotations

import logging
from collections.abc import Generator
from contextlib import contextmanager
from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.core._shared import FirewallTracker
from mvmctl.core.network._repository import NetworkRepository
from mvmctl.exceptions import NetworkError, ProcessError
from mvmctl.models import (
    FirewallChain,
    FirewallPort,
    FirewallProtocol,
    FirewallRule,
    FirewallRuleType,
    FirewallTable,
    FirewallTarget,
    FirewallWildcard,
    NetworkItem,
)
from mvmctl.utils._system import run_cmd
from mvmctl.utils.network import NetworkUtils

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)


class NetworkService:
    """
    Manages network interfaces, bridges, TAP devices, and NAT/firewall rules.

    Stateless class - all methods require explicit parameters.
    Supports both iptables and nftables backends, determined by the
    ``firewall_backend`` setting ("iptables" or "nftables").
    Shares database connection with trackers for rule synchronization.
    """

    def __init__(self, repo: NetworkRepository) -> None:
        """
        Initialize NetworkService.

        Selects the firewall backend (iptables or nftables) based on the
        ``firewall_backend`` setting and stores the chosen tracker as
        ``self._tracker``.  Exactly one tracker is instantiated.

        Args:
            repo: NetworkRepository instance for network DB operations.

        """
        self._repo = repo
        self._tracker = FirewallTracker(repo.db)

    @property
    def tracker(self) -> FirewallTracker:
        """Return the active firewall tracker instance."""
        return self._tracker

    @contextmanager
    def batch(self) -> Generator[None, None, None]:
        """Context manager: queue firewall rules, flush atomically on exit.

        Delegates to :meth:`FirewallTracker.batch` which handles the
        batching logic for both iptables and nftables backends.
        """
        with self._tracker.batch():
            yield

    def list_all(self, verify: bool = True) -> list[NetworkItem]:
        """
        List all networks, syncing is_present flag with bridge state.

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
        """Ensure firewall chains exist via the active backend.

        Delegates to :meth:`FirewallTracker.initialize` which handles
        both iptables and nftables backends. Idempotent.
        """
        self._tracker.initialize()

    def initialize(self) -> None:
        """Ensure firewall chains exist via the active backend.

        For iptables: creates MVM chains with jump rules from standard chains.
        For nftables: creates the nftables table, hook chains, and custom chains.
        Idempotent — safe to call multiple times.
        """
        self._tracker.initialize()

    def teardown(self) -> None:
        """Remove MVM chains and their jump rules from system tables.

        Delegates to :meth:`FirewallTracker.teardown` which handles
        both iptables and nftables backends. Best-effort — safe to
        call multiple times.
        """
        self._tracker.teardown()

    def detect_iptables_backend_conflict(self) -> tuple[bool, str]:
        """Detect mixed iptables backend conflict."""
        return NetworkUtils.detect_iptables_backend_conflict()

    def ensure_bridge(
        self,
        bridge: str,
        bridge_address: str,
    ) -> None:
        """
        Create and configure the bridge interface.

        Args:
            bridge: Bridge interface name.
            bridge_address: Bridge IP address with prefix (e.g. '172.29.0.1/28').

        """
        if NetworkUtils.bridge_exists(bridge):
            logger.debug("Bridge %s already exists, reconciling state", bridge)
            reconcile_cmds: list[str] = []
            if not NetworkUtils.bridge_has_subnet(bridge, bridge_address):
                reconcile_cmds.append(f"addr add {bridge_address} dev {bridge}")
            reconcile_cmds.append(f"link set {bridge} up")
            try:
                NetworkUtils._run_batch(reconcile_cmds)
            except ProcessError as e:
                raise NetworkError(f"Failed to setup bridge {bridge}") from e
        else:
            try:
                NetworkUtils._run_batch(
                    [
                        f"link add name {bridge} type bridge",
                        f"addr add {bridge_address} dev {bridge}",
                        f"link set {bridge} up",
                    ]
                )
            except ProcessError as e:
                raise NetworkError(f"Failed to setup bridge {bridge}") from e

        # ip forwarding has to be enabled
        self.ensure_ip_forwarding()

        logger.info("Bridge %s created with address %s", bridge, bridge_address)

    def ensure_ip_forwarding(self) -> None:
        """
        Enable IP forwarding for NAT.

        Idempotent operation - safe to call multiple times.
        Tries /proc/sys first, falls back to sysctl command.
        """
        try:
            Path("/proc/sys/net/ipv4/ip_forward").write_text("1\n")
        except OSError:
            try:
                run_cmd(
                    ["sysctl", "-w", "net.ipv4.ip_forward=1"],
                    privileged=True,
                )
            except ProcessError as e:
                logger.debug("Failed to enable IP forwarding", exc_info=True)
                raise NetworkError("Failed to enable IP forwarding") from e

    def remove_bridge(self, bridge: str, *, network_id: str) -> None:
        """
        Remove the bridge interface and clean up attached TAPs.

        Idempotent operation - safe to call multiple times.
        Removes all TAP devices attached to the bridge before removing the bridge.
        Note: NAT rules are NOT removed by this method; call remove_nat() separately.

        Args:
            bridge: Bridge interface name to remove.
            network_id: Network UUID for iptables rule tracking.

        """
        attached_taps = NetworkUtils.get_bridge_taps(bridge)
        for tap in attached_taps:
            logger.debug("Removing attached TAP %s from bridge %s", tap, bridge)
            self.remove_tap(tap, bridge, network_id=network_id)

        try:
            NetworkService.remove_raw_bridge(bridge)
        except NetworkError as e:
            raise NetworkError(f"Failed to teardown bridge {bridge}") from e

        logger.info("Bridge %s removed", bridge)

    @staticmethod
    def remove_raw_tap(tap: str) -> None:
        """Remove a TAP device robustly without iptables cleanup.

        First tries ``ip link delete <tap>``. If that fails (e.g. for
        certain tuntap implementations), falls back to
        ``ip tuntap del dev <tap> mode tap``.

        If the TAP does not exist, this is a silent no-op.

        Args:
            tap: TAP device name.

        Raises:
            NetworkError: If the device exists but cannot be removed.

        """
        if not NetworkUtils.tap_exists(tap):
            return

        # Bring down (best effort — may already be down)
        run_cmd(
            ["ip", "link", "set", tap, "down"],
            privileged=True,
            check=False,
        )

        # Try standard link delete first
        result = run_cmd(
            ["ip", "link", "delete", tap],
            privileged=True,
            check=False,
        )
        if result.returncode == 0:
            return

        stderr_first = result.stderr.strip()

        # Fallback for tuntap-type interfaces
        result = run_cmd(
            ["ip", "tuntap", "del", "dev", tap, "mode", "tap"],
            privileged=True,
            check=False,
        )
        if result.returncode == 0:
            return

        details = f" ({stderr_first})" if stderr_first else ""
        raise NetworkError(
            f"Failed to remove TAP device '{tap}'. "
            f"Tried 'ip link delete'{details} and 'ip tuntap del'."
        )

    @staticmethod
    def remove_raw_bridge(bridge: str) -> None:
        """Remove a bridge interface robustly, including all attached slaves.

        Iterates over all interfaces attached to the bridge, removes each
        one, then brings the bridge down and deletes it.

        If the bridge does not exist, this is a silent no-op.

        Args:
            bridge: Bridge interface name.

        Raises:
            NetworkError: If the bridge exists but cannot be removed.

        """
        if not NetworkUtils.bridge_exists(bridge):
            return

        # Remove all attached slaves first
        for slave in NetworkUtils.get_bridge_slaves(bridge):
            if slave == bridge:
                continue
            run_cmd(
                ["ip", "link", "set", slave, "down"],
                privileged=True,
                check=False,
            )
            result = run_cmd(
                ["ip", "link", "delete", slave],
                privileged=True,
                check=False,
            )
            if result.returncode != 0:
                # Try tuntap fallback for TAP slaves
                run_cmd(
                    ["ip", "tuntap", "del", "dev", slave, "mode", "tap"],
                    privileged=True,
                    check=False,
                )

        # Bring bridge down and delete
        run_cmd(
            ["ip", "link", "set", bridge, "down"],
            privileged=True,
            check=False,
        )
        result = run_cmd(
            ["ip", "link", "delete", bridge, "type", "bridge"],
            privileged=True,
            check=False,
        )
        if result.returncode == 0:
            return

        stderr_first = result.stderr.strip()

        # Fallback: try without type specifier
        result = run_cmd(
            ["ip", "link", "delete", bridge],
            privileged=True,
            check=False,
        )
        if result.returncode == 0:
            return

        details = f" ({stderr_first})" if stderr_first else ""
        raise NetworkError(
            f"Failed to remove bridge '{bridge}'. "
            f"Tried 'ip link delete' with type{details} and without."
        )

    @staticmethod
    def check_nftables_available() -> bool:
        """
        Check if nftables with MASQUERADE support is available.

        Tests nft binary existence, kernel module availability, and actual
        MASQUERADE rule creation capability. All commands use ``check=False``
        so no exceptions escape this method.

        Returns:
            True if nftables with MASQUERADE is available, False otherwise.

        """
        # Check nft binary exists and runs
        result = run_cmd(["nft", "--version"], privileged=True, check=False)
        if result.returncode != 0:
            logger.debug("nftables not available: nft --version failed")
            return False

        # Attempt to load the NAT chain kernel module (best-effort)
        run_cmd(["modprobe", "nft_chain_nat"], privileged=True, check=False)

        # Test that MASQUERADE actually works by creating a temporary table
        test = (
            "add table inet __mvm_nft_test\n"
            "add chain inet __mvm_nft_test test_post "
            "{ type nat hook postrouting priority srcnat; policy accept; }\n"
            "add rule inet __mvm_nft_test test_post masquerade\n"
        )
        test_result = run_cmd(
            ["nft", "-f", "-"],
            privileged=True,
            check=False,
            input=test,
        )

        # Cleanup — best-effort, ignore failures
        run_cmd(
            ["nft", "delete", "table", "inet", "__mvm_nft_test"],
            privileged=True,
            check=False,
        )

        available = test_result.returncode == 0
        if not available:
            logger.debug(
                "nftables MASQUERADE not available "
                "(kernel module nft_chain_nat may be missing)"
            )
        return available

    @staticmethod
    def remove_stale_interfaces(prefix: str) -> list[str]:
        """Remove all slave interfaces attached to bridges matching a prefix.

        Iterates over all system bridges, finds those starting with ``prefix``,
        enumerates their slave interfaces, and deletes each one. This prevents
        bridge deletion failures when interfaces are still attached.

        Args:
            prefix: Bridge name prefix to match (e.g., ``"mvm-"``).

        Returns:
            List of summary strings describing actions taken.

        """
        summary: list[str] = []
        for bridge in NetworkUtils.get_bridges():
            if not bridge.startswith(prefix):
                continue
            for slave in NetworkUtils.get_bridge_slaves(bridge):
                try:
                    NetworkService.remove_raw_tap(slave)
                    summary.append(f"Removed interface '{slave}'")
                except NetworkError as e:
                    summary.append(
                        f"Warning: failed to remove interface '{slave}': {e}"
                    )
        return summary

    def ensure_nat(
        self,
        bridge: str,
        nat_gateways: list[str],
        *,
        subnet: str,
        network_id: str,
    ) -> None:
        """
        Ensure NAT rules exist for the bridge subnet.

        Idempotent operation - safe to call multiple times.
        Creates MASQUERADE and FORWARD rules via IPTablesTracker.
        """
        # Ensure MVM chains exist before adding rules
        self.initialize()

        for gateway_iface in nat_gateways:
            masquerade_rule = FirewallRule(
                table_name=FirewallTable.NAT,
                chain_name=FirewallChain.MVM_POSTROUTING,
                rule_type=FirewallRuleType.MASQUERADE,
                target=FirewallTarget.MASQUERADE,
                network_id=network_id,
                protocol=FirewallProtocol.ALL,
                source=subnet,
                destination=FirewallWildcard.ANY_CIDR,
                in_interface=FirewallWildcard.ANY_INTERFACE,
                out_interface=gateway_iface,
                sport=FirewallPort.ANY,
                dport=FirewallPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            forward_out_rule = FirewallRule(
                table_name=FirewallTable.FILTER,
                chain_name=FirewallChain.MVM_FORWARD,
                rule_type=FirewallRuleType.FORWARD_OUT,
                target=FirewallTarget.ACCEPT,
                network_id=network_id,
                protocol=FirewallProtocol.ALL,
                source=subnet,
                destination=FirewallWildcard.ANY_CIDR,
                in_interface=bridge,
                out_interface=gateway_iface,
                sport=FirewallPort.ANY,
                dport=FirewallPort.ANY,
                is_active=True,
                network_name=bridge,
            )
            forward_in_rule = FirewallRule(
                table_name=FirewallTable.FILTER,
                chain_name=FirewallChain.MVM_FORWARD,
                rule_type=FirewallRuleType.FORWARD_IN,
                target=FirewallTarget.ACCEPT,
                network_id=network_id,
                protocol=FirewallProtocol.ALL,
                source=FirewallWildcard.ANY_CIDR,
                destination=subnet,
                in_interface=gateway_iface,
                out_interface=bridge,
                sport=FirewallPort.ANY,
                dport=FirewallPort.ANY,
                is_active=True,
                network_name=bridge,
            )

            context = f"{bridge}:{gateway_iface}"
            result = self._tracker.ensure_rule(masquerade_rule, context=context)
            if not result.success:
                raise NetworkError(
                    f"Failed to add MASQUERADE rule for {bridge} via {gateway_iface}: {result.error_message}"
                )
            result = self._tracker.ensure_rule(
                forward_out_rule, context=context
            )
            if not result.success:
                raise NetworkError(
                    f"Failed to add FORWARD out rule for {bridge} via {gateway_iface}: {result.error_message}"
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
        network_id: str,
        force: bool = False,
    ) -> None:
        """
        Remove NAT (MASQUERADE + FORWARD) rules for the bridge.

        Idempotent operation - safe to call multiple times.
        Uses IPTablesTracker to remove rules from iptables and mark as deleted in DB.

        Args:
            bridge: Bridge interface name (also used as network name to query DB).
            subnet: Subnet CIDR (e.g., "10.0.0.0/24"). If None, queries from database.
            nat_gateways: List of gateway interfaces. If None, queries from database.
            network_id: Network UUID for iptables rule tracking.
            force: If True, remove NAT even if TAPs are still attached.

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

        # Check for attached TAPs — raise unless force=True
        attached_taps = NetworkUtils.get_bridge_taps(bridge)
        if attached_taps:
            if not force:
                raise NetworkError(
                    f"Cannot remove NAT: {len(attached_taps)} TAP(s) still attached on bridge {bridge}. Use --force to override."
                )
            logger.warning(
                "Removing NAT for bridge %s but %d TAP(s) still attached: %s",
                bridge,
                len(attached_taps),
                ", ".join(attached_taps),
            )

        rules_to_remove: list[FirewallRule] = []
        for gateway_iface in effective_nat_gateways:
            rules_to_remove.append(
                FirewallRule(
                    table_name=FirewallTable.NAT,
                    chain_name=FirewallChain.MVM_POSTROUTING,
                    rule_type=FirewallRuleType.MASQUERADE,
                    target=FirewallTarget.MASQUERADE,
                    network_id=network_id,
                    protocol=FirewallProtocol.ALL,
                    source=effective_subnet,
                    destination=FirewallWildcard.ANY_CIDR,
                    in_interface=FirewallWildcard.ANY_INTERFACE,
                    out_interface=gateway_iface,
                    sport=FirewallPort.ANY,
                    dport=FirewallPort.ANY,
                    is_active=True,
                    network_name=bridge,
                )
            )
            rules_to_remove.append(
                FirewallRule(
                    table_name=FirewallTable.FILTER,
                    chain_name=FirewallChain.MVM_FORWARD,
                    rule_type=FirewallRuleType.FORWARD_OUT,
                    target=FirewallTarget.ACCEPT,
                    network_id=network_id,
                    protocol=FirewallProtocol.ALL,
                    source=effective_subnet,
                    destination=FirewallWildcard.ANY_CIDR,
                    in_interface=bridge,
                    out_interface=gateway_iface,
                    sport=FirewallPort.ANY,
                    dport=FirewallPort.ANY,
                    is_active=True,
                    network_name=bridge,
                )
            )
            rules_to_remove.append(
                FirewallRule(
                    table_name=FirewallTable.FILTER,
                    chain_name=FirewallChain.MVM_FORWARD,
                    rule_type=FirewallRuleType.FORWARD_IN,
                    target=FirewallTarget.ACCEPT,
                    network_id=network_id,
                    protocol=FirewallProtocol.ALL,
                    source=FirewallWildcard.ANY_CIDR,
                    destination=effective_subnet,
                    in_interface=gateway_iface,
                    out_interface=bridge,
                    sport=FirewallPort.ANY,
                    dport=FirewallPort.ANY,
                    is_active=True,
                    network_name=bridge,
                )
            )

        result = self._tracker.batch_remove_rules(rules_to_remove)
        if not result.success:
            logger.warning(
                "Failed to remove NAT rules for %s: %s",
                bridge,
                result.error_message,
            )

        logger.info(
            "NAT rules removed for bridge %s via %s (source %s)",
            bridge,
            ", ".join(effective_nat_gateways),
            effective_subnet,
        )

    def ensure_tap(
        self,
        tap: str,
        bridge: str,
        *,
        network_id: str,
        subnet: str | None = None,
    ) -> None:
        """
        Ensure a TAP device exists and is attached to the bridge with iptables rules.

        Idempotent operation - safe to call multiple times.
        If TAP already exists and is attached to the correct bridge, reconciles iptables rules.

        Creates two FORWARD rules in MVM-FORWARD chain:
        - FORWARD_OUT (bridge -> TAP): traffic exiting bridge toward VM
        - FORWARD_IN (TAP -> bridge): traffic entering bridge from VM

        Rule types are named from the bridge's perspective:
        - "OUT" = leaving the bridge toward the TAP
        - "IN" = entering the bridge from the TAP

        Args:
            tap: TAP device name.
            bridge: Bridge interface name.
            network_id: Network UUID for iptables rule tracking.
            subnet: CIDR subnet to constrain FORWARD rules (e.g. 10.0.0.0/24).

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
                    except ProcessError as e:
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
                    except ProcessError as e:
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
            except ProcessError as e:
                raise NetworkError(f"Failed to create TAP {tap}") from e
            logger.info(
                "TAP device %s created and attached to bridge %s", tap, bridge
            )

        self.initialize()

        forward_bridge_to_tap = FirewallRule(
            table_name=FirewallTable.FILTER,
            chain_name=FirewallChain.MVM_FORWARD,
            rule_type=FirewallRuleType.FORWARD_OUT,
            target=FirewallTarget.ACCEPT,
            network_id=network_id,
            protocol=FirewallProtocol.ALL,
            source=subnet or FirewallWildcard.ANY_CIDR,
            destination=FirewallWildcard.ANY_CIDR,
            in_interface=bridge,
            out_interface=tap,
            sport=FirewallPort.ANY,
            dport=FirewallPort.ANY,
            is_active=True,
            network_name=bridge,
        )
        forward_tap_to_bridge = FirewallRule(
            table_name=FirewallTable.FILTER,
            chain_name=FirewallChain.MVM_FORWARD,
            rule_type=FirewallRuleType.FORWARD_IN,
            target=FirewallTarget.ACCEPT,
            network_id=network_id,
            protocol=FirewallProtocol.ALL,
            source=FirewallWildcard.ANY_CIDR,
            destination=subnet or FirewallWildcard.ANY_CIDR,
            in_interface=tap,
            out_interface=bridge,
            sport=FirewallPort.ANY,
            dport=FirewallPort.ANY,
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
        result = self._tracker.ensure_rule(
            forward_tap_to_bridge, context=f"tap:{tap}"
        )
        if not result.success:
            self._tracker.remove_rule(forward_bridge_to_tap)
            raise NetworkError(
                f"Failed to add FORWARD rule for TAP {tap} to bridge {bridge}: {result.error_message}"
            )

    def remove_tap(
        self, tap: str, bridge: str | None = None, *, network_id: str
    ) -> None:
        """
        Remove a TAP device and its FORWARD rules.

        Idempotent operation - safe to call multiple times.
        If TAP doesn't exist, does nothing.

        Looks up the actual FORWARD rules from the DB by network ID and
        TAP interface, then removes them with their stored attributes —
        avoiding expression mismatches with wildcard-vs-specific-subnet.

        Args:
            tap: TAP device name to remove.
            bridge: Bridge name the TAP is attached to. If None, attempts to detect.
            network_id: Network UUID for rule lookup.

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
            # Look up the actual rules from DB by network_id + tap interface
            # to get the exact attributes (subnet, etc.) that were stored
            # when the rules were created by ensure_tap()
            db_rules = self._tracker.repo.get_by_network_id_and_interface(
                network_id, tap
            )
            rules_to_remove: list[FirewallRule] = list(db_rules)

            if rules_to_remove:
                result = self._tracker.batch_remove_rules(rules_to_remove)
                if not result.success:
                    logger.warning(
                        "Failed to remove FORWARD rules for TAP %s: %s",
                        tap,
                        result.error_message,
                    )

        NetworkService.remove_raw_tap(tap)

        logger.info("TAP device %s removed", tap)

    def remove(self, network: NetworkItem, *, force: bool = False) -> None:
        """
        Remove a network's infrastructure and database record.

        1. Tear down NAT rules if enabled
        2. Remove bridge
        3. VM reference check + DB removal

        Args:
            network: The NetworkItem to remove.
            force: If True, remove even if referenced by VMs.

        Raises:
            NetworkError: If infrastructure teardown fails or network is
                         referenced by VMs and force is False.

        """
        # 1. Tear down NAT
        if network.nat_enabled:
            try:
                self.remove_nat(
                    network.bridge,
                    network.nat_gateways_list,
                    subnet=network.subnet,
                    network_id=network.id,
                    force=force,
                )
            except NetworkError as e:
                logger.debug("NAT teardown for %s: %s", network.bridge, e)

        # 2. Remove bridge
        try:
            self.remove_bridge(network.bridge, network_id=network.id)
        except NetworkError as e:
            logger.debug("Bridge teardown for %s: %s", network.bridge, e)

        # 3. VM reference check + DB removal
        vms = network.vms or []
        has_vms = bool(vms)
        if has_vms and not force:
            raise NetworkError(
                f"Network referenced by VMs: {', '.join(v.name for v in vms)}"
            )
        if has_vms:
            self._repo.soft_delete(network.id)
        else:
            self._repo.delete(network.id)

    def remove_many(
        self, networks: list[NetworkItem], *, force: bool = False
    ) -> None:
        """
        Remove multiple networks.

        Args:
            networks: List of NetworkItem to remove.
            force: If True, remove even if referenced by VMs.

        """
        for network in networks:
            self.remove(network, force=force)

    def sync_iptables_rules(self, network: NetworkItem) -> dict[str, int]:
        """
        Sync iptables rules for a network between DB and host.

        Ensures all active DB rules exist in host iptables, and detects
        orphaned host rules that are not tracked in the DB.  Uses the
        firewall tracker's batch mode to queue ``ensure_rule`` calls and
        flush atomically (nftables) or individually (iptables).

        Args:
            network: The NetworkItem to sync rules for.

        Returns:
            Dict with counts: {"added": int, "verified": int, "orphaned": int}

        """
        db_rules = self._tracker.repo.get_by_network_id(
            network.id, active_only=True
        )

        added = 0
        verified = 0

        with self._tracker.batch():
            for rule in db_rules:
                result = self._tracker.ensure_rule(rule)
                if result.success:
                    if result.command_executed is None:
                        verified += 1
                    else:
                        added += 1

        orphaned = self._tracker.count_orphaned_rules(network)

        return {"added": added, "verified": verified, "orphaned": orphaned}

    @staticmethod
    def flush_arp(bridge: str) -> None:
        """Flush ARP cache for the given bridge interface.

        When a VM is removed and a new VM is assigned the same IP, the
        host's ARP cache still maps that IP to the old (deleted) TAP
        device.  Subsequent SSH connections hit TCP timeouts on the
        stale entry before re-ARping, inflating perceived boot time.

        Call this after the TAP is created and attached, before spawning
        Firecracker, so the ARP cache is clean when the first packet
        arrives.
        """
        from mvmctl.utils._system import run_cmd

        run_cmd(
            ["ip", "neigh", "flush", "dev", bridge],
            privileged=True,
            check=False,
        )


__all__ = ["NetworkService"]
