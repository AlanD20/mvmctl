"""VM console access commands - attach, state, kill."""

from __future__ import annotations

import select
import socket
import sys
import termios
import tty

import typer

from mvmctl.api.console_operations import ConsoleOperation
from mvmctl.constants import CONST_CONSOLE_SOCKET_TIMEOUT_S
from mvmctl.exceptions import MVMError
from mvmctl.utils._io import print_error, print_info, print_success
from mvmctl.utils.cli import handle_errors

console_app = typer.Typer(
    help="VM console access",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@console_app.callback(invoke_without_command=True)
@handle_errors
def console(
    ctx: typer.Context,
    identifier: str | None = typer.Argument(
        None, help="VM name, ID, IP, or MAC address"
    ),
    name: str | None = typer.Option(None, "--name", "-n", help="VM name"),
    ip: str | None = typer.Option(None, "--ip", help="VM guest IP address"),
    mac: str | None = typer.Option(None, "--mac", help="VM guest MAC address"),
    state: bool = typer.Option(
        False, "--state", help="Show console state without attaching"
    ),
    kill: bool = typer.Option(False, "--kill", help="Kill the console relay"),
) -> None:
    """
    Attach to a VM console.

    Provide a VM identifier as a positional argument, or use
    --name, --ip, or --mac to specify the VM explicitly.

    Press Ctrl+X then D to detach from the console.
    """
    if ctx.invoked_subcommand is not None:
        return

    # Resolve identifier: explicit flags take priority, then positional
    vm_id: str | None = name or ip or mac or identifier
    if not vm_id:
        print_error(
            "Provide a VM name, ID, IP, or MAC as argument, "
            "or use --name, --ip, or --mac"
        )
        raise typer.Exit(1)

    if state:
        _show_console_state(vm_id)
    elif kill:
        _kill_console_relay(vm_id)
    else:
        _attach_to_console(vm_id)


def _show_console_state(vm_id: str) -> None:
    """Display console relay state for a VM."""
    state_dict = ConsoleOperation.get_state(vm_id)

    status = "running" if state_dict["running"] else "stopped"
    print_info(f"Console for '{vm_id}': {status}")
    if state_dict["pid"]:
        print_info(f"  PID: {state_dict['pid']}")
    if state_dict["socket_path"]:
        print_info(f"  Socket: {state_dict['socket_path']}")


def _kill_console_relay(vm_id: str) -> None:
    """Kill the console relay for a VM."""
    killed = ConsoleOperation.kill(vm_id)

    if killed:
        print_success(f"Console relay stopped for '{vm_id}'")
    else:
        print_error(f"No console relay running for '{vm_id}'")
        raise typer.Exit(1)


def _attach_to_console(vm_id: str) -> None:
    """Attach to VM console interactively."""
    attach_info = ConsoleOperation.attach(vm_id)

    print_info(f"Attaching to console of '{attach_info.vm_name}'...")
    print_info("Press Ctrl+X then D to detach")

    sock = _connect_socket(attach_info.socket_path)
    if sock is None:
        print_error("Failed to connect to console relay")
        raise typer.Exit(1)

    old_tty = None
    try:
        old_tty = termios.tcgetattr(sys.stdin)
        tty.setraw(sys.stdin.fileno())
        _interact(sock)
        print_info("\nDetached from console")
    except KeyboardInterrupt:
        pass
    except MVMError as e:
        print_error(str(e))
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
        print_error(f"Failed to connect to console relay: {e}")
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
