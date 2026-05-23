"""Network management system tests.

Covers: create, ls, inspect, rm, default, sync, --no-nat, --ipv4-gateway,
--nat-gateways, error paths, destructive removal, VM dependency, and
sync-after-reboot (bridge deletion recovery).

Structure (top-to-bottom):
  - Helper functions
  - Non-destructive / read-only classes
  - State-modifying but self-cleaning classes
  - Destructive classes (force-rm, VM-dependent operations, bridge deletion)
"""

from __future__ import annotations

import hashlib
import json
import subprocess
import uuid
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_network,
    pytest.mark.slow,
    pytest.mark.serial,
    pytest.mark.domain_network,
]


# ============================================================================
# Helpers
# ============================================================================


def _compute_bridge_name(name: str) -> str:
    """Replicate NetworkUtils.compute_bridge_name for test assertions.

    Bridge names are limited to 15 chars (IFNAMSIZ). If the full
    mvm-{name} exceeds 15, the name portion is truncated and a hash
    suffix is appended to preserve uniqueness.
    """
    raw = f"mvm-{name}"
    if len(raw) <= 15:
        return raw
    hash_len = 8
    prefix = "mvm-"
    max_name = 15 - len(prefix) - hash_len - 1
    name_truncated = name[:max_name]
    short_hash = hashlib.sha256(name.encode()).hexdigest()[:hash_len]
    return f"{prefix}{name_truncated}-{short_hash}"


def _get_firewall_backend(mvm_binary: str) -> str:
    """Determine the current firewall backend (nftables or iptables).

    Returns ``"nftables"`` as the default (the hardcoded default in
    ``constants.py``) when config returns ``(default)`` or when the
    config command fails.
    """
    backend_result = _run_mvm(
        mvm_binary,
        "config",
        "get",
        "settings",
        "firewall_backend",
        check=False,
    )
    if backend_result.returncode == 0 and backend_result.stdout.strip():
        stdout_clean = backend_result.stdout.strip()
        if "=" in stdout_clean:
            raw = stdout_clean.split("=")[-1].strip()
            if raw and raw != "(default)":
                return raw
    return "nftables"


def _assert_bridge_exists(bridge: str) -> None:
    """Assert a bridge interface exists via ``ip link show``."""
    result = subprocess.run(
        ["ip", "link", "show", bridge],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 0, (
        f"Bridge '{bridge}' should exist:\n{result.stderr}"
    )


def _assert_firewall_rules_contain_bridge(bridge: str, backend: str) -> None:
    """Assert the active firewall has rules referencing the bridge."""
    if backend == "nftables":
        result = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "filter", "MVM-FORWARD"],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.returncode == 0, (
            f"nft list MVM-FORWARD failed: {result.stderr}"
        )
        assert bridge in result.stdout, (
            f"Bridge '{bridge}' not found in nftables MVM-FORWARD:\n"
            f"{result.stdout}"
        )
    else:
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L"],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.returncode == 0
        assert bridge in result.stdout, (
            f"Bridge '{bridge}' not found in iptables nat table:\n"
            f"{result.stdout}"
        )


