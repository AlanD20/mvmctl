"""Invariant enforcement system tests.

Verifies business rules that must always hold true,
regardless of which operation was performed.
"""

from __future__ import annotations

import json
import uuid
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system, pytest.mark.domain_invariant]


class TestAtMostOneDefaultImage:
    """No two images can be the default simultaneously."""

    def test_at_most_one_default_image(self, mvm_binary) -> None:
        """Pull two images with --default and verify exactly one default at a time."""
        # ── 1. Pull first image with --default ──────────────────────────
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine-3.21",
            "--default",
            timeout=120,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"alpine-3.21 pull failed: {result.stderr.strip()}")

        # Verify exactly one default
        images = _present_images(mvm_binary)
        first_defaults = [i for i in images if i.get("is_default")]
        assert len(first_defaults) == 1, (
            f"Expected exactly 1 default image after first pull, "
            f"got {len(first_defaults)}"
        )
        first_default_id = first_defaults[0]["id"]

        # ── 2. Pull a different image with --default ────────────────────
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "ubuntu-24.04-minimal",
            "--default",
            timeout=120,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"ubuntu-24.04-minimal pull failed: {result.stderr.strip()}"
            )

        # Verify exactly one default — must be the second image
        images = _present_images(mvm_binary)
        second_defaults = [i for i in images if i.get("is_default")]
        assert len(second_defaults) == 1, (
            f"Expected exactly 1 default image after second pull, "
            f"got {len(second_defaults)}"
        )
        assert second_defaults[0]["id"] != first_default_id, (
            "Default did not switch to the second image"
        )

        # ── 3. Restore: set first image as default again ────────────────
        _run_mvm(mvm_binary, "image", "default", first_default_id, check=False)
        restored = [
            i for i in _present_images(mvm_binary) if i.get("is_default")
        ]
        assert len(restored) == 1, (
            f"Expected exactly 1 default after restore, got {len(restored)}"
        )


