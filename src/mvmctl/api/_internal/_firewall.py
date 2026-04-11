"""Shared firewall and nocloud orchestration for API modules."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Final

__all__: Final[list[str]] = ["NocloudManager", "FirewallManager"]

logger: Final[logging.Logger] = logging.getLogger(__name__)


class NocloudManager:
    """Manages NoCloud network server lifecycle."""

    def start_server(
        self,
        vm_name: str,
        cloud_init_dir: Path,
        gateway: str,
        vm_id: str,
        preferred_port: int = 0,
    ) -> tuple[str, int]:
        """Start a nocloud server.

        Args:
            vm_name: Name of the VM.
            cloud_init_dir: Path to the cloud-init directory.
            gateway: Gateway IP address.
            vm_id: Unique VM identifier.
            preferred_port: Preferred port number (0 for auto-assign).

        Returns:
            Tuple of (url, port) for the started server.
        """
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        manager = NoCloudNetServerManager()
        url, port = manager.start_server(
            vm_name, cloud_init_dir, gateway, vm_id, preferred_port=preferred_port
        )
        return url, port

    def get_server_pid(self, vm_name: str, vm_id: str) -> int | None:
        """Get the PID of a nocloud server.

        Args:
            vm_name: Name of the VM.
            vm_id: Unique VM identifier.

        Returns:
            The server PID if found, None otherwise.
        """
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        manager = NoCloudNetServerManager()
        return manager.get_server_pid(vm_name, vm_id)

    def stop_server(self, vm_name: str, vm_id: str = "") -> None:
        """Stop a nocloud server. Logs warnings on failure.

        Args:
            vm_name: Name of the VM.
            vm_id: Unique VM identifier (optional).
        """
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        try:
            manager = NoCloudNetServerManager()
            if vm_id:
                manager.stop_server(vm_name, vm_id)
            else:
                manager.stop_server(vm_name)
        except (OSError, RuntimeError) as exc:
            logger.warning("Failed to stop nocloud server for %s: %s", vm_name, exc)

    def cleanup_orphans(self) -> None:
        """Clean up orphaned nocloud server processes."""
        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        try:
            manager = NoCloudNetServerManager()
            manager.cleanup_orphans()
        except Exception as exc:
            logger.debug("Failed to clean up orphaned nocloud servers: %s", exc)


class FirewallManager:
    """Manages firewall (iptables) rule lifecycle."""

    @staticmethod
    def ensure_nocloud_chain() -> None:
        """Ensure the nocloud iptables input chain exists (idempotent)."""
        from mvmctl.core.firewall import setup_nocloud_input_chain

        setup_nocloud_input_chain()

    @staticmethod
    def add_nocloud_rule(guest_ip: str, vm_name: str, port: int) -> None:
        """Add a nocloud iptables input rule.

        Args:
            guest_ip: IP address of the guest VM.
            vm_name: Name of the VM.
            port: Port number for the rule.
        """
        from mvmctl.core.firewall import add_nocloud_input_rule

        add_nocloud_input_rule(guest_ip, vm_name, port)

    @staticmethod
    def remove_nocloud_rule(guest_ip: str, vm_name: str, port: int) -> None:
        """Remove a nocloud iptables input rule. Logs warnings on failure.

        Args:
            guest_ip: IP address of the guest VM.
            vm_name: Name of the VM.
            port: Port number for the rule.
        """
        from mvmctl.core.firewall import remove_nocloud_input_rule
        from mvmctl.exceptions import NetworkError

        try:
            remove_nocloud_input_rule(guest_ip, vm_name, port)
        except NetworkError as exc:
            logger.warning("Failed to remove nocloud firewall rule: %s", exc)

    @staticmethod
    def add_forward_rules(tap_name: str, bridge: str) -> None:
        """Add iptables forward rules for a TAP device.

        Args:
            tap_name: Name of the TAP device.
            bridge: Name of the bridge interface.
        """
        from mvmctl.core.network import add_iptables_forward_rules

        add_iptables_forward_rules(tap_name, bridge=bridge)

    @staticmethod
    def remove_forward_rules(tap_name: str, bridge: str) -> None:
        """Remove iptables forward rules for a TAP device.

        Args:
            tap_name: Name of the TAP device.
            bridge: Name of the bridge interface.
        """
        from mvmctl.core.network import remove_iptables_forward_rules

        remove_iptables_forward_rules(tap_name, bridge=bridge)

    @staticmethod
    def teardown_nat(bridge: str, force: bool = False, subnet: str | None = None) -> None:
        """Tear down NAT for a bridge. Logs debug on NetworkError.

        Args:
            bridge: Name of the bridge interface.
            force: Whether to force teardown even if VMs are running.
            subnet: Optional subnet CIDR to use for teardown.
        """
        from mvmctl.core.network import teardown_nat
        from mvmctl.exceptions import NetworkError

        try:
            teardown_nat(bridge, force=force, subnet=subnet)
        except NetworkError as exc:
            logger.debug("NAT teardown for bridge %s: %s", bridge, exc)
