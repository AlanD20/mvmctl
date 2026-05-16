"""Network management system tests."""

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


class TestNetworkLifecycle:
    """Test network CRUD operations."""

    @pytest.mark.serial
    def test_network_list_empty(self, mvm_binary):
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

    def test_network_create_with_generated_subnet(
        self, mvm_binary, unique_network_name
    ):
        """Create network with a dynamically generated unique subnet."""
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
            assert unique_network_name in result.stdout
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_custom_cidr(
        self, mvm_binary, unique_network_name
    ):
        """Create network with custom CIDR."""
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
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_listing_and_verification(self, mvm_binary, module_network):
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
        """Verify firewall rules were created for network (iptables or nftables)."""
        bridge = _compute_bridge_name(module_network)

        # Determine current firewall backend
        backend_result = _run_mvm(
            mvm_binary,
            "config",
            "get",
            "settings",
            "firewall_backend",
            check=False,
        )
        backend = "nftables"  # hardcoded default in constants.py
        if backend_result.returncode == 0 and backend_result.stdout.strip():
            stdout_clean = backend_result.stdout.strip()
            # Parse "settings.firewall_backend = nftables" format
            if "=" in stdout_clean:
                raw = stdout_clean.split("=")[-1].strip()
                # "(default)" means the hardcoded default applies = nftables
                if raw and raw != "(default)":
                    backend = raw
            else:
                backend = stdout_clean

        if backend == "nftables":
            # In nftables mode, the bridge interface name appears in the
            # MVM-FORWARD chain (iifname/oifname match), not POSTROUTING.
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
            assert result.returncode == 0, (
                f"nft list chain MVM-FORWARD failed: {result.stderr}"
            )
            assert bridge in result.stdout, (
                f"Bridge '{bridge}' not found in nftables MVM-FORWARD chain:\n"
                f"{result.stdout}"
            )
        else:
            result = subprocess.run(
                ["sudo", "iptables", "-t", "nat", "-L"],
                capture_output=True,
                text=True,
            )
            assert result.returncode == 0
            assert bridge in result.stdout, (
                f"Bridge '{bridge}' not found in iptables nat table:\n"
                f"{result.stdout}"
            )

    def test_nat_gateway_configuration(self, module_network):
        """Verify bridge interface exists for created network."""
        bridge = _compute_bridge_name(module_network)
        result = subprocess.run(
            ["ip", "addr", "show"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        assert bridge in result.stdout

    def test_network_deletion_and_cleanup(
        self, mvm_binary, unique_network_name
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
            result = _run_mvm(mvm_binary, "network", "ls")
            assert unique_network_name in result.stdout

            result = _run_mvm(mvm_binary, "network", "rm", unique_network_name)
            assert result.returncode == 0
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        networks: list[dict[str, Any]] = json.loads(result.stdout)
        assert not any(n.get("name") == unique_network_name for n in networks)

    def test_network_inspect(self, mvm_binary, module_network):
        """Inspect a network and verify name appears in output."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network, "--json"
        )
        data: dict[str, Any] = json.loads(result.stdout)
        assert data.get("name") == module_network, (
            f"Expected name '{module_network}', got '{data.get('name')}'"
        )
        assert isinstance(data.get("subnet"), str) and "/" in data.get(
            "subnet", ""
        ), f"Expected CIDR format subnet, got: {data.get('subnet')}"
        assert isinstance(data.get("bridge"), str) and data["bridge"], (
            f"Expected non-empty bridge name, got: {data.get('bridge')}"
        )

    def test_network_inspect_json(self, mvm_binary, module_network):
        """Inspect a network with --json and verify parsed fields."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network, "--json"
        )
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert "name" in data
        assert "subnet" in data
        assert "bridge" in data

    def test_network_remove_nonexistent(self, mvm_binary):
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

    def test_network_create_no_nat(self, mvm_binary, unique_network_name):
        """Create a network with --no-nat flag."""
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
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            assert any(n.get("name") == unique_network_name for n in networks)
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_default_without_name(self, mvm_binary):
        """Calling network default without a name should show guidance."""
        result = _run_mvm(mvm_binary, "network", "default", check=False)
        assert result.returncode != 0
        assert "Usage" in result.stdout or "Usage" in result.stderr, (
            f"Expected usage/help message, got: {result.stdout} / {result.stderr}"
        )

    def test_network_ls_structure(self, mvm_binary, module_network):
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

    def test_network_set_default(self, mvm_binary, module_network):
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
            assert "default" in result.stdout.lower()
            ls_result = _run_mvm(mvm_binary, "network", "ls", "--json")
            networks = json.loads(ls_result.stdout)
            default = next(
                (n for n in networks if n.get("name") == module_network), None
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

    def test_network_list_json(self, mvm_binary, module_network):
        """List networks in JSON format."""
        result = _run_mvm(mvm_binary, "network", "ls", "--json")
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(n["name"] == module_network for n in data)

    def test_network_remove_multiple(self, mvm_binary, unique_network_name):
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

    def test_network_create_with_invalid_cidr_format(self, mvm_binary):
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
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["invalid", "cidr", "subnet"])

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(n.get("name") == net_name for n in networks)

    def test_overlapping_subnet_across_networks_rejected(self, mvm_binary):
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
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["overlap", "conflict", "exists"])

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
            combined = (result.stdout + result.stderr).lower()
            assert any(
                s in combined for s in ["invalid", "too small", "subnet"]
            )

            ls_result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = json.loads(ls_result.stdout)
                assert not any(n.get("name") == net_name for n in networks)
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_network_create_with_same_name(self, mvm_binary):
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
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["already exists", "duplicate"])

            ls_result = _run_mvm(
                mvm_binary, "network", "ls", "--json", check=False
            )
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                networks = json.loads(ls_result.stdout)
                same_name = [n for n in networks if n.get("name") == net_name]
                assert len(same_name) == 1
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_set_default_nonexistent_network_fails(self, mvm_binary):
        """Setting a nonexistent network as default should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "default",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "no such"])

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )

    @pytest.mark.requires_network
    def test_overlapping_subnet_rejected(self, mvm_binary: str) -> None:
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
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["overlap", "conflict", "exists"])
        finally:
            _run_mvm(mvm_binary, "network", "rm", net_a, check=False)


class TestNetworkSync:
    """Test mvm network sync command."""

    def test_network_sync_all(self, mvm_binary, module_network):
        """Sync all networks."""
        result = _run_mvm(mvm_binary, "network", "sync", check=False)
        assert result.returncode == 0

    def test_network_sync_specific(self, mvm_binary, module_network):
        """Sync a specific network by name."""
        result = _run_mvm(
            mvm_binary, "network", "sync", module_network, check=False
        )
        assert result.returncode == 0

    def test_network_sync_json(self, mvm_binary, module_network):
        """Sync with JSON output."""
        result = _run_mvm(mvm_binary, "network", "sync", "--json", check=False)
        assert result.returncode == 0
        data: list[dict[str, Any]] = json.loads(result.stdout)
        assert isinstance(data, dict)

    def test_network_sync_on_nonexistent_network(self, mvm_binary):
        """Syncing a network that doesn't exist should fail gracefully."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "sync",
            "totally-nonexistent-network",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "no such"])

        ls_result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if ls_result.returncode == 0 and ls_result.stdout.strip():
            networks = json.loads(ls_result.stdout)
            assert not any(
                n.get("name") == "totally-nonexistent-network" for n in networks
            )


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
        self, mvm_binary, unique_network_name
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
            data: list[dict[str, Any]] = json.loads(inspect.stdout)
            assert data.get("ipv4_gateway") == custom_gateway
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_with_nat_gateways(
        self, mvm_binary, unique_network_name
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
            data: list[dict[str, Any]] = json.loads(inspect.stdout)
            gateways = data.get("nat_gateways", [])
            assert "wlo1" in gateways
        finally:
            _run_mvm(
                mvm_binary, "network", "rm", unique_network_name, check=False
            )

    def test_network_create_invalid_gateway_fails(
        self, mvm_binary, unique_network_name
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


class TestNetworkInspectTree:
    """Test network inspect with --tree flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_inspect_tree(self, mvm_binary, module_network):
        """Inspect a network with --tree and verify tree characters in output."""
        result = _run_mvm(
            mvm_binary, "network", "inspect", module_network, "--tree"
        )
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout


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
        """Create a network and remove it with --force, verify cleanup."""
        subnet = _unique_subnet(unique_network_name)
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


class TestNetworkVMDependency:
    """Test network operations that depend on VM lifecycle."""

    pytestmark = [
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
    ]

    def test_network_inspect_after_all_vms_removed(
        self, mvm_binary, unique_vm_name, unique_network_name
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
        self, mvm_binary, unique_vm_name, unique_network_name
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
            combined = (result.stdout + result.stderr).lower()
            assert any(s in combined for s in ["in use", "active", "vm"])

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
