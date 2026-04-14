"""Network management for VM networking."""

import logging
import subprocess
from pathlib import Path
from typing import TYPE_CHECKING, Optional

from mvmctl.api._internal._iptables_tracker import IPTablesTracker
from mvmctl.db.models import (
    IPTablesChain,
    IPTablesPort,
    IPTablesProtocol,
    IPTablesRule,
    IPTablesRuleType,
    IPTablesTable,
    IPTablesTarget,
    IPTablesWildcard,
)
from mvmctl.exceptions import NetworkError
from mvmctl.utils.process import privileged_cmd as _privileged_cmd

if TYPE_CHECKING:
    from mvmctl.core.mvm_db import MVMDatabase

logger = logging.getLogger(__name__)


# Excluded virtual interface prefixes when listing physical network interfaces
_EXCLUDED_VIRTUAL_INTERFACE_PREFIXES = ("mvm-", "tap", "br-", "virbr", "docker", "veth")
_EXCLUDED_INTERFACES = ("lo",)

# MVM iptables chains configuration: (chain_enum, table_enum, jump_from_chain)
_MVM_CHAINS_CONFIG: list[tuple[IPTablesChain, IPTablesTable, str]] = [
    (IPTablesChain.MVM_FORWARD, IPTablesTable.FILTER, "FORWARD"),
    (IPTablesChain.MVM_POSTROUTING, IPTablesTable.NAT, "POSTROUTING"),
]


