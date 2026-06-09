"""VM console access commands - connect, state, kill."""

from __future__ import annotations

import select
import socket
import sys
import termios
import tty

import typer

from mvmctl.cli._completion import _complete_vm_names
from mvmctl.constants import CONST_CONSOLE_SOCKET_TIMEOUT_S
from mvmctl.exceptions import MVMError
from mvmctl.utils.cli import handle_errors, mvm_cli

console_app = typer.Typer(
    help="VM console access",
    no_args_is_help=True,
    add_completion=False,
    context_settings={"allow_interspersed_args": True},
)


@console_app.callback(invoke_without_command=True)
@handle_errors
def console(
    ctx: typer.Context,
    identifier: str = typer.Argument(
        ...,
        help="VM name, ID, IP, or MAC address",
        autocompletion=_complete_vm_names,
    ),
    state: bool = typer.Option(
        False, "--state", help="Show console state without attaching"
    ),
    kill: bool = typer.Option(False, "--kill", help="Kill the console relay"),
) -> None:
    """
    Attach to a VM console.

    Provide a VM identifier (name, ID prefix, IP, or MAC address) as the
    positional argument.

    Press Ctrl+X then D to detach from the console.
    """
    if ctx.invoked_subcommand is not None:
        return

    if state:
        _show_console_state(identifier)
    elif kill:
        _kill_console_relay(identifier)
    else:
        _attach_to_console(identifier)


def _show_console_state(identifier: str) -> None:
    """Display console relay state for a VM."""
    from mvmctl.api import ConsoleOperation

    state_dict = ConsoleOperation.get_state(identifier)

    status = "running" if state_dict["running"] else "stopped"
    mvm_cli.info(f"Console for '{identifier}': {status}")
    if state_dict["pid"]:
        mvm_cli.info(f"  PID: {state_dict['pid']}")
    if state_dict["socket_path"]:
        mvm_cli.info(f"  Socket: {state_dict['socket_path']}")


def _kill_console_relay(identifier: str) -> None:
    """Kill the console relay for a VM."""
    from mvmctl.api import ConsoleOperation

    result = ConsoleOperation.kill(identifier)

    if result.status == "success":
        mvm_cli.success(f"Stopped: {identifier}")
    elif result.status == "skipped":
        mvm_cli.error(f"Console relay not running: {identifier}")
        raise typer.Exit(1)
    else:
        mvm_cli.error(result.message or f"Stop failed: {identifier}")
        raise typer.Exit(1)


def _attach_to_console(identifier: str) -> None:
    """Attach to VM console interactively."""
    from mvmctl.api import ConsoleOperation

    attach_info = ConsoleOperation.get_connection_info(identifier)

    mvm_cli.info(f"Attaching to console of '{attach_info.vm_name}'...")
    mvm_cli.info("Press Ctrl+X then D to detach")

    sock = _connect_socket(attach_info.socket_path)
    if sock is None:
        mvm_cli.error("Console relay connection failed")
        raise typer.Exit(1)

    old_tty = None
    try:
        old_tty = termios.tcgetattr(sys.stdin)
        tty.setraw(sys.stdin.fileno())
        _interact(sock)
        mvm_cli.info("\nDetached from console")
    except KeyboardInterrupt:
        pass
    except MVMError as e:
        mvm_cli.error(str(e))
    finally:
        if old_tty is not None:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, old_tty)
        try:
            sock.close()
        except OSError:
            pass


def _connect_socket(socket_path: str) -> socket.socket | None:
    """Connect to the console relay Unix socket."""
    try:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(CONST_CONSOLE_SOCKET_TIMEOUT_S)
        sock.connect(socket_path)
        sock.setblocking(False)
        return sock
    except (OSError, ConnectionRefusedError, FileNotFoundError) as e:
        mvm_cli.error(f"Console relay connection failed: {e}")
        return None


def _interact(sock: socket.socket) -> None:
    """Run interactive console I/O loop."""
    buffer_size = 4096
    input_buffer = bytearray()

    while True:
        ready, _, _ = select.select([sys.stdin, sock], [], [], 0.05)

        if sock in ready:
            try:
                data = sock.recv(buffer_size)
                if data:
                    sys.stdout.buffer.write(data)
                    sys.stdout.flush()
                else:
                    return
            except (BlockingIOError, InterruptedError):
                pass
            except (OSError, ConnectionResetError):
                return

        if sys.stdin in ready:
            char = sys.stdin.buffer.read(1)
            if not char:
                return

            input_buffer.extend(char)

            if len(input_buffer) >= 2:
                if bytes(input_buffer[-2:]) == b"\x18d":
                    if len(input_buffer) > 2:
                        leftover = bytes(input_buffer[:-2])
                        _try_send(sock, leftover)
                    return

            if input_buffer[0:1] != b"\x18":
                to_send = bytes(input_buffer)
                if to_send:
                    _try_send(sock, to_send)
                input_buffer = bytearray()
            elif len(input_buffer) >= 2:
                if bytes(input_buffer) != b"\x18d":
                    to_send = bytes(input_buffer)
                    if to_send:
                        _try_send(sock, to_send)
                    input_buffer = bytearray()


def _try_send(sock: socket.socket, data: bytes) -> None:
    try:
        sock.sendall(data)
    except (OSError, BrokenPipeError, ConnectionResetError):
        pass
