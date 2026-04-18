import sys
import termios
import tty
from typing import Optional

import typer

from mvmctl.api.console import ConsoleRelaySession
from mvmctl.cli._helpers import resolve_vm_by_id_or_name
from mvmctl.exceptions import MVMError, VMNotFoundError
from mvmctl.utils.console import print_error, print_info, print_success
from mvmctl.utils.error_handler import handle_mvm_error

console_app = typer.Typer(
    help="VM console access",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@console_app.command()
def console_attach(
    vm_id: Optional[str] = typer.Argument(None, help="VM ID prefix or name"),
    name: Optional[str] = typer.Option(None, "--name", "-n", help="VM name"),
    state: bool = typer.Option(False, "--state", help="Show console state without attaching"),
    kill: bool = typer.Option(False, "--kill", help="Kill the console relay"),
) -> None:
    """Attach to a VM console by ID prefix (e.g., 3df) or --name."""
    # Setup: resolve target VM
    vm_name = resolve_vm_by_id_or_name(vm_id, name)
    
    # Setup: create session for this VM
    try:
        session = ConsoleRelaySession(vm_name)
    except VMNotFoundError as e:
        print_error(str(e))
        raise typer.Exit(1)
    
    # Route to operation
    if state:
        _show_console_state(session, vm_name)
    elif kill:
        _kill_console_relay(session, vm_name)
    else:
        _attach_to_console(session, vm_name)


def _show_console_state(session: ConsoleRelaySession, vm_name: str) -> None:
    """Display console relay state for a VM."""
    try:
        state_dict = session.get_state()
    except MVMError as e:
        handle_mvm_error(e)
        return
        
    status = "running" if state_dict["running"] else "stopped"
    print_info(f"Console for '{vm_name}': {status}")
    if state_dict["pid"]:
        print_info(f"  PID: {state_dict['pid']}")
    if state_dict["socket_path"]:
        print_info(f"  Socket: {state_dict['socket_path']}")


def _kill_console_relay(session: ConsoleRelaySession, vm_name: str) -> None:
    """Kill the console relay for a VM."""
    try:
        killed = session.kill()
    except MVMError as e:
        handle_mvm_error(e)
        return
        
    if killed:
        print_success(f"Console relay stopped for '{vm_name}'")
    else:
        print_error(f"No console relay running for '{vm_name}'")
        raise typer.Exit(1)


def _attach_to_console(session: ConsoleRelaySession, vm_name: str) -> None:
    """Attach to VM console interactively."""
    # Validate: check relay is running
    try:
        session.attach()
    except MVMError as e:
        handle_mvm_error(e)
        return
    
    print_info(f"Attaching to console of '{vm_name}'...")
    print_info("Press Ctrl+X then D to detach")
    
    # Execute: connect and run interactive session
    old_tty = None
    try:
        session.connect()
        old_tty = termios.tcgetattr(sys.stdin)
        tty.setraw(sys.stdin.fileno())
        session.interact()
        print_info("\nDetached from console")
    except KeyboardInterrupt:
        pass
    except MVMError as e:
        handle_mvm_error(e)
    finally:
        # Terminate: cleanup terminal and disconnect
        if old_tty is not None:
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, old_tty)
        session.disconnect()
