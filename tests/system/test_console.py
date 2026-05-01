"""Console system tests — console state and relay management."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
]


class TestConsoleState:
    """Test console state reporting on a running VM."""

    def test_console_state(self, mvm_binary, created_vm):
        """Show console relay state for a running VM."""
        result = _run_mvm(
            mvm_binary,
            "console",
            created_vm["name"],
            "--state",
        )
        assert result.returncode == 0
        combined = result.stdout + result.stderr
        assert (
            "Console" in combined
            or "running" in combined
            or "stopped" in combined
        )


class TestConsoleKill:
    """Test console relay kill operation."""

    def test_console_kill(self, mvm_binary, created_vm):
        """Kill the console relay for a VM.

        The relay may not be running if no one has attached yet,
        so we accept either success or the expected error message.
        """
        result = _run_mvm(
            mvm_binary,
            "console",
            created_vm["name"],
            "--kill",
            check=False,
        )

        if result.returncode == 0:
            # Relay was running and got killed
            assert (
                "stopped" in result.stdout
                or "killed" in (result.stdout + result.stderr).lower()
            )
        else:
            # Relay was not running — expect clear error message
            combined = result.stdout + result.stderr
            assert "No console relay" in combined
