"""Unit tests for vm_lifecycle helper functions (no KVM/subprocess required)."""

from unittest.mock import patch


from fcm.core.vm_lifecycle import cleanup_tap, graceful_shutdown
from fcm.exceptions import NetworkError


def test_graceful_shutdown_no_pid() -> None:
    """shutdown with pid=None is a no-op."""
    # Should not raise
    graceful_shutdown(None, None)


def test_graceful_shutdown_already_dead() -> None:
    """shutdown with a PID that is already gone finishes cleanly."""
    with patch("os.kill", side_effect=ProcessLookupError):
        graceful_shutdown(99999, None)


def test_graceful_shutdown_via_sigterm_then_sigkill() -> None:
    """Exercises the SIGTERM -> SIGKILL path when process stays alive."""

    call_count = {"n": 0}

    def fake_kill(pid: int, sig: int) -> None:
        call_count["n"] += 1
        if sig == 0:
            # Always report alive
            pass
        # SIGTERM and SIGKILL: succeed silently

    with (
        patch("os.kill", side_effect=fake_kill),
        patch("time.sleep"),
    ):
        graceful_shutdown(12345, None)


def test_cleanup_tap_success() -> None:
    """cleanup_tap calls remove rules and delete tap."""
    with (
        patch("fcm.core.vm_lifecycle.remove_iptables_forward_rules") as mock_rules,
        patch("fcm.core.vm_lifecycle.delete_tap") as mock_del,
    ):
        cleanup_tap("fc-vm1-0")
        mock_rules.assert_called_once_with("fc-vm1-0")
        mock_del.assert_called_once_with("fc-vm1-0")


def test_cleanup_tap_network_error_swallowed() -> None:
    """cleanup_tap swallows NetworkError from delete_tap."""
    with (
        patch("fcm.core.vm_lifecycle.remove_iptables_forward_rules"),
        patch("fcm.core.vm_lifecycle.delete_tap", side_effect=NetworkError("no tap")),
    ):
        # Should not raise
        cleanup_tap("fc-vm1-0")
