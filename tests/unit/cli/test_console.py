"""Comprehensive tests for CLI console command."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import typer
import typer.main
from click.testing import CliRunner

from mvmctl.cli.console import _do_attach, _do_kill, _show_state, app
from mvmctl.exceptions import MVMError, VMNotFoundError

# Convert Typer app to Click command for CliRunner
click_app = typer.main.get_command(app)
runner = CliRunner()


def _make_fake_stdin(extra_buffer_read=b""):
    """Create a fake stdin with a valid fileno() for TTY tests."""
    fake_stdin = MagicMock()
    fake_stdin.fileno.return_value = 0
    fake_stdin.buffer = MagicMock()
    reads = [b"\x18", b"d"]
    if extra_buffer_read:
        reads.insert(0, extra_buffer_read)
    fake_stdin.buffer.read.side_effect = reads
    return fake_stdin


class TestAttachCommand:
    """Tests for the attach command entry point."""

    def test_attach_no_flags_calls_do_attach(self):
        """No flags should call _do_attach."""
        with patch("mvmctl.cli.console._do_attach") as mock_do_attach:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = []
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm"])

                assert result.exit_code == 0
                mock_do_attach.assert_called_once_with("testvm")

    def test_attach_state_flag_calls_show_state(self):
        """--state flag should call _show_state."""
        with patch("mvmctl.cli.console._show_state") as mock_show_state:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm", "--state"])

                assert result.exit_code == 0
                mock_show_state.assert_called_once_with("testvm")

    def test_attach_kill_flag_calls_do_kill(self):
        """--kill flag should call _do_kill."""
        with patch("mvmctl.cli.console._do_kill") as mock_do_kill:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm", "--kill"])

                assert result.exit_code == 0
                mock_do_kill.assert_called_once_with("testvm")

    def test_attach_vm_not_found(self):
        """VM not found should exit with error."""
        with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
            mock_mgr = MagicMock()
            mock_mgr.get.return_value = None
            mock_mgr.find_by_id_prefix.return_value = []
            mock_get_mgr.return_value = mock_mgr

            result = runner.invoke(click_app, ["nonexistent"])

            assert result.exit_code == 1

    def test_attach_name_required(self):
        """Missing positional and --name should show usage error."""
        result = runner.invoke(click_app, [])

        assert result.exit_code == 1

    def test_attach_positional_short_id_resolves_vm(self):
        """Positional arg with short ID resolves to VM name."""
        with patch("mvmctl.cli.console._do_attach") as mock_do_attach:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                mock_mgr.get.return_value = None  # Not found by exact name
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["abc123"])

                assert result.exit_code == 0
                mock_do_attach.assert_called_once_with("testvm")

    def test_attach_positional_name_fallback(self):
        """Positional arg falls back to name lookup if not short ID."""
        with patch("mvmctl.cli.console._do_attach") as mock_do_attach:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_mgr = MagicMock()
                mock_mgr.find_by_id_prefix.return_value = []
                mock_mgr.get.return_value = mock_vm  # Found by exact name
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["myvm"])

                assert result.exit_code == 0
                mock_do_attach.assert_called_once_with("myvm")

    def test_attach_short_id_ambiguous(self):
        """Multiple short ID matches shows error."""
        with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
            mock_vm1 = MagicMock()
            mock_vm1.name = "vm1"
            mock_vm2 = MagicMock()
            mock_vm2.name = "vm2"
            mock_mgr = MagicMock()
            mock_mgr.find_by_id_prefix.return_value = [mock_vm1, mock_vm2]
            mock_mgr.get.return_value = None
            mock_get_mgr.return_value = mock_mgr

            result = runner.invoke(click_app, ["abc"])

            assert result.exit_code == 1
            assert "Multiple VMs match ID prefix" in result.stdout


class TestShowStateFunction:
    """Tests for _show_state function."""

    def test_show_state_running_with_pid_and_socket(self):
        """Shows running state with PID and socket."""
        with patch("mvmctl.cli.console.print_info") as mock_print:
            with patch("mvmctl.cli.console._get_console_state") as mock_get_state:
                mock_get_state.return_value = {
                    "running": True,
                    "pid": 12345,
                    "socket_path": "/tmp/test.sock",
                }

                _show_state("testvm")

                mock_print.assert_any_call("Console for 'testvm': running")
                mock_print.assert_any_call("  PID: 12345")
                mock_print.assert_any_call("  Socket: /tmp/test.sock")

    def test_show_state_stopped(self):
        """Shows stopped state."""
        with patch("mvmctl.cli.console.print_info") as mock_print:
            with patch("mvmctl.cli.console._get_console_state") as mock_get_state:
                mock_get_state.return_value = {
                    "running": False,
                    "pid": None,
                    "socket_path": None,
                }

                _show_state("testvm")

                mock_print.assert_any_call("Console for 'testvm': stopped")

    def test_show_state_vm_not_found(self):
        """VM not found exits with error."""
        with patch("mvmctl.cli.console.print_error") as mock_print:
            with patch("mvmctl.cli.console._get_console_state") as mock_get_state:
                mock_get_state.side_effect = VMNotFoundError("VM 'nonexistent' not found")

                try:
                    _show_state("nonexistent")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("VM 'nonexistent' not found")

    def test_show_state_handles_mvm_error(self):
        """Handles MVMError gracefully."""
        with patch("mvmctl.utils.error_handler.print_error") as mock_print:
            with patch("mvmctl.cli.console._get_console_state") as mock_get_state:
                mock_get_state.side_effect = MVMError("Console error")

                try:
                    _show_state("testvm")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("Console error")


class TestDoKillFunction:
    """Tests for _do_kill function."""

    def test_do_kill_success(self):
        """Successfully kills console relay."""
        with patch("mvmctl.cli.console.print_success") as mock_print:
            with patch("mvmctl.cli.console._kill_console") as mock_kill:
                mock_kill.return_value = True

                _do_kill("testvm")

                mock_print.assert_called_once_with("Console relay stopped for 'testvm'")

    def test_do_kill_already_stopped(self):
        """Relay already stopped shows error."""
        with patch("mvmctl.cli.console.print_error") as mock_print:
            with patch("mvmctl.cli.console._kill_console") as mock_kill:
                mock_kill.return_value = False

                try:
                    _do_kill("testvm")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("No console relay running for 'testvm'")

    def test_do_kill_vm_not_found(self):
        """VM not found exits with error."""
        with patch("mvmctl.cli.console.print_error") as mock_print:
            with patch("mvmctl.cli.console._kill_console") as mock_kill:
                mock_kill.side_effect = VMNotFoundError("VM 'nonexistent' not found")

                try:
                    _do_kill("nonexistent")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("VM 'nonexistent' not found")

    def test_do_kill_handles_mvm_error(self):
        """Handles MVMError gracefully."""
        with patch("mvmctl.utils.error_handler.print_error") as mock_print:
            with patch("mvmctl.cli.console._kill_console") as mock_kill:
                mock_kill.side_effect = MVMError("Kill error")

                try:
                    _do_kill("testvm")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("Kill error")


class TestDoAttachFunction:
    """Tests for _do_attach function."""

    def test_do_attach_success(self):
        """Successfully attaches to console."""
        with patch("mvmctl.cli.console.print_info") as _mock_print:
            with patch("mvmctl.cli.console.disconnect_from_relay"):
                with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                    with patch("mvmctl.cli.console._attach_console") as mock_attach:
                        mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                        mock_sock = MagicMock()
                        mock_connect.return_value = mock_sock

                        # Mock TTY operations to raise (simulating non-TTY environment)
                        with patch(
                            "mvmctl.cli.console.termios.tcgetattr",
                            side_effect=Exception("no tty"),
                        ):
                            try:
                                _do_attach("testvm")
                            except Exception:
                                pass

                        mock_attach.assert_called_once_with("testvm")
                        mock_connect.assert_called_once_with(Path("/tmp/test.sock"))

    def test_do_attach_vm_not_found(self):
        """VM not found exits with error."""
        with patch("mvmctl.cli.console.print_error") as mock_print:
            with patch("mvmctl.cli.console._attach_console") as mock_attach:
                mock_attach.side_effect = VMNotFoundError("VM 'nonexistent' not found")

                try:
                    _do_attach("nonexistent")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("VM 'nonexistent' not found")

    def test_do_attach_console_not_running(self):
        """Console not running exits with error."""
        with patch("mvmctl.utils.error_handler.print_error") as mock_print:
            with patch("mvmctl.cli.console._attach_console") as mock_attach:
                mock_attach.side_effect = MVMError("No console relay running")

                try:
                    _do_attach("testvm")
                    assert False, "Expected typer.Exit"
                except typer.Exit as exc:
                    assert exc.exit_code == 1
                    mock_print.assert_called_once_with("No console relay running")

    def test_do_attach_connection_refused(self):
        """Connection refused exits with error."""
        with patch("mvmctl.cli.console.print_info"):
            with patch("mvmctl.cli.console.print_error") as mock_print:
                with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                    with patch("mvmctl.cli.console._attach_console") as mock_attach:
                        mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                        mock_connect.side_effect = ConnectionRefusedError("Connection refused")

                        try:
                            _do_attach("testvm")
                            assert False, "Expected typer.Exit"
                        except typer.Exit as exc:
                            assert exc.exit_code == 1
                            assert "Failed to connect to console" in mock_print.call_args[0][0]

    def test_do_attach_socket_not_found(self):
        """Socket not found exits with error."""
        with patch("mvmctl.cli.console.print_info"):
            with patch("mvmctl.cli.console.print_error") as mock_print:
                with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                    with patch("mvmctl.cli.console._attach_console") as mock_attach:
                        mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                        mock_connect.side_effect = FileNotFoundError("Socket not found")

                        try:
                            _do_attach("testvm")
                            assert False, "Expected typer.Exit"
                        except typer.Exit as exc:
                            assert exc.exit_code == 1
                            assert "Failed to connect to console" in mock_print.call_args[0][0]

    def test_do_attach_timeout(self):
        """Connection timeout exits with error."""
        with patch("mvmctl.cli.console.print_info"):
            with patch("mvmctl.cli.console.print_error") as mock_print:
                with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                    with patch("mvmctl.cli.console._attach_console") as mock_attach:
                        mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                        mock_connect.side_effect = TimeoutError("Connection timed out")

                        try:
                            _do_attach("testvm")
                            assert False, "Expected typer.Exit"
                        except typer.Exit as exc:
                            assert exc.exit_code == 1
                            assert "Failed to connect to console" in mock_print.call_args[0][0]

    def test_do_attach_keyboard_interrupt(self):
        """KeyboardInterrupt is handled gracefully."""
        with patch("mvmctl.cli.console.print_info") as _mock_print:
            with patch("mvmctl.cli.console.read_console_output") as _mock_read:
                with patch("mvmctl.cli.console.select.select") as mock_select:
                    with patch("mvmctl.cli.console.disconnect_from_relay") as mock_disconnect:
                        with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                            with patch("mvmctl.cli.console._attach_console") as mock_attach:
                                mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                                mock_sock = MagicMock()
                                mock_connect.return_value = mock_sock

                                # Mock select to raise KeyboardInterrupt
                                mock_select.side_effect = KeyboardInterrupt

                                # Create fake stdin with valid fileno
                                fake_stdin = _make_fake_stdin()
                                fake_stdout = MagicMock()

                                mock_tty_settings = MagicMock()
                                with patch("sys.stdin", fake_stdin):
                                    with patch("sys.stdout", fake_stdout):
                                        with patch(
                                            "mvmctl.cli.console.termios.tcgetattr",
                                            return_value=mock_tty_settings,
                                        ):
                                            with patch("mvmctl.cli.console.tty.setraw"):
                                                with patch("mvmctl.cli.console.termios.tcsetattr"):
                                                    _do_attach("testvm")

                                # Verify disconnect was called
                                mock_disconnect.assert_called_once()

    def test_do_attach_detach_sequence(self):
        """Detach sequence (Ctrl+X D) works correctly."""
        import sys

        with patch("mvmctl.cli.console.print_info") as mock_print:
            with patch("mvmctl.cli.console.send_console_input") as _mock_send:
                with patch("mvmctl.cli.console.check_escape_sequence") as mock_check:
                    with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                        with patch("mvmctl.cli.console._attach_console") as mock_attach:
                            mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                            mock_sock = MagicMock()
                            mock_connect.return_value = mock_sock

                            # Create fake stdin that reads escape sequence then EOF
                            fake_stdin = _make_fake_stdin()
                            fake_stdout = MagicMock()

                            # Mock select to show stdin ready
                            def select_side_effect(stdin_list, wlist, xlist, timeout=0):
                                return (stdin_list, [], [])

                            with patch(
                                "mvmctl.cli.console.select.select",
                                side_effect=select_side_effect,
                            ):
                                # Mock escape sequence detection
                                def check_escape(seq):
                                    if len(seq) >= 2 and seq[-2:] == b"\x18d":
                                        return (True, "detach")
                                    return (False, None)

                                mock_check.side_effect = check_escape

                                # Yield output indefinitely until detach
                                def infinite_output(sock):
                                    while True:
                                        yield b"output"

                                with patch(
                                    "mvmctl.cli.console.read_console_output",
                                    side_effect=infinite_output,
                                ):
                                    with patch.object(sys, "stdin", fake_stdin):
                                        with patch("sys.stdout", fake_stdout):
                                            with patch(
                                                "mvmctl.cli.console.termios.tcgetattr",
                                                return_value=MagicMock(),
                                            ):
                                                with patch("mvmctl.cli.console.tty.setraw"):
                                                    with patch(
                                                        "mvmctl.cli.console.termios.tcsetattr"
                                                    ):
                                                        _do_attach("testvm")

                            # Verify detach message was printed
                            calls = [str(c) for c in mock_print.call_args_list]
                            assert any("Detached from console" in c for c in calls)

    def test_do_attach_tty_restored_on_exit(self):
        """TTY settings are restored on exit."""
        with patch("mvmctl.cli.console.print_info") as _mock_print:
            with patch("mvmctl.cli.console.send_console_input") as _mock_send:
                with patch("mvmctl.cli.console.check_escape_sequence") as _mock_check:
                    with patch("mvmctl.cli.console.read_console_output") as mock_read:
                        with patch("mvmctl.cli.console.select.select") as mock_select:
                            with patch(
                                "mvmctl.cli.console.disconnect_from_relay"
                            ) as _mock_disconnect:
                                with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                                    with patch("mvmctl.cli.console._attach_console") as mock_attach:
                                        mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                                        mock_sock = MagicMock()
                                        mock_connect.return_value = mock_sock

                                        mock_read.return_value = iter([])
                                        mock_select.side_effect = KeyboardInterrupt

                                        # Create fake stdin with valid fileno
                                        fake_stdin = _make_fake_stdin()
                                        fake_stdout = MagicMock()

                                        mock_tty_settings = MagicMock()
                                        with patch("sys.stdin", fake_stdin):
                                            with patch("sys.stdout", fake_stdout):
                                                with patch(
                                                    "mvmctl.cli.console.termios.tcgetattr",
                                                    return_value=mock_tty_settings,
                                                ):
                                                    with patch("mvmctl.cli.console.tty.setraw"):
                                                        with patch(
                                                            "mvmctl.cli.console.termios.tcsetattr"
                                                        ) as mock_tcsetattr:
                                                            _do_attach("testvm")

                                                            # Verify TTY settings were restored
                                                            mock_tcsetattr.assert_called()

    def test_do_attach_disconnect_on_exit(self):
        """Socket is disconnected on exit."""
        with patch("mvmctl.cli.console.print_info") as _mock_print:
            with patch("mvmctl.cli.console.read_console_output") as mock_read:
                with patch("mvmctl.cli.console.select.select") as mock_select:
                    with patch("mvmctl.cli.console.disconnect_from_relay") as mock_disconnect:
                        with patch("mvmctl.cli.console.connect_to_relay") as mock_connect:
                            with patch("mvmctl.cli.console._attach_console") as mock_attach:
                                mock_attach.return_value = {"socket_path": "/tmp/test.sock"}
                                mock_sock = MagicMock()
                                mock_connect.return_value = mock_sock

                                mock_read.return_value = iter([])
                                mock_select.side_effect = KeyboardInterrupt

                                # Create fake stdin with valid fileno
                                fake_stdin = _make_fake_stdin()
                                fake_stdout = MagicMock()

                                mock_tty_settings = MagicMock()
                                with patch("sys.stdin", fake_stdin):
                                    with patch("sys.stdout", fake_stdout):
                                        with patch(
                                            "mvmctl.cli.console.termios.tcgetattr",
                                            return_value=mock_tty_settings,
                                        ):
                                            with patch("mvmctl.cli.console.tty.setraw"):
                                                with patch("mvmctl.cli.console.termios.tcsetattr"):
                                                    _do_attach("testvm")

                                mock_disconnect.assert_called_once_with(mock_sock)


class TestAttachCommandIntegration:
    """Integration tests for attach command using CliRunner."""

    def test_attach_state_flag_integration(self):
        """Test --state flag with full command flow."""
        with patch("mvmctl.cli.console._show_state") as mock_show:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm", "--state"])

                assert result.exit_code == 0
                mock_show.assert_called_once_with("testvm")

    def test_attach_kill_flag_integration(self):
        """Test --kill flag with full command flow."""
        with patch("mvmctl.cli.console._do_kill") as mock_kill:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm", "--kill"])

                assert result.exit_code == 0
                mock_kill.assert_called_once_with("testvm")

    def test_attach_default_attach_mode_integration(self):
        """Test default attach mode with full command flow."""
        with patch("mvmctl.cli.console._do_attach") as mock_attach:
            with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                mock_vm = MagicMock()
                mock_vm.name = "testvm"
                mock_mgr = MagicMock()
                mock_mgr.get.return_value = mock_vm
                mock_mgr.find_by_id_prefix.return_value = []
                mock_get_mgr.return_value = mock_mgr

                result = runner.invoke(click_app, ["testvm"])

                assert result.exit_code == 0
                mock_attach.assert_called_once_with("testvm")

    def test_attach_state_and_kill_mutually_exclusive(self):
        """--state and --kill can both be specified (first one wins)."""
        with patch("mvmctl.cli.console._show_state") as mock_show:
            with patch("mvmctl.cli.console._do_kill") as mock_kill:
                with patch("mvmctl.cli._helpers.get_vm_manager") as mock_get_mgr:
                    mock_vm = MagicMock()
                    mock_vm.name = "testvm"
                    mock_mgr = MagicMock()
                    mock_mgr.get.return_value = mock_vm
                    mock_mgr.find_by_id_prefix.return_value = [mock_vm]
                    mock_get_mgr.return_value = mock_mgr

                    result = runner.invoke(click_app, ["testvm", "--state", "--kill"])

                    assert result.exit_code == 0
                    mock_show.assert_called_once_with("testvm")
                    mock_kill.assert_not_called()