def _count_firewall_rules(backend: str, chain: str = "MVM-FORWARD") -> int:
    """Count rules in a firewall chain (nftables or iptables)."""
    if backend == "nftables":
        result = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "filter", chain],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return 0
        count = 0
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if not stripped:
                continue
            if stripped.startswith("table "):
                continue
            if stripped.startswith("chain "):
                continue
            if stripped.startswith("type "):
                continue
            if stripped in ("{", "}"):
                continue
            count += 1
        return count
    else:
        result = subprocess.run(
            ["sudo", "iptables", "-L", chain, "-n"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            return 0
        lines = [ln for ln in result.stdout.splitlines() if ln.strip()]
        # Subtract chain header row and column header row
        return max(0, len(lines) - 2)


def _assert_masquerade_rule(backend: str) -> None:
    """Assert MASQUERADE rule exists in MVM-POSTROUTING."""
    if backend == "nftables":
        result = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "nat", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            assert "masquerade" in result.stdout.lower(), (
                f"No MASQUERADE rule in MVM-POSTROUTING:\n{result.stdout}"
            )
    else:
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            assert "MASQUERADE" in result.stdout.upper(), (
                f"No MASQUERADE rule in MVM-POSTROUTING:\n{result.stdout}"
            )


def _assert_no_masquerade_rule(backend: str) -> None:
    """Assert no MASQUERADE rule exists in MVM-POSTROUTING (``--no-nat``
    mode).
    """
    if backend == "nftables":
        result = subprocess.run(
            ["sudo", "nft", "list", "chain", "ip", "nat", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            assert "masquerade" not in result.stdout.lower(), (
                "Unexpected MASQUERADE rule in MVM-POSTROUTING "
                f"(expected --no-nat to suppress it):\n{result.stdout}"
            )
    else:
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L", "MVM-POSTROUTING"],
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            assert "MASQUERADE" not in result.stdout.upper(), (
                "Unexpected MASQUERADE rule in MVM-POSTROUTING "
                f"(expected --no-nat to suppress it):\n{result.stdout}"
            )


# ============================================================================
# Tests
# ============================================================================


class TestNetworkLifecycle:
    """Test network CRUD operations.

    Order within class:
      1. Read-only / error-path tests (no resource creation)
      2. State-modifying but self-cleaning tests (create + cleanup)
      3. Destructive tests (rm, delete)
    """

    # ── Read-only / error-path tests (no resources needed) ──────────────

    @pytest.mark.serial
    def test_network_list_empty(self, mvm_binary):
        # Rationale: Only needs JSON parsing (free). No resources needed --
        # verifies empty list is valid.
        """network ls --json returns valid empty list when no networks exist."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        assert result.returncode == 0, (
            f"network ls --json failed: {result.stderr}"
        )
        networks = json.loads(result.stdout)
        assert isinstance(networks, list), (
            f"Expected list, got {type(networks).__name__}: {networks}"
        )
        if len(networks) == 0:
            return  # Empty state — ideal
        # If networks exist, validate entry structure
        for net in networks:
            assert isinstance(net.get("name"), str) and net["name"], (
                f"Expected non-empty name: {net}"
            )
            assert isinstance(net.get("id"), str) and net["id"], (
                f"Expected non-empty id: {net}"
            )

    def test_network_listing_and_verification(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture (shared, 5-10s). Verifies
        # network appears in JSON listing.
        """List networks and verify created network appears."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks: list[dict[str, Any]] = json.loads(result.stdout)
        matched = [n for n in networks if n.get("name") == module_network]
        assert len(matched) == 1, (
            f"Network '{module_network}' not found in JSON listing"
        )
        network = matched[0]
        assert isinstance(network.get("id"), str) and network["id"], (
            f"Expected non-empty id: {network}"
        )
        assert isinstance(network.get("subnet"), str) and network["subnet"], (
            f"Expected non-empty subnet: {network}"
        )

    def test_ip_rule_verification_iptables(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies iptables/nftables
        # rules reference the bridge (Option C).
        """Verify firewall rules were created for network (iptables or nftables)."""
        bridge = _compute_bridge_name(module_network)
        backend = _get_firewall_backend(mvm_binary)
        _assert_firewall_rules_contain_bridge(bridge, backend)

    def test_nat_gateway_configuration(self, module_network):
        # Rationale: Uses module_network fixture. Verifies bridge interface
        # exists via ip addr.
        """Verify bridge interface exists for created network."""
        bridge = _compute_bridge_name(module_network)
        _assert_bridge_exists(bridge)

    def test_network_inspect(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON fields (name,
        # subnet, bridge) — was originally checking table output, now uses --json.
        """Inspect a network and verify JSON fields."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network, "--json"
        )
        data: dict[str, Any] = json.loads(result.stdout)
        network_info = data.get("network", {})
        assert network_info.get("name") == module_network, (
            f"Expected name '{module_network}', got '{network_info.get('name')}'"
        )
        assert isinstance(
            network_info.get("subnet"), str
        ) and "/" in network_info.get("subnet", ""), (
            f"Expected CIDR format subnet, got: {network_info.get('subnet')}"
        )
        assert (
            isinstance(network_info.get("bridge"), str)
            and network_info["bridge"]
        ), f"Expected non-empty bridge name, got: {network_info.get('bridge')}"

    def test_network_inspect_json(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies --json output
        # has expected fields.
        """Inspect a network with --json and verify parsed fields."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network, "--json"
        )
        assert result.returncode == 0
        data: dict[str, Any] = json.loads(result.stdout)
        assert "network" in data
        network_info = data["network"]
        assert "name" in network_info
        assert "subnet" in network_info
        assert "bridge" in network_info

    def test_network_ls_structure(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON field
        # structure in listing.
        """Verify network ls --json returns a list with well-formed entries.

        Every entry must have non-empty name, id, and subnet fields.
        """
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            for entry in data:
                assert isinstance(entry.get("name"), str) and entry["name"], (
                    f"Expected non-empty name: {entry}"
                )
                assert isinstance(entry.get("id"), str) and entry["id"], (
                    f"Expected non-empty id: {entry}"
                )
                assert (
                    isinstance(entry.get("subnet"), str) and entry["subnet"]
                ), f"Expected non-empty subnet: {entry}"

    def test_network_list_json(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies network appears
        # in JSON listing.
        """List networks in JSON format."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(n["name"] == module_network for n in data)

    def test_network_default_without_name(self, mvm_binary):
        # Rationale: No resources needed -- error path for missing name
        # argument.
        """Calling network default without a name should show guidance."""
        result = _run_mvm(mvm_binary, "network", "default", check=False)
        assert result.returncode != 0
        assert "Usage" in result.stdout or "Usage" in result.stderr, (
            f"Expected usage/help message, got: "
            f"{result.stdout} / {result.stderr}"
        )

    def test_network_remove_nonexistent(self, mvm_binary):
        # Rationale: No resources needed -- error path for nonexistent network.
        """Removing a non-existent network returns error."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "rm",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "not found" in result.stdout.lower()
            or "not found" in result.stderr.lower()
        )

    def test_set_default_nonexistent_network_fails(self, mvm_binary):
        # Rationale: No resources needed -- error path for nonexistent network.
        """Setting a nonexistent network as default should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "default",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        assert "not found" in result.stderr.lower()

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )

    def test_network_create_with_invalid_cidr_format(self, mvm_binary):
        # Rationale: No resources needed -- error path for invalid CIDR.
        """Create network with an invalid CIDR string should be rejected."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            "not-a-cidr",
            "--non-interactive",
            check=False,
        )
        assert result.returncode != 0
        assert "expected 4 octets" in result.stderr.lower()

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(n.get("name") == net_name for n in networks)

    # ── State-modifying tests (create + cleanup) ───────────────────────

    def test_network_create_with_generated_subnet(
        # Rationale: Needs a real network (5-10s). Tests basic creation with
        # generated subnet.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create network with a dynamically generated unique subnet (verify via --json)."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0
            # Verify via JSON listing
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            assert any(n["name"] == unique_network_name for n in networks)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_custom_cidr(
        # Rationale: Needs a real network (5-10s). Tests custom CIDR creation.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create network with custom CIDR (verify via --json)."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0
            # Verify via JSON listing
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            assert any(n["name"] == unique_network_name for n in networks)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_no_nat(self, mvm_binary, unique_network_name):
        # Rationale: Needs a real network (5-10s). Tests --no-nat flag.
        # Upgraded to L3: verifies bridge exists and no MASQUERADE rule.
        """Create a network with --no-nat flag.

        L2: Checks JSON listing for the network.
        L3: Verifies bridge exists but has NO MASQUERADE firewall rule.
        """
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                _unique_subnet(unique_network_name),
                "--no-nat",
            )
            assert result.returncode == 0

            # L2: Verify network appears in JSON listing
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            assert any(n.get("name") == unique_network_name for n in networks)

            # L3: Verify bridge exists and --no-nat was honored
            bridge = _compute_bridge_name(unique_network_name)
            _assert_bridge_exists(bridge)
            inspect_result = _run_mvm(
                mvm_binary, "network", "inspect", unique_network_name, "--json"
            )
            inspect_data = json.loads(inspect_result.stdout)
            assert inspect_data.get("nat", {}).get("nat_enabled") is False, (
                f"Expected nat_enabled=False for --no-nat network, "
                f"got: {inspect_data.get('nat', {}).get('nat_enabled')}"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_set_default(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Tests setting/restoring
        # default network.
        """Set a network as the default."""
        # Save original default network before changing
        ls_before = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        original_default = None
        if ls_before.returncode == 0 and ls_before.stdout.strip():
            nets_before = json.loads(ls_before.stdout)
            orig = next((n for n in nets_before if n.get("is_default")), None)
            if orig:
                original_default = orig["name"]

        try:
            result = _run_mvm(mvm_binary, "network", "default", module_network)
            assert result.returncode == 0
            # Verify via JSON listing that the network is now default
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            default = next(
                (n for n in networks if n.get("name") == module_network),
                None,
            )
            assert default is not None
            assert default.get("is_default", False)
        finally:
            # Restore original default so subsequent tests aren't broken
            if original_default:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    original_default,
                    check=False,
                )

    def test_network_create_with_default_flag(
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create a network with --default flag and verify is_default=true in ls JSON.

        Rationale: The --default flag on network create sets the new network as
        the default for VM creation. A regression where --default is silently
        ignored would leave the previous default (or no default) in place.
        """
        subnet = _unique_subnet(unique_network_name)
        # Save original default to restore later
        ls_before = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        original_default = None
        if ls_before.returncode == 0 and ls_before.stdout.strip():
            nets_before = json.loads(ls_before.stdout)
            orig = next((n for n in nets_before if n.get("is_default")), None)
            if orig:
                original_default = orig["name"]

        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--default",
                "--non-interactive",
            )
            assert result.returncode == 0

            # L2: Verify is_default=true in ls JSON
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            new_net = next(
                (n for n in networks if n.get("name") == unique_network_name),
                None,
            )
            assert new_net is not None, (
                f"Network '{unique_network_name}' not found in listing"
            )
            assert new_net.get("is_default") is True, (
                f"Network '{unique_network_name}' should have is_default=True "
                f"after create --default"
            )
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )
            # Restore original default
            if original_default:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    original_default,
                    check=False,
                )

    # ── Error path create + cleanup ────────────────────────────────────

    def test_overlapping_subnet_across_networks_rejected(self, mvm_binary):
        # Rationale: Needs a real network. Tests duplicate subnet rejection.
        """Two networks with the same subnet should be rejected at create time."""
        net_a = f"sys-edge-{uuid.uuid4().hex[:6]}"
        net_b = f"sys-edge-{uuid.uuid4().hex[:6]}"
        shared_subnet = "10.251.0.0/24"

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_a,
                "--subnet",
                shared_subnet,
                "--non-interactive",
            )

            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_b,
                "--subnet",
                shared_subnet,
                "--non-interactive",
                check=False,
            )
            assert result.returncode != 0
            assert "overlap" in result.stderr.lower()

            ls_result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = json.loads(ls_result.stdout)
                same_subnet = [
                    n for n in networks if n.get("subnet") == shared_subnet
                ]
                assert len(same_subnet) == 1
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_a, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_b, check=False)

    def test_network_create_with_slash_32_subnet(self, mvm_binary):
        # Rationale: Needs one network attempt. Tests /32 rejection.
        """Create network with /32 subnet should be rejected (too small)."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"

        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                "10.252.0.0/32",
                "--non-interactive",
                check=False,
            )
            assert result.returncode != 0
            assert "invalid" in result.stderr.lower()

            ls_result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = json.loads(ls_result.stdout)
                assert not any(n.get("name") == net_name for n in networks)
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_network_create_with_same_name(self, mvm_binary):
        # Rationale: Needs a real network. Tests duplicate name rejection.
        """Two networks with the same name should be rejected."""
        net_name = f"sys-edge-{uuid.uuid4().hex[:6]}"

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )

            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(f"{net_name}-other"),
                "--non-interactive",
                check=False,
            )
            assert result.returncode != 0
            assert "already exists" in result.stderr.lower()

            ls_result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = json.loads(ls_result.stdout)
                same_name = [n for n in networks if n.get("name") == net_name]
                assert len(same_name) == 1
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    @pytest.mark.requires_network
    def test_overlapping_subnet_rejected(self, mvm_binary: str) -> None:
        # Rationale: Needs real networks. Tests overlapping subnet rejection.
        """Creating a second network with the same subnet CIDR should fail."""
        net_a = f"sys-ovlap-a-{uuid.uuid4().hex[:6]}"
        net_b = f"sys-ovlap-b-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_a)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_a,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_b,
                "--subnet",
                subnet,
                "--non-interactive",
                check=False,
            )
            assert result.returncode != 0
            assert "overlap" in result.stderr.lower()
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_a, check=False)

    # ── Destructive tests (remove / delete) ────────────────────────────

    def test_network_deletion_and_cleanup(
        # Rationale: Needs a real network. Tests create, verify, delete,
        # verify gone.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create and delete network, verify cleanup."""
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            _unique_subnet(unique_network_name),
            "--non-interactive",
        )

        try:
            result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks_before = json.loads(result.stdout)
            assert any(
                n.get("name") == unique_network_name for n in networks_before
            )

            result = _run_mvm(mvm_binary, "network", "rm", unique_network_name)
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(n.get("name") == unique_network_name for n in networks)

    def test_network_remove_multiple(self, mvm_binary, unique_network_name):
        # Rationale: Needs two real networks. Tests multi-rm.
        # Cleanup in finally.
        """Remove multiple networks at once."""
        name_a = f"{unique_network_name}-a"
        name_b = f"{unique_network_name}-b"

        _run_mvm(
            mvm_binary,
            "network",
            "create",
            name_a,
            "--subnet",
            _unique_subnet(name_a),
            "--non-interactive",
        )
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            name_b,
            "--subnet",
            _unique_subnet(name_b),
            "--non-interactive",
        )

        try:
            result = _run_mvm(mvm_binary, "network", "rm", name_a, name_b)
            assert result.returncode == 0
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            assert not any(n.get("name") in (name_a, name_b) for n in networks)
        finally:
            _run_mvm(mvm_binary, "network", "rm", name_a, check=False)
            _run_mvm(mvm_binary, "network", "rm", name_b, check=False)


class TestNetworkInspectTree:
    """Test network inspect with default format output."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_tree(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies default inspect
        # output format.
        """Inspect a network and verify tree characters in default output."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network
        )
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout


class TestNetworkAdvancedCreate:
    """Test advanced network creation options."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_create_with_ipv4_gateway(
        # Rationale: Needs a real network (5-10s). Tests --ipv4-gateway flag.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create a network with explicit --ipv4-gateway."""
        subnet = _unique_subnet(unique_network_name)
        custom_gateway = subnet.replace(".0/24", ".100")
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--ipv4-gateway",
                custom_gateway,
                "--non-interactive",
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary,
                "network",
                "inspect",
                unique_network_name,
                "--json",
            )
            data: dict[str, Any] = json.loads(inspect.stdout)
            network_info = data.get("network", {})
            assert network_info.get("ipv4_gateway") == custom_gateway
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_nat_gateways(
        # Rationale: Needs a real network (5-10s). Tests --nat-gateways flag.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Create a network with explicit --nat-gateways."""
        subnet = _unique_subnet(unique_network_name)
        try:
            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--nat-gateways",
                "wlo1",
                "--non-interactive",
            )
            assert result.returncode == 0

            inspect = _run_mvm(
                mvm_binary,
                "network",
                "inspect",
                unique_network_name,
                "--json",
            )
            data: dict[str, Any] = json.loads(inspect.stdout)
            gateways = data.get("nat", {}).get("nat_gateways", [])
            assert "wlo1" in gateways
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_invalid_gateway_fails(
        # Rationale: Needs one network attempt. Tests invalid gateway rejection.
        self,
        mvm_binary,
        unique_network_name,
    ):
        """Creating a network with an invalid gateway should fail."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            unique_network_name,
            "--subnet",
            "10.99.99.0/24",
            "--ipv4-gateway",
            "not-an-ip",
            "--non-interactive",
            check=False,
        )
        assert result.returncode != 0
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)


class TestNetworkSync:
    """Test mvm network sync with atomicity and Option C verification.

    Non-destructive: all tests either create+cleanup their own networks or
    use the module_network fixture. This class is placed BEFORE destructive
    classes so idempotent tests run if file execution is interrupted.
    """

    def test_network_sync_all(self, mvm_binary):
        # Rationale: Creates own network inline. Verifies bridge + firewall
        # rules persist after sync (Option C). Self-contained to avoid stale
        # iptables rules from earlier tests in the module.
        """Sync all networks — verifies bridge and firewall rules persist."""
        net_name = f"sys-sync-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        backend = _get_firewall_backend(mvm_binary)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Read bridge name from inspect output (authoritative source)
            inspect = json.loads(
                _run_mvm(
                    mvm_binary, "network", "inspect", net_name, "--json"
                ).stdout
            )
            bridge: str = inspect.get("network", {}).get("bridge", "")

            result = _run_mvm(mvm_binary, "network", "sync", check=False)
            assert result.returncode == 0, f"sync failed: {result.stderr}"

            # Option C: bridge exists
            _assert_bridge_exists(bridge)

            # Option C: bridge has an IP address
            ip_result = subprocess.run(
                ["ip", "addr", "show", bridge],
                capture_output=True,
                text=True,
                check=False,
            )
            assert ip_result.returncode == 0
            assert "inet " in ip_result.stdout, (
                f"Bridge {bridge} has no IP after sync:\n{ip_result.stdout}"
            )

            # Option C: firewall rules reference the bridge
            _assert_firewall_rules_contain_bridge(bridge, backend)
        finally:
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                net_name,
                "--force",
                check=False,
            )

    def test_network_sync_specific(self, mvm_binary):
        # Rationale: Creates own network inline. Verifies sync by name.
        # Self-contained to avoid stale iptables rules from earlier tests.
        """Sync a specific network by name."""
        net_name = f"sys-sync-{uuid.uuid4().hex[:6]}"
        subnet = _unique_subnet(net_name)
        backend = _get_firewall_backend(mvm_binary)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Read bridge name from inspect output
            inspect = json.loads(
                _run_mvm(
                    mvm_binary, "network", "inspect", net_name, "--json"
                ).stdout
            )
            bridge: str = inspect.get("network", {}).get("bridge", "")

            result = _run_mvm(
                mvm_binary, "network", "sync", net_name, check=False
            )
            assert result.returncode == 0, (
                f"sync {net_name} failed: {result.stderr}"
            )

            # Option C: bridge and rules
            _assert_bridge_exists(bridge)
            _assert_firewall_rules_contain_bridge(bridge, backend)
        finally:
            _run_mvm(
                mvm_binary,
                "network",
                "rm",
                net_name,
                "--force",
                check=False,
            )

    def test_network_sync_json(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies JSON result
        # structure from sync.
        """Sync with JSON output — verify result structure has per-network stats."""
        result = _run_mvm(mvm_binary, "network", "sync", "--json", check=False)
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, dict), f"Expected dict, got {type(data)}"
        assert len(data) > 0, "Expected at least one network in sync results"
        for net_id, stats in data.items():
            assert isinstance(stats, dict), (
                f"Expected dict for network {net_id}"
            )
            assert "added" in stats, f"Missing 'added' in {net_id}"
            assert "verified" in stats, f"Missing 'verified' in {net_id}"
            assert "orphaned" in stats, f"Missing 'orphaned' in {net_id}"

    @pytest.mark.serial
    def test_network_sync_idempotent(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies no rule
        # duplication on second sync.
        """Sync twice — verify no rule duplication (atomicity check).

        batch_ensure_rules flushes MVM chains and re-adds all rules.
        Running sync twice must produce the same rule count.
        """
        backend = _get_firewall_backend(mvm_binary)

        # First sync — establish baseline
        _run_mvm(mvm_binary, "network", "sync")
        count_after_first = _count_firewall_rules(backend)

        # Second sync — should not add duplicates
        _run_mvm(mvm_binary, "network", "sync")
        count_after_second = _count_firewall_rules(backend)

        # Option C: rule count must not change
        assert count_after_second == count_after_first, (
            f"Rule count changed after second sync: "
            f"{count_after_first} -> {count_after_second}. "
            f"batch_ensure_rules flush+add should be idempotent."
        )

    @pytest.mark.serial
    def test_network_sync_conntrack_rule(self, mvm_binary, module_network):
        # Rationale: Uses module_network fixture. Verifies conntrack
        # established/related accept rule.
        """Sync ensures conntrack established/related accept rule exists."""
        backend = _get_firewall_backend(mvm_binary)

        _run_mvm(mvm_binary, "network", "sync")

        if backend == "nftables":
            result = subprocess.run(
                [
                    "sudo",
                    "nft",
                    "list",
                    "chain",
                    "ip",
                    "filter",
                    "MVM-FORWARD",
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            assert result.returncode == 0
            assert "established,related accept" in result.stdout, (
                "Missing conntrack established/related accept "
                f"in MVM-FORWARD:\n{result.stdout}"
            )
        else:
            result = subprocess.run(
                ["sudo", "iptables", "-L", "MVM-FORWARD", "-n"],
                capture_output=True,
                text=True,
                check=False,
            )
            assert result.returncode == 0
            assert "ESTABLISHED" in result.stdout.upper(), (
                "Missing ESTABLISHED,RELATED accept "
                f"in MVM-FORWARD:\n{result.stdout}"
            )

    def test_network_sync_on_nonexistent_network(self, mvm_binary):
        # Rationale: No resources needed -- error path for nonexistent
        # network sync.
        """Syncing a network that doesn't exist should fail gracefully."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "sync",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        assert "not found" in result.stderr.lower()

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )


