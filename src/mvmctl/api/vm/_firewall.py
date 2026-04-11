"""VM firewall and nocloud orchestration.

This module provides direct access to core firewall and nocloud functions.
The API layer should import and call these core functions directly instead of
using wrapper classes.

Direct core imports:
    from mvmctl.core.firewall import (
        setup_nocloud_input_chain,
        add_nocloud_input_rule,
        remove_nocloud_input_rule,
        cleanup_nocloud_input_rules,
    )
    from mvmctl.core.network import (
        add_iptables_forward_rules,
        remove_iptables_forward_rules,
        teardown_nat,
    )
"""

from __future__ import annotations

from pathlib import Path
from typing import Final

__all__: Final[list[str]] = ["NocloudManager"]


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
        import logging

        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        logger = logging.getLogger(__name__)
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
        import logging

        from mvmctl.services.nocloud_server.manager import NoCloudNetServerManager

        logger = logging.getLogger(__name__)
        try:
            manager = NoCloudNetServerManager()
            manager.cleanup_orphans()
        except Exception as exc:
            logger.debug("Failed to clean up orphaned nocloud servers: %s", exc)
