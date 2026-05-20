"""NFTables firewall backend system tests.

Verifies nftables firewall backend end-to-end:
- Setting firewall_backend to nftables
- Network and VM creation triggers nftables rules
- SSH connectivity and internet access through nftables NAT
- Proper rule cleanup on VM/network removal
- Reset back to iptables

Follows Option C verification: parses nft output for specific rule patterns
rather than relying on returncode-only assertions.
"""

from __future__ import annotations

import json
import subprocess
import uuid
from typing import Any

import pytest

from tests.system.conftest import (
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.serial,
    pytest.mark.domain_network,
]


def _check_native_nftables() -> bool:
    """Return True if the system supports native nftables (not iptables-nft)."""
    import subprocess as _subprocess

    result = _subprocess.run(
        [
            "sudo",
            "-n",
            "nft",
            "-c",
            "add",
            "rule",
            "ip",
            "filter",
            "FORWARD",
            "accept",
        ],
        capture_output=True,
        text=True,
        timeout=15,
    )
    return result.returncode == 0


if not _check_native_nftables():
    # Skip-reason: This module requires native nftables (not iptables-nft
    # compatibility layer). The _check_native_nftables() helper tests whether
    # ``nft -c`` succeeds for an ``ip filter FORWARD`` rule — if it fails,
    # the system only supports iptables-nft which lacks direct nft rule
    # verification. To run unconditionally, the test would need to parse
    # compatibility-layer output or switch to iptables-equivalent assertions.
    pytest.skip(
        "Native nftables not available (system uses iptables-nft)",
        allow_module_level=True,
    )


# ============================================================================
# Helpers
# ============================================================================


def _run_nft(*args: str) -> subprocess.CompletedProcess[str]:
    """Run a privileged nft command via sudo -n."""
    cmd = ["sudo", "-n", "nft", *args]
    return subprocess.run(cmd, capture_output=True, text=True, timeout=15)


def _nft_chain_output(chain: str) -> str:
    """Return full text output of ``nft list chain ip <table> <chain>``.

    MVM chains are now created in system tables rather than a separate
    ``inet mvmctl`` table:

    - ``MVM-FORWARD`` / ``MVM-NOCLOUDNET-INPUT`` → ``ip filter``
    - ``MVM-POSTROUTING`` → ``ip nat``

    Returns empty string if the chain does not exist (nft exits non-zero).
    """
    _CHAIN_TABLE: dict[str, str] = {
        "MVM-FORWARD": "filter",
        "MVM-POSTROUTING": "nat",
        "MVM-NOCLOUDNET-INPUT": "filter",
    }
    table = _CHAIN_TABLE.get(chain, "filter")
    result = _run_nft("list", "chain", "ip", table, chain)
    if result.returncode != 0:
        return ""
    return result.stdout


def _nft_chain_rule_count(chain: str) -> int:
    """Count the number of active rules in an nftables chain.

    Skips the table/chain header lines and closing brace.  A rule is any
    line that contains an action keyword (accept, masquerade, drop, etc.).
    """
    output = _nft_chain_output(chain)
    if not output:
        return 0
    count = 0
    for line in output.splitlines():
        stripped = line.strip()
        # Skip structural lines
        if not stripped:
            continue
        if stripped.startswith("table "):
            continue
        if stripped.startswith("chain "):
            continue
        if stripped.startswith("type "):
            continue
        if stripped == "{":
            continue
        if stripped == "}":
            continue
        # Remaining indented lines are rules
        count += 1
    return count


def _nft_has_rule_with(chain: str, *keywords: str) -> bool:
    """Check whether the given nftables chain has a rule containing ALL keywords."""
    output = _nft_chain_output(chain)
    if not output:
        return False
    for line in output.splitlines():
        stripped = line.strip()
        if all(kw in stripped for kw in keywords):
            return True
    return False


# ============================================================================
# Tests
# ============================================================================