class TestNetworkRemoveForce:
    """Test network removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_rm_with_force(self, mvm_binary, unique_network_name):
        # Rationale: Needs a real network. Tests --force removal.
        # Cleanup in finally. L3: verifies bridge is actually gone.
        """Create a network and remove it with --force, verify cleanup."""
        subnet = _unique_subnet(unique_network_name)
        bridge = _compute_bridge_name(unique_network_name)
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                unique_network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            result = _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, "--force"
            )
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks = json.loads(ls_result.stdout)
        assert not any(n.get("name") == unique_network_name for n in networks)

        # L3: Verify bridge interface is actually gone from the system
        # Rationale: A deletion that removes the DB entry but fails to
        # delete the bridge would leave a stale interface consuming
        # system resources. The `ip link show` check proves the bridge
        # was actually torn down by network rm --force.
        result = subprocess.run(
            ["ip", "link", "show", bridge],
            capture_output=True,
            text=True,
            check=False,
        )
        assert result.returncode != 0, (
            f"Bridge '{bridge}' should no longer exist after "
            f"network rm --force:\n{result.stdout}"
        )


class TestNetworkVMDependency:
    """Test network operations that depend on VM lifecycle."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_after_all_vms_removed(
        # Rationale: Needs a real VM (30-120s). Verifies network vm_count=0
        # after VM removal.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """After VM removal, network inspect should show zero VM count."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

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
            )

            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force")

            result = _run_mvm(
                mvm_binary, "network", "inspect", net_name, "--json"
            )
            data: list[dict[str, Any]] = json.loads(result.stdout)
            assert data.get("vm_count", 0) == 0
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_network_rm_with_active_vm_fails_without_force(
        # Rationale: Needs a real VM (30-120s). Tests network rm rejection
        # with active VMs.
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
    ):
        """Network with active VM must not be deletable."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)

        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

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
            )

            result = _run_mvm(
                mvm_binary, "network", "rm", net_name, check=False
            )
            assert result.returncode != 0
            assert "referenced by" in result.stderr.lower()

            result_net = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if result_net.returncode == 0:
                nets: list[dict[str, Any]] = json.loads(result_net.stdout)
                assert any(n["name"] == net_name for n in nets)

            result_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if result_vm.returncode == 0:
                vms: list[dict[str, Any]] = json.loads(result_vm.stdout)
                assert any(v["name"] == vm_name for v in vms)
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestNetworkSyncAfterReboot:
    """Test that ``network sync`` recreates missing infrastructure.

    Simulates a reboot-like scenario:
    1. Creates a network (bridge + NAT + firewall rules)
    2. Deletes the bridge directly via ``ip link delete``
    3. Runs ``mvm network sync``
    4. Verifies bridge, IP address, and firewall rules are restored

    This class is DESTRUCTIVE (deletes bridge mid-test) and is placed last
    in the file.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_sync_recreates_bridge_and_nat(
        # Rationale: Needs a real network (5-10s). Tests sync recreates
        # bridge after deletion.
        self,
        mvm_binary,
        unique_network_name,
    ) -> None:
        """Bridge, IP, and firewall rules are recreated after bridge deletion."""
        net_name = unique_network_name
        subnet = _unique_subnet(net_name)
        bridge = _compute_bridge_name(net_name)

        # 1. Create the network
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )

            # Verify bridge exists before removal
            _assert_bridge_exists(bridge)

            # 2. Delete the bridge (simulates reboot / accidental removal)
            result = subprocess.run(
                ["sudo", "ip", "link", "delete", bridge],
                capture_output=True,
                text=True,
                check=False,
            )
            assert result.returncode == 0, (
                f"Failed to delete bridge {bridge}: {result.stderr}"
            )

            # Verify bridge is really gone
            result = subprocess.run(
                ["ip", "link", "show", bridge],
                capture_output=True,
                text=True,
                check=False,
            )
            assert result.returncode != 0, (
                f"Bridge {bridge} should no longer exist"
            )

            # 3. Run sync — should recreate the bridge and re-add firewall rules
            sync_result = _run_mvm(mvm_binary, "network", "sync", check=False)
            assert sync_result.returncode == 0, (
                f"network sync failed: {sync_result.stderr}"
            )

            # 4. Verify the bridge was recreated
            _assert_bridge_exists(bridge)

            # 5. Verify bridge has an IP address assigned
            result = subprocess.run(
                ["ip", "addr", "show", bridge],
                capture_output=True,
                text=True,
                check=False,
            )
            assert result.returncode == 0
            gateway_ip = subnet.replace(".0/24", ".1")
            assert gateway_ip in result.stdout, (
                f"Bridge {bridge} missing IP {gateway_ip} after sync:\n"
                f"{result.stdout}"
            )

            # 6. Verify firewall rules and NAT (use module-level helpers)
            backend = _get_firewall_backend(mvm_binary)
            _assert_firewall_rules_contain_bridge(bridge, backend)
            _assert_masquerade_rule(backend)

        finally:
            # Cleanup
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)