class NetworkManager:
    """Manages network interfaces, bridges, TAP devices, and NAT rules.

    Stateless class - all methods require explicit parameters.
    Shares database connection with IPTablesTracker for rule synchronization.
    """

    def __init__(self, db: Optional["MVMDatabase"] = None) -> None:
        """Initialize NetworkManager with optional database instance.

        Args:
            db: Optional MVMDatabase instance. If not provided, IPTablesTracker
                instances will create their own.
        """
        self._db = db

    def initialize(self) -> None:
        """Initialize MVM iptables chains with proper jump rules.

        Idempotent operation - safe to call multiple times.
        Creates MVM chains and sets up jump rules from standard chains.
        """
        tracker = IPTablesTracker(db=self._db)

        for chain_enum, table_enum, jump_from in _MVM_CHAINS_CONFIG:
            tracker.ensure_chain(
                chain_name=chain_enum,
                table=table_enum,
                auto_jump_from=jump_from,
                position=1,
            )

    @staticmethod
    def _run_ip_batch(commands: list[str]) -> None:
        """Execute a batch of ip commands atomically."""
        batch = "\n".join(commands) + "\n"
        subprocess.run(
            _privileged_cmd(["ip", "-batch", "-"]),
            input=batch,
            text=True,
            check=True,
            capture_output=True,
        )

    def get_physical_interfaces(self) -> list[str]:
        """Get available physical network interfaces."""
        try:
            net_path = Path("/sys/class/net")
            if not net_path.exists():
                raise NetworkError("Unable to access /sys/class/net")

            interfaces: list[str] = []
            for entry in net_path.iterdir():
                name = entry.name
                if name in _EXCLUDED_INTERFACES:
                    continue
                if any(name.startswith(prefix) for prefix in _EXCLUDED_VIRTUAL_INTERFACE_PREFIXES):
                    continue
                interfaces.append(name)

            return sorted(interfaces)
        except OSError as e:
            logger.debug("Failed to list network interfaces", exc_info=True)
            raise NetworkError("Failed to list network interfaces") from e

    def detect_outbound_interface(self) -> str | None:
        """Get the outbound (default route) network interface.

        Returns:
            The interface name (e.g., "eth0") or None if not found.
        """
        try:
            result = subprocess.run(
                ["ip", "route", "show", "default"],
                capture_output=True,
                text=True,
                check=True,
            )
        except subprocess.CalledProcessError:
            logger.debug("Failed to detect outbound network interface", exc_info=True)
            return None

        for line in result.stdout.splitlines():
            parts = line.split()
            if "dev" in parts:
                dev_idx = parts.index("dev")
                if dev_idx + 1 < len(parts):
                    return parts[dev_idx + 1]

        return None

    def detect_iptables_backend_conflict(self) -> tuple[bool, str]:
        """Detect mixed iptables backend conflict."""
        result = subprocess.run(
            ["iptables", "--version"],
            capture_output=True,
            text=True,
        )
        current_backend = "nft" if "nf_tables" in result.stderr else "legacy"

        legacy_active = False
        try:
            legacy_result = subprocess.run(
                _privileged_cmd(["iptables-legacy", "-L", "-n", "-v"]),
                capture_output=True,
                text=True,
                check=False,
            )
            if legacy_result.returncode == 0:
                for line in legacy_result.stdout.splitlines():
                    parts = line.split()
                    if len(parts) >= 2:
                        try:
                            pkts = int(parts[0])
                            if pkts > 0:
                                legacy_active = True
                                break
                        except ValueError:
                            continue
        except Exception:
            pass

        nft_active = False
        try:
            nft_result = subprocess.run(
                _privileged_cmd(["iptables", "-L", "-n", "-v"]),
                capture_output=True,
                text=True,
                check=False,
            )
            if nft_result.returncode == 0:
                for line in nft_result.stdout.splitlines():
                    parts = line.split()
                    if len(parts) >= 2:
                        try:
                            pkts = int(parts[0])
                            if pkts > 0:
                                nft_active = True
                                break
                        except ValueError:
                            continue
        except Exception:
            pass

        has_conflict = legacy_active and nft_active
        diagnosis = (
            f"iptables backend: {current_backend}, "
            f"legacy active: {legacy_active}, "
            f"nft active: {nft_active}"
        )
        return has_conflict, diagnosis

    def bridge_exists(self, bridge: str) -> bool:
        """Return True if the bridge interface exists."""
        result = subprocess.run(
            ["ip", "link", "show", bridge],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return result.returncode == 0

    def _bridge_has_subnet(self, bridge: str, subnet: str) -> bool:
        """Return True if the bridge already has the given subnet assigned."""
        result = subprocess.run(
            ["ip", "-o", "addr", "show", bridge],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return False
        return subnet in result.stdout

    def ensure_bridge(
        self,
        bridge: str,
        subnet: str,
    ) -> None:
        """Create and configure the bridge interface."""
        if self.bridge_exists(bridge):
            logger.debug("Bridge %s already exists, reconciling state", bridge)
            reconcile_cmds: list[str] = []
            if not self._bridge_has_subnet(bridge, subnet):
                reconcile_cmds.append(f"addr add {subnet} dev {bridge}")
            reconcile_cmds.append(f"link set {bridge} up")
            try:
                self._run_ip_batch(reconcile_cmds)
            except subprocess.CalledProcessError as e:
                raise NetworkError(f"Failed to setup bridge {bridge}") from e
        else:
            try:
                self._run_ip_batch(
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
        attached_taps = self.get_bridge_taps(bridge)
        for tap in attached_taps:
            logger.debug("Removing attached TAP %s from bridge %s", tap, bridge)
            self.remove_tap(tap, bridge)

        try:
            self._run_ip_batch([f"link set {bridge} down", f"link delete {bridge} type bridge"])
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to teardown bridge {bridge}") from e

        logger.info("Bridge %s removed", bridge)

    def ensure_nat(
        self,
        bridge: str,
        nat_gateways: list[str],
        *,
        subnet: str,
    ) -> None:
        """Ensure NAT rules exist for the bridge subnet.

        Idempotent operation - safe to call multiple times.
        Creates MASQUERADE and FORWARD rules via IPTablesTracker.
        """
        # Ensure MVM chains exist before adding rules
        self.initialize()

        tracker = IPTablesTracker(db=self._db)

        for gateway_iface in nat_gateways:
            context = f"{bridge}:{gateway_iface}"

            # MASQUERADE rule
            masquerade_rule = IPTablesRule(
                table_name=IPTablesTable.NAT,
                chain_name=IPTablesChain.MVM_POSTROUTING,
                rule_type=IPTablesRuleType.MASQUERADE,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
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
            result = tracker.ensure_rule(masquerade_rule, context=context)
            if not result.success:
                raise NetworkError(
                    f"Failed to add MASQUERADE rule for {bridge} via {gateway_iface}: {result.error_message}"
                )

            # Forward out rule
            forward_out_rule = IPTablesRule(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_OUT,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
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
            result = tracker.ensure_rule(forward_out_rule, context=context)
            if not result.success:
                raise NetworkError(
                    f"Failed to add FORWARD out rule for {bridge} via {gateway_iface}: {result.error_message}"
                )

            # Forward in rule
            forward_in_rule = IPTablesRule(
                table_name=IPTablesTable.FILTER,
                chain_name=IPTablesChain.MVM_FORWARD,
                rule_type=IPTablesRuleType.FORWARD_IN,
                target=IPTablesTarget.ACCEPT,
                network_id=bridge,
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
            result = tracker.ensure_rule(forward_in_rule, context=context)
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
        from mvmctl.api._internal._resolvers._network_resolver import NetworkResolver

        effective_nat_gateways = nat_gateways
        effective_subnet = subnet

        if effective_nat_gateways is None or effective_subnet is None:
            resolver = NetworkResolver(db=self._db)
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

        tracker = IPTablesTracker(db=self._db)

        for gateway_iface in effective_nat_gateways:
            masquerade_rule = IPTablesRule(
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
            tracker.remove_rule(masquerade_rule)

            forward_out_rule = IPTablesRule(
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
            tracker.remove_rule(forward_out_rule)

            forward_in_rule = IPTablesRule(
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
            tracker.remove_rule(forward_in_rule)

        logger.info(
            "NAT rules removed for bridge %s via %s (source %s)",
            bridge,
            ", ".join(effective_nat_gateways),
            effective_subnet,
        )

    def tap_exists(self, tap: str) -> bool:
        """Return True if the TAP device exists."""
        result = subprocess.run(
            ["ip", "link", "show", tap],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        return result.returncode == 0

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
        tracker = IPTablesTracker(db=self._db)

        if self.tap_exists(tap):
            current_bridge = self._get_tap_bridge(tap)
            if current_bridge == bridge:
                logger.debug("TAP device %s already attached to bridge %s", tap, bridge)
            else:
                if current_bridge:
                    logger.warning(
                        "TAP device %s exists but attached to different bridge %s, reattaching to %s",
                        tap,
                        current_bridge,
                        bridge,
                    )
                    try:
                        self._run_ip_batch(
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
                        self._run_ip_batch(
                            [f"link set {tap} master {bridge}", f"link set {tap} up"]
                        )
                    except subprocess.CalledProcessError as e:
                        raise NetworkError(f"Failed to attach TAP {tap} to bridge {bridge}") from e
                logger.info("TAP device %s reattached to bridge %s", tap, bridge)
        else:
            try:
                self._run_ip_batch(
                    [
                        f"tuntap add dev {tap} mode tap",
                        f"link set {tap} master {bridge}",
                        f"link set {tap} up",
                    ]
                )
            except subprocess.CalledProcessError as e:
                raise NetworkError(f"Failed to create TAP {tap}") from e
            logger.info("TAP device %s created and attached to bridge %s", tap, bridge)

        self.initialize()

        forward_bridge_to_tap = IPTablesRule(
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
        result = tracker.ensure_rule(forward_bridge_to_tap, context=f"tap:{tap}")
        if not result.success:
            raise NetworkError(
                f"Failed to add FORWARD rule for bridge {bridge} to TAP {tap}: {result.error_message}"
            )

        forward_tap_to_bridge = IPTablesRule(
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
        result = tracker.ensure_rule(forward_tap_to_bridge, context=f"tap:{tap}")
        if not result.success:
            tracker.remove_rule(forward_bridge_to_tap)
            raise NetworkError(
                f"Failed to add FORWARD rule for TAP {tap} to bridge {bridge}: {result.error_message}"
            )

    def _get_tap_bridge(self, tap: str) -> str | None:
        """Get the bridge that a TAP device is attached to. Returns None if not attached."""
        try:
            result = subprocess.run(
                ["ip", "link", "show", tap],
                capture_output=True,
                text=True,
                check=True,
            )
            for line in result.stdout.splitlines():
                if "master" in line:
                    parts = line.split()
                    for i, part in enumerate(parts):
                        if part == "master" and i + 1 < len(parts):
                            return parts[i + 1]
        except subprocess.CalledProcessError:
            pass
        return None

    def remove_tap(self, tap: str, bridge: str | None = None) -> None:
        """Remove a TAP device and its iptables forwarding rules.

        Idempotent operation - safe to call multiple times.
        If TAP doesn't exist, does nothing.

        Args:
            tap: TAP device name to remove.
            bridge: Bridge name the TAP is attached to. If None, attempts to detect.
        """
        if not self.tap_exists(tap):
            logger.debug("TAP device %s does not exist, skipping removal", tap)
            return

        effective_bridge = bridge if bridge is not None else self._get_tap_bridge(tap)
        if effective_bridge is None:
            logger.warning("Could not determine bridge for TAP %s, skipping rule cleanup", tap)
        else:
            tracker = IPTablesTracker(db=self._db)

            forward_bridge_to_tap = IPTablesRule(
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
            tracker.remove_rule(forward_bridge_to_tap)

            forward_tap_to_bridge = IPTablesRule(
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
            tracker.remove_rule(forward_tap_to_bridge)

        try:
            self._run_ip_batch([f"link set {tap} down", f"link delete {tap}"])
        except subprocess.CalledProcessError as e:
            raise NetworkError(f"Failed to remove TAP {tap}") from e

        logger.info("TAP device %s removed", tap)

    def get_bridge_taps(self, bridge: str) -> list[str]:
        """List all TAP devices currently attached to the bridge."""
        result = subprocess.run(
            ["ip", "link", "show", "master", bridge],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        devices: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if parts and parts[0][0].isdigit() and len(parts) >= 2:
                iface = parts[1].rstrip(":")
                devices.append(iface)

        return devices

    def get_tuntap_devices(self) -> list[str]:
        """List all TUN/TAP devices."""
        result = subprocess.run(
            ["ip", "-o", "link", "show", "type", "tuntap"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        devices: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                devices.append(parts[1].rstrip(":"))
        return devices

    def get_bridges(self) -> list[str]:
        """List all bridge interfaces."""
        result = subprocess.run(
            ["ip", "-o", "link", "show", "type", "bridge"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return []

        bridges: list[str] = []
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) >= 2:
                bridges.append(parts[1].rstrip(":"))
        return bridges


__all__ = ["NetworkManager"]