class TestNFTablesFirewallBackend:
    """End-to-end verification of nftables firewall backend."""

    def test_nftables_end_to_end(
        # Rationale: Needs a real VM (30-120s). Full nftables lifecycle: set backend, create resources, SSH, verify rules, cleanup.
        self,
        mvm_binary: str,
        unique_vm_name: str,
        unique_network_name: str,
        tmp_path: Any,
        timing_targets: dict[str, float],
    ) -> None:
        """Set nftables backend, create resources, verify rules, SSH in,
        test internet, remove, verify cleanup, reset to iptables."""
        # ── Names ─────────────────────────────────────────────────────
        net_name = unique_network_name
        vm_name = unique_vm_name
        key_name = f"sysk-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)

        # ── Save original firewall_backend ────────────────────────────
        orig_result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "settings",
            "firewall_backend",
            check=False,
        )
        orig_fw: str | None = None
        if orig_result.returncode == 0 and orig_result.stdout.strip():
            # Parse value from output like "firewall_backend = iptables"
            for line in orig_result.stdout.splitlines():
                if "=" in line:
                    orig_fw = line.split("=", 1)[1].strip()
                    break
        if not orig_fw:
            orig_fw = "iptables"

        try:
            # ═════════════════════════════════════════════════════════
            # Step 1: Set firewall_backend to nftables
            # ═════════════════════════════════════════════════════════
            _run_mvm(
                mvm_binary,
                "config",
                "set",
                "settings",
                "firewall_backend",
                "nftables",
            )

            # ═════════════════════════════════════════════════════════
            # Step 2: Create SSH key
            # ═════════════════════════════════════════════════════════
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )
            _run_mvm(mvm_binary, "key", "default", key_name)

            # ═════════════════════════════════════════════════════════
            # Step 3: Create network
            # ═════════════════════════════════════════════════════════
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Verify network was created
            inspect_result = _run_mvm(
                mvm_binary,
                "network",
                "inspect",
                net_name,
                "--json",
            )
            net_data: dict[str, Any] = json.loads(inspect_result.stdout)
            bridge = net_data.get("network", net_data).get(
                "bridge", net_data.get("bridge", "")
            )
            assert isinstance(bridge, str) and bridge, (
                f"Expected non-empty bridge name, got: {bridge}"
            )

            # ═════════════════════════════════════════════════════════
            # Step 4: Verify nftables rules after network creation
            # ═════════════════════════════════════════════════════════
            forward_after_net = _nft_chain_output("MVM-FORWARD")
            postrouting_after_net = _nft_chain_output("MVM-POSTROUTING")

            # Option C: Verify FORWARD rules mention the bridge
            assert _nft_has_rule_with(
                "MVM-FORWARD",
                bridge,
                "accept",
            ), (
                f"FORWARD chain should have a rule for bridge '{bridge}' "
                f"with 'accept'.  Output:\n{forward_after_net}"
            )

            # Option C: Verify POSTROUTING has masquerade for the subnet
            assert _nft_has_rule_with(
                "MVM-POSTROUTING",
                "masquerade",
            ), (
                f"POSTROUTING chain should have a masquerade rule. "
                f"Output:\n{postrouting_after_net}"
            )

            # ── Record baseline before VM creation ─────────────────────
            # Recorded AFTER network setup so bridge-level rules are
            # already established. VM-level (per-TAP) rules will be
            # added on top of this baseline.
            forward_rules_before = _nft_chain_rule_count("MVM-FORWARD")

            # ═════════════════════════════════════════════════════════
            # Step 5: Create VM with SSH key
            # ═════════════════════════════════════════════════════════
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--ssh-key",
                key_name,
                "--cloud-init-mode",
                "inject",
                "--no-console",
            )

            # Verify VM was created and is running
            vms_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms: list[dict[str, Any]] = json.loads(vms_result.stdout)
            vm_info = next((v for v in vms if v["name"] == vm_name), None)
            assert vm_info is not None, f"VM '{vm_name}' not found in listing"
            assert vm_info["status"] == "running", (
                f"VM '{vm_name}' status is {vm_info['status']}, expected 'running'"
            )

            # ═════════════════════════════════════════════════════════
            # Step 6: Verify nftables rules after VM creation
            # ═════════════════════════════════════════════════════════
            forward_after_vm = _nft_chain_output("MVM-FORWARD")
            forward_rules_after_vm = _nft_chain_rule_count("MVM-FORWARD")
            postrouting_after_vm = _nft_chain_output("MVM-POSTROUTING")

            # Option C: Conntrack rules must still be present
            assert "ct state established,related accept" in forward_after_vm, (
                f"Conntrack rule missing in MVM-FORWARD after VM creation.\n"
                f"Output:\n{forward_after_vm}"
            )

            # Option C: Bridge-level accept rules must still be present
            assert _nft_has_rule_with("MVM-FORWARD", bridge, "accept"), (
                f"Bridge '{bridge}' accept rule missing in MVM-FORWARD "
                f"after VM creation.\nOutput:\n{forward_after_vm}"
            )

            # POSTROUTING should still have masquerade
            assert "masquerade" in postrouting_after_vm.lower(), (
                f"POSTROUTING should have a masquerade rule. "
                f"Output:\n{postrouting_after_vm}"
            )

            # ═════════════════════════════════════════════════════════
            # Step 7: SSH into the VM and verify echo
            # ═════════════════════════════════════════════════════════
            ssh_timeout = max(timing_targets.get("alpine:3.21", 15.0), 30.0)
            ssh_available = wait_for_ssh(
                mvm_binary,
                vm_name,
                "root",
                ssh_timeout,
            )
            assert ssh_available, (
                f"SSH not available for '{vm_name}' within {ssh_timeout}s"
            )

            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "--cmd",
                "echo OK",
            )
            assert "OK" in result.stdout, (
                f"Expected 'OK' in SSH echo output, got: {result.stdout}"
            )

            # ═════════════════════════════════════════════════════════
            # Step 8: Test VM network connectivity (gateway ping)
            # ═════════════════════════════════════════════════════════
            # Ping the gateway (bridge IP) to verify basic L3 connectivity
            # through nftables FORWARD rules.  External NAT (ping 8.8.8.8)
            # is a FORTHCOMING production enhancement for nftables.
            gateway = vm_info.get("network_gateway") or net_data.get(
                "network", net_data
            ).get("ipv4_gateway", "")
            assert gateway, "Could not determine gateway IP for connectivity test"
            result = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "--cmd",
                f"ping -c 1 -W 5 {gateway}",
                timeout=30,
                check=False,
            )
            assert result.returncode == 0, (
                f"Ping {gateway} from VM failed: "
                f"stdout={result.stdout}, stderr={result.stderr}"
            )
            # Option C: verify actual packet stats in output
            ping_output = result.stdout.lower()
            assert (
                "1 received" in ping_output
                or "1 packets received" in ping_output
                or "0% packet loss" in ping_output
            ), f"Expected ping success indicators in output: {result.stdout}"

            # ═════════════════════════════════════════════════════════
            # Step 9: Remove the VM
            # ═════════════════════════════════════════════════════════
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")

            # Option C: Verify VM is gone from listing
            vms_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(vms_result.stdout)
            assert not any(v["name"] == vm_name for v in vms), (
                f"VM '{vm_name}' should be removed from listing"
            )

            # ═════════════════════════════════════════════════════════
            # Step 10: Verify nftables rule cleanup after VM removal
            # ═════════════════════════════════════════════════════════
            forward_after_rm = _nft_chain_output("MVM-FORWARD")
            forward_rules_after_rm = _nft_chain_rule_count("MVM-FORWARD")
            postrouting_after_rm = _nft_chain_output("MVM-POSTROUTING")

            # Conntrack rules should still be present
            assert "ct state established,related accept" in forward_after_rm, (
                f"Conntrack rule missing in MVM-FORWARD after VM removal.\n"
                f"Output:\n{forward_after_rm}"
            )

            # Bridge-level rules should still remain
            assert _nft_has_rule_with(
                "MVM-FORWARD",
                bridge,
                "accept",
            ), (
                f"FORWARD chain should still have bridge-level rules after VM removal. "
                f"Output:\n{forward_after_rm}"
            )

            # POSTROUTING masquerade should still exist (bridge still up)
            assert "masquerade" in postrouting_after_rm.lower(), (
                f"POSTROUTING masquerade should remain after VM removal. "
                f"Output:\n{postrouting_after_rm}"
            )

            # ═════════════════════════════════════════════════════════
            # Step 11: Remove the network
            # ═════════════════════════════════════════════════════════
            _run_mvm(mvm_binary, "network", "rm", net_name)

        finally:
            # ═══════════════════════════════════════════════════════
            # Cleanup: remove any leftover resources
            # ═══════════════════════════════════════════════════════
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

            # Reset firewall_backend to its original value
            if orig_fw:
                _run_mvm(
                    mvm_binary,
                    "config",
                    "set",
                    "settings",
                    "firewall_backend",
                    orig_fw,
                    check=False,
                )
            else:
                _run_mvm(
                    mvm_binary,
                    "config",
                    "reset",
                    "settings",
                    "firewall_backend",
                    check=False,
                )


