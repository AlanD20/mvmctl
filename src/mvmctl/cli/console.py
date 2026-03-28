import select
import sys
import termios
import tty
from pathlib import Path
from typing import Optional

import typer

from mvmctl.api.vms import (
    attach_console as _attach_console,
)
from mvmctl.api.vms import (
    check_escape_sequence,
    connect_to_relay,
    disconnect_from_relay,
    get_vm_manager,
    read_console_output,
    send_console_input,
)
from mvmctl.api.vms import (
    get_console_state as _get_console_state,
)
from mvmctl.api.vms import (
    kill_console as _kill_console,
)
from mvmctl.exceptions import MVMError
from mvmctl.utils.console import print_error, print_info, print_success

app = typer.Typer(
    help="VM console access",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


def _resolve_vm(vm_id: Optional[str], name: Optional[str]) -> str:
    manager = get_vm_manager()

    if name:
        if manager.get(name) is None:
            print_error(f"VM '{name}' not found")
            raise typer.Exit(1)
        return name

    if vm_id:
        matches = manager.find_by_short_id(vm_id)
        if len(matches) == 1:
            return matches[0].name
        if len(matches) > 1:
            print_error(f"Multiple VMs match short ID '{vm_id}' — use a longer prefix or --name")
            raise typer.Exit(1)
        if manager.get(vm_id) is not None:
            return vm_id
        print_error(f"No VM found with short ID or name '{vm_id}'")
        raise typer.Exit(1)

    print_error("Provide a VM short ID or --name")
    raise typer.Exit(1)


@app.command()
def attach(
    vm_id: Optional[str] = typer.Argument(None, help="VM short ID (first 6 chars) or name"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name"),
    state: bool = typer.Option(False, "--state", help="Show console state without attaching"),
    kill: bool = typer.Option(False, "--kill", help="Kill the console relay"),
) -> None:
    """Attach to a VM console by short ID (e.g., 3df) or --name."""
    vm_name = _resolve_vm(vm_id, name)

    if state:
        _show_state(vm_name)
        return

    if kill:
        _do_kill(vm_name)
        return

    _do_attach(vm_name)


def _show_state(name: str) -> None:
    manager = get_vm_manager()
    if manager.get(name) is None:
        print_error(f"VM '{name}' not found")
        raise typer.Exit(1)

    try:
        state = _get_console_state(name)
        status = "running" if state["running"] else "stopped"
        print_info(f"Console for '{name}': {status}")
        if state["pid"]:
            print_info(f"  PID: {state['pid']}")
        if state["socket_path"]:
            print_info(f"  Socket: {state['socket_path']}")
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(1)


def _do_kill(name: str) -> None:
    manager = get_vm_manager()
    if manager.get(name) is None:
        print_error(f"VM '{name}' not found")
        raise typer.Exit(1)

    try:
        killed = _kill_console(name)
        if killed:
            print_success(f"Console relay stopped for '{name}'")
        else:
            print_error(f"No console relay running for '{name}'")
            raise typer.Exit(1)
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(1)


def _do_attach(name: str) -> None:
    manager = get_vm_manager()
    if manager.get(name) is None:
        print_error(f"VM '{name}' not found")
        raise typer.Exit(1)

    try:
        info = _attach_console(name)
        socket_path = Path(info["socket_path"])
    except MVMError as e:
        print_error(str(e))
        raise typer.Exit(1)

    print_info(f"Attaching to console of '{name}'...")
    print_info("Press Ctrl+A then D to detach")

    try:
        sock = connect_to_relay(socket_path)
    except (ConnectionRefusedError, FileNotFoundError, TimeoutError) as e:
        print_error(f"Failed to connect to console: {e}")
        raise typer.Exit(1)

    old_tty = None
    try:
        old_tty = termios.tcgetattr(sys.stdin)
        tty.setraw(sys.stdin.fileno())

        input_buffer = bytearray()
        detach_requested = False

        for output in read_console_output(sock):
            sys.stdout.buffer.write(output)
            sys.stdout.flush()

            ready, _, _ = select.select([sys.stdin], [], [], 0)
            if sys.stdin in ready:
                char = sys.stdin.buffer.read(1)
                if not char:
                    break

                input_buffer.extend(char)
                matched, action = check_escape_sequence(input_buffer)
                if matched and action == "detach":
                    detach_requested = True
                    break

                if len(input_buffer) > 2:
                    to_send = bytes(input_buffer[:-2])
                    if to_send:
                        send_console_input(sock, to_send)
                    input_buffer = input_buffer[-2:]

        if detach_requested:
            if input_buffer:
                send_console_input(sock, bytes(input_buffer[:-2]))
            print_info("\nDetached from console")
        elif input_buffer:
            send_console_input(sock, bytes(input_buffer))

    except KeyboardInterrupt:
        pass
    finally:
        if old_tty is not None:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, old_tty)
        disconnect_from_relay(sock)