class TestAtMostOneDefaultKernel:
    """No two kernels can be the default simultaneously."""

    def test_at_most_one_default_kernel(self, mvm_binary) -> None:
        """Set different kernels as default and verify exactly one default."""
        # ── 1. List present kernels ────────────────────────────────────
        present = _present_kernels(mvm_binary)
        if len(present) < 2:
            pytest.skip("Need at least 2 present kernels for this test")

        defaults = [k for k in present if k.get("is_default")]
        original_default_id: str | None = (
            defaults[0]["id"] if defaults else None
        )

        # Pick first kernel that is NOT default
        non_defaults = [k for k in present if not k.get("is_default")]
        if not non_defaults:
            pytest.skip("All present kernels are already default")

        first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        # ── 2. Set first non-default kernel as default ─────────────────
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "default",
            first_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set kernel {first_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        # Verify exactly one default
        present = _present_kernels(mvm_binary)
        first_round = [k for k in present if k.get("is_default")]
        assert len(first_round) == 1, (
            f"Expected exactly 1 default kernel after first set, "
            f"got {len(first_round)}"
        )
        assert first_round[0]["id"] == first_target["id"], (
            "Unexpected kernel became default"
        )

        # ── 3. Pick a different present kernel, set it as default ──────
        # Re-read present kernels
        present = _present_kernels(mvm_binary)
        other_non_defaults = [k for k in present if not k.get("is_default")]
        if not other_non_defaults:
            pytest.skip("No other kernel to set as default")

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]

        result = _run_mvm(
            mvm_binary,
            "kernel",
            "default",
            second_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set kernel {second_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        # Verify exactly one default, it's the second
        present = _present_kernels(mvm_binary)
        second_round = [k for k in present if k.get("is_default")]
        assert len(second_round) == 1, (
            f"Expected exactly 1 default kernel after second set, "
            f"got {len(second_round)}"
        )
        assert second_round[0]["id"] == second_target["id"], (
            "Second kernel did not become the sole default"
        )

        # ── 4. Restore original default ─────────────────────────────────
        if original_default_id:
            _run_mvm(
                mvm_binary,
                "kernel",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultBinary:
    """No two binaries can be the default simultaneously."""

    def test_at_most_one_default_binary(self, mvm_binary) -> None:
        """Set different binaries as default and verify exactly one default."""
        # ── 1. List cached binaries ────────────────────────────────────
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        if not binaries:
            pytest.skip("No cached binaries available")
        if not any(b.get("is_default") for b in binaries):
            # Ensure at least one binary is set as default
            _run_mvm(
                mvm_binary, "bin", "default", binaries[0]["id"][:6], check=False
            )

        # Re-read after potential default set
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        binaries = json.loads(result.stdout)
        defaults = [b for b in binaries if b.get("is_default")]
        original_default_id: str | None = (
            defaults[0]["id"] if defaults else None
        )

        # ── 2. Pick a non-default binary ────────────────────────────────
        non_defaults = [b for b in binaries if not b.get("is_default")]
        if not non_defaults:
            pytest.skip("All cached binaries are already default")

        first_target = non_defaults[0]
        first_target_prefix = first_target["id"][:6]

        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            first_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set binary {first_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        # Verify exactly one default
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        first_round = [
            b for b in json.loads(result.stdout) if b.get("is_default")
        ]
        assert len(first_round) == 1, (
            f"Expected exactly 1 default binary after first set, "
            f"got {len(first_round)}"
        )

        # ── 3. Pick another non-default binary ──────────────────────────
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        other_non_defaults = [
            b for b in json.loads(result.stdout) if not b.get("is_default")
        ]
        if not other_non_defaults:
            pytest.skip("No other binary to set as default")

        second_target = other_non_defaults[0]
        second_target_prefix = second_target["id"][:6]
        second_target_id = second_target["id"]

        result = _run_mvm(
            mvm_binary,
            "bin",
            "default",
            second_target_prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set binary {second_target_prefix} as default: "
                f"{result.stderr.strip()}"
            )

        # Verify exactly one default, it's the second
        result = _run_mvm(mvm_binary, "bin", "ls", "--json")
        second_round = [
            b for b in json.loads(result.stdout) if b.get("is_default")
        ]
        assert len(second_round) == 1, (
            f"Expected exactly 1 default binary after second set, "
            f"got {len(second_round)}"
        )
        assert second_round[0]["id"] == second_target_id, (
            "Second binary did not become the sole default"
        )

        # ── 4. Restore original default ─────────────────────────────────
        if original_default_id:
            _run_mvm(
                mvm_binary,
                "bin",
                "default",
                original_default_id,
                check=False,
            )


class TestAtMostOneDefaultNetwork:
    """No two networks can be the default simultaneously."""

    @pytest.mark.requires_network
    def test_at_most_one_default_network(self, mvm_binary) -> None:
        """Create two networks, set each as default, verify exactly one default."""
        net_a_name = f"sys-inv-net-a-{uuid.uuid4().hex[:6]}"
        net_b_name = f"sys-inv-net-b-{uuid.uuid4().hex[:6]}"

        # Save the original default so we can restore it in cleanup
        original_default_id: str | None = None
        try:
            result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
            if result.returncode == 0:
                nets = json.loads(result.stdout)
                orig = [n for n in nets if n.get("is_default")]
                if orig:
                    original_default_id = orig[0]["id"]
        except Exception:
            pass

        try:
            # ── 1. Create network A and set as default ────────────────
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_a_name,
                "--subnet",
                _unique_subnet(net_a_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "network", "default", net_a_name)

            # Verify exactly one default
            networks = _all_networks(mvm_binary)
            first_defaults = [n for n in networks if n.get("is_default")]
            assert len(first_defaults) == 1, (
                f"Expected exactly 1 default network after first set, "
                f"got {len(first_defaults)}"
            )
            assert first_defaults[0]["name"] == net_a_name

            # ── 2. Create network B and set IT as default ──────────────
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_b_name,
                "--subnet",
                _unique_subnet(net_b_name),
                "--non-interactive",
            )
            _run_mvm(mvm_binary, "network", "default", net_b_name)

            # Verify exactly one default, it's network B
            networks = _all_networks(mvm_binary)
            second_defaults = [n for n in networks if n.get("is_default")]
            assert len(second_defaults) == 1, (
                f"Expected exactly 1 default network after second set, "
                f"got {len(second_defaults)}"
            )
            assert second_defaults[0]["name"] == net_b_name, (
                "Network B did not become the sole default"
            )

        finally:
            # ── 3. Cleanup: remove both networks ───────────────────────
            _run_mvm(mvm_binary, "network", "rm", net_a_name, check=False)
            _run_mvm(mvm_binary, "network", "rm", net_b_name, check=False)
            # ── 4. Restore original default if it still exists ─────────
            if original_default_id:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    original_default_id[:6],
                    check=False,
                )


class TestVolumeTransitionsToAvailableAfterVmRm:
    """After VM removal, any attached volumes must transition back to 'available'."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_invariant,
        pytest.mark.requires_kvm,
        pytest.mark.requires_network,
        pytest.mark.slow,
    ]

    def test_volume_transitions_to_available_after_vm_rm(
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
    ) -> None:
        """Create VM with a volume, remove VM, verify volume returns to available."""
        key_name = f"sys-inv-key-{unique_key_name}"
        vol_name = f"sys-inv-vol-{unique_key_name}"
        vm_name = unique_vm_name

        try:
            # ── 1. Create throwaway SSH key ────────────────────────────
            _run_mvm(
                mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
            )

            # ── 2. Create volume ───────────────────────────────────────
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            # ── 3. Create VM with volume and SSH key ───────────────────
            result = _run_mvm(
                mvm_binary,
                "vm",
                "create",
                "--name",
                vm_name,
                "--image",
                "alpine-3.21",
                "--volume",
                vol_name,
                "--ssh-key",
                key_name,
            )
            assert result.returncode == 0, (
                f"VM creation failed: {result.stderr}"
            )

            # ── 4. Verify volume is attached ───────────────────────────
            vol_inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "attached", (
                f"Expected volume status 'attached', got '{vol_data['status']}'"
            )

            # ── 5. Remove VM ───────────────────────────────────────────
            result = _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
            )
            assert result.returncode == 0, f"VM removal failed: {result.stderr}"

            # ── 6. Verify volume is available again ────────────────────
            vol_inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            vol_data = json.loads(vol_inspect.stdout)
            assert vol_data["status"] == "available", (
                f"Expected volume status 'available' after VM removal, "
                f"got '{vol_data['status']}'"
            )

        finally:
            # ── 7. Cleanup ─────────────────────────────────────────────
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(
                mvm_binary, "volume", "rm", vol_name, "--force", check=False
            )
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


# ============================================================================
# Helpers
# ============================================================================


def _present_images(mvm_binary: str) -> list[dict[str, Any]]:
    """List present cached images."""
    result = _run_mvm(mvm_binary, "image", "ls", "--json")
    images: list[dict[str, Any]] = json.loads(result.stdout)
    return [i for i in images if i.get("is_present")]


def _present_kernels(mvm_binary: str) -> list[dict[str, Any]]:
    """List present cached kernels."""
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
    kernels: list[dict[str, Any]] = json.loads(result.stdout)
    return [k for k in kernels if k.get("is_present")]


def _all_networks(mvm_binary: str) -> list[dict[str, Any]]:
    """List all networks."""
    result = _run_mvm(mvm_binary, "network", "ls", "--json")
    networks: list[dict[str, Any]] = json.loads(result.stdout)
    return networks


__all__: list[str] = []
