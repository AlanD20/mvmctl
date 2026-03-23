"""Host configuration CLI commands."""

from fcm.exceptions import FCMError
import typer
from rich.table import Table

from fcm.core.host import (
    check_kvm_access,
    check_required_binaries,
    clean_host,
    get_host_state,
    get_ip_forward_status,
    init_host,
    prune_host,
    reset_host,
    restore_host,
)
from fcm.exceptions import HostError
from fcm.utils.console import console, print_error, print_info, print_success, print_warning
from fcm.utils.fs import get_cache_dir

app = typer.Typer(help="Host configuration", no_args_is_help=True)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the host command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@app.command(name="init")
def init_cmd() -> None:
    """Apply host configuration changes. Idempotent."""
    cache_dir = get_cache_dir()
    try:
        changes = init_host(cache_dir)
    except HostError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if not changes:
        print_info("Host already configured — nothing to do.")
    else:
        for change in changes:
            print_success(f"{change.setting}: {change.original_value!r} → {change.applied_value!r}")
        print_success(f"Host initialized ({len(changes)} change(s) applied).")
        print_warning(
            "ACTION REQUIRED: Log out and back in for group membership to take effect."
        )
        print_info("Or run immediately: newgrp fcm")

    from fcm.core.network_manager import ensure_default_network

    try:
        ensure_default_network()
        print_success("Default network ready.")
    except FCMError as e:
        print_warning(f"Default network setup skipped: {e}")


@app.command(name="ls")
def ls_cmd(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show current host configuration state vs expected."""
    import json

    kvm_ok = check_kvm_access()
    missing = check_required_binaries()

    try:
        ip_fwd = get_ip_forward_status()
    except HostError:
        ip_fwd = "unknown"
    fwd_ok = ip_fwd == "1"

    cache_dir = get_cache_dir()
    state = None
    try:
        state = get_host_state(cache_dir)
    except HostError:
        pass

    if json_output:
        data = {
            "kvm_accessible": kvm_ok,
            "required_binaries": {"ok": not missing, "missing": missing},
            "ip_forward": {"value": ip_fwd, "ok": fwd_ok},
            "state_snapshot": {
                "exists": state is not None,
                "timestamp": state.init_timestamp if state else None,
            },
        }
        typer.echo(json.dumps(data, indent=2))
        return

    table = Table(title="Host Configuration")
    table.add_column("Check", style="cyan", no_wrap=True)
    table.add_column("Status", style="bold")
    table.add_column("Detail")

    table.add_row(
        "/dev/kvm",
        "[green]ok[/green]" if kvm_ok else "[red]FAIL[/red]",
        "accessible" if kvm_ok else "not accessible",
    )

    table.add_row(
        "required binaries",
        "[green]ok[/green]" if not missing else "[red]FAIL[/red]",
        "all found" if not missing else f"missing: {', '.join(missing)}",
    )

    table.add_row(
        "ip_forward",
        "[green]ok[/green]" if fwd_ok else "[yellow]off[/yellow]",
        f"value={ip_fwd}",
    )

    table.add_row(
        "state snapshot",
        "[green]saved[/green]" if state else "[dim]none[/dim]",
        state.init_timestamp if state else "no snapshot",
    )

    console.print(table)


@app.command(name="clean")
def clean_cmd(
    force: bool = typer.Option(False, "--force", help="Skip confirmation prompt"),
) -> None:
    """Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl or group."""
    from fcm.core.vm_manager import VMManager
    from fcm.models.vm import VMState

    # Refuse if any VMs are running
    manager = VMManager()
    running = [v for v in manager.list_all() if v.status == VMState.RUNNING]
    if running:
        names = ", ".join(v.name for v in running)
        print_error(f"Cannot clean: {len(running)} VM(s) still running: {names}")
        print_error("Stop all VMs first with: fcm vm remove --name <name>")
        raise typer.Exit(code=1)

    if not force:
        print_warning(
            "This will tear down all network bridges, TAP devices, and iptables rules. "
            "Sysctl, sudoers, and group settings will NOT be touched."
        )
        typer.confirm("Proceed with host clean?", abort=True)

    cache_dir = get_cache_dir()
    try:
        summary = clean_host(cache_dir)
    except FCMError as e:
        print_error(f"Clean failed: {e}")
        raise typer.Exit(code=1)

    if summary:
        for item in summary:
            print_info(f"  {item}")

    print_success("Host cleaned successfully.")


@app.command(name="reset")
def reset_cmd(
    force: bool = typer.Option(False, "--force", help="Skip confirmation prompt"),
) -> None:
    """Full rollback: remove networking, revert sysctl, remove sudoers and group."""
    from fcm.core.vm_manager import VMManager
    from fcm.models.vm import VMState

    # Refuse if any VMs are running
    manager = VMManager()
    running = [v for v in manager.list_all() if v.status == VMState.RUNNING]
    if running:
        names = ", ".join(v.name for v in running)
        print_error(f"Cannot reset: {len(running)} VM(s) still running: {names}")
        print_error("Stop all VMs first with: fcm vm remove --name <name>")
        raise typer.Exit(code=1)

    if not force:
        print_warning(
            "This will tear down all networking, revert sysctl changes, "
            "remove the sudoers drop-in, and remove the project group. "
            "This is a full rollback to pre-init state."
        )
        typer.confirm("Proceed with host reset?", abort=True)

    cache_dir = get_cache_dir()
    try:
        summary = reset_host(cache_dir)
    except FCMError as e:
        print_error(f"Reset failed: {e}")
        raise typer.Exit(code=1)

    if summary:
        for item in summary:
            print_info(f"  {item}")

    print_success("Host reset successfully.")


# ---- Deprecated aliases (hidden) ----


@app.command(name="prune", hidden=True)
def prune(
    force: bool = typer.Option(False, "--force", help="Skip confirmation prompt"),
) -> None:
    """[Deprecated] Use 'fcm host clean' instead."""
    print_warning("'host prune' is deprecated. Use 'host clean' instead.")

    from fcm.core.vm_manager import VMManager
    from fcm.models.vm import VMState

    manager = VMManager()
    running = [v for v in manager.list_all() if v.status == VMState.RUNNING]
    if running:
        names = ", ".join(v.name for v in running)
        print_error(f"Cannot prune: {len(running)} VM(s) still running: {names}")
        print_error("Stop all VMs first with: fcm vm remove --name <name>")
        raise typer.Exit(code=1)

    if not force:
        print_warning(
            "This will tear down all network bridges, TAP devices, iptables rules, "
            "and revert host sysctl changes. VM cache files, images, kernels, and "
            "binaries will NOT be removed."
        )
        typer.confirm("Proceed with host prune?", abort=True)

    cache_dir = get_cache_dir()
    try:
        summary = prune_host(cache_dir)
    except FCMError as e:
        print_error(f"Prune failed: {e}")
        raise typer.Exit(code=1)

    if summary:
        for item in summary:
            print_info(f"  {item}")

    print_success("Host pruned successfully.")


@app.command(name="restore", hidden=True)
def restore() -> None:
    """[Deprecated] Use 'fcm host reset' instead."""
    print_warning("'host restore' is deprecated. Use 'host reset' instead.")
    cache_dir = get_cache_dir()
    try:
        reverted = restore_host(cache_dir)
    except HostError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if not reverted:
        print_warning("No changes to revert.")
        return

    for change in reverted:
        print_info(
            f"Reverted {change.setting}: {change.original_value!r} → {change.applied_value!r}"
        )

    print_success(f"Host restored ({len(reverted)} change(s) reverted).")