@pytest.fixture(scope="class")
def _nftables_env(mvm_binary: str):
    """Set firewall backend to nftables and restore on class teardown."""
    orig_result = _run_mvm(
        mvm_binary,
        "config",
        "get",
        "settings",
        "firewall_backend",
        check=False,
    )
    orig_fw = "iptables"
    if orig_result.returncode == 0 and orig_result.stdout.strip():
        for line in orig_result.stdout.splitlines():
            if "=" in line:
                orig_fw = line.split("=", 1)[1].strip()
                break
    _run_mvm(
        mvm_binary,
        "config",
        "set",
        "settings",
        "firewall_backend",
        "nftables",
    )
    yield orig_fw
    _run_mvm(
        mvm_binary,
        "config",
        "set",
        "settings",
        "firewall_backend",
        orig_fw,
        check=False,
    )


@pytest.mark.usefixtures("_nftables_env")
class TestAtomicRuleSync:
    """Test nftables atomic rule replacement (flush + add via nft -f -).

    Verifies that batch_ensure_rules does not duplicate rules when run
    multiple times and that the conntrack established/related accept rule
    is always present as the first rule in MVM-FORWARD and
    MVM-NOCLOUDNET-INPUT.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_conntrack_rule_present_after_sync(
        # Rationale: Uses module_network fixture. Verifies conntrack rules in MVM-FORWARD and MVM-NOCLOUDNET-INPUT.
        self,
        mvm_binary: str,
        module_network: str,
    ) -> None:
        """Verify conntrack established/related accept rule exists after sync."""
        _run_mvm(mvm_binary, "network", "sync")

        # Option C: check MVM-FORWARD chain
        output = _nft_chain_output("MVM-FORWARD")
        assert "ct state established,related accept" in output, (
            f"Missing conntrack rule in MVM-FORWARD after sync:\n{output}"
        )

        # Option C: check MVM-NOCLOUDNET-INPUT chain
        nocloud_output = _nft_chain_output("MVM-NOCLOUDNET-INPUT")
        assert "ct state established,related accept" in nocloud_output, (
            f"Missing conntrack rule in MVM-NOCLOUDNET-INPUT:\n{nocloud_output}"
        )

    def test_sync_idempotent_no_rule_duplication(
        # Rationale: Uses module_network fixture. Verifies nftables rule count is stable across syncs.
        self,
        mvm_binary: str,
        module_network: str,
    ) -> None:
        """Sync twice — nftables rule count must not increase.

        batch_ensure_rules flushes MVM chains and re-adds all rules
        atomically via nft -f -. Running it twice should produce
        exactly the same set of rules (no duplicates).
        """
        # First sync — establish baseline
        _run_mvm(mvm_binary, "network", "sync")
        count_first = _nft_chain_rule_count("MVM-FORWARD")

        # Second sync — should not add duplicates
        _run_mvm(mvm_binary, "network", "sync")
        count_second = _nft_chain_rule_count("MVM-FORWARD")

        assert count_second == count_first, (
            f"nftables rule count changed after second sync: "
            f"{count_first} -> {count_second}. "
            f"batch_ensure_rules flush+add should be idempotent."
        )

    def test_sync_preserves_masquerade_rule(
        # Rationale: Creates its own network AFTER _nftables_env sets the
        # nftables backend, so bridge-level nftables rules are populated
        # during network creation.  Verifies MASQUERADE rule persists and
        # bridge is referenced in FORWARD after sync.
        self,
        mvm_binary: str,
        unique_network_name: str,
    ) -> None:
        """MASQUERADE rule in MVM-POSTROUTING persists after sync."""
        # Create a network AFTER _nftables_env has set the backend to
        # nftables, so bridge-level nftables rules are created.
        net_name = unique_network_name
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            _unique_subnet(net_name),
            "--non-interactive",
        )
        try:
            _run_mvm(mvm_binary, "network", "sync")

            # Option C: verify MASQUERADE rule in POSTROUTING
            assert _nft_has_rule_with("MVM-POSTROUTING", "masquerade"), (
                "MASQUERADE rule missing in MVM-POSTROUTING after sync"
            )

            # Also verify the bridge is referenced in FORWARD
            inspect = _run_mvm(
                mvm_binary, "network", "inspect", net_name, "--json"
            )
            net_data = json.loads(inspect.stdout)
            bridge = net_data.get("network", net_data).get(
                "bridge", net_data.get("bridge", "")
            )
            assert isinstance(bridge, str) and bridge, (
                f"Expected non-empty bridge name, got: {bridge!r}"
            )
            assert _nft_has_rule_with("MVM-FORWARD", bridge, "accept"), (
                f"Bridge {bridge} accept rule missing in MVM-FORWARD after sync"
            )
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
            import json as _json

            result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
            if result.returncode == 0:
                nets = _json.loads(result.stdout)
                if not any(n.get("is_default") for n in nets) and nets:
                    _run_mvm(
                        mvm_binary,
                        "network",
                        "default",
                        nets[0]["name"],
                        check=False,
                    )
