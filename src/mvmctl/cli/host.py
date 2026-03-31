"""Host configuration CLI commands."""

import os
import sys

import typer

from mvmctl.api.host import (
    HostChange,
    check_kvm_access,
    check_required_binaries,
    clean_host,
    clean_ready_pool,
    get_host_state,
    get_ip_forward_status,
    get_ready_pool_dir,
    get_vm_manager,
    init_host,
    reset_host,
)
from mvmctl.api.network import ensure_default_network, restore_networks
from mvmctl.constants import PROJECT_GROUP
from mvmctl.exceptions import HostError, MVMError
from mvmctl.utils.console import print_error, print_info, print_success, print_table, print_warning
from mvmctl.utils.error_handler import handle_mvm_error
from mvmctl.utils.fs import chown_to_real_user, get_cache_dir

_CHAIN_EXISTS_MARKER = "MVM chains already exist"


def _format_change(change: HostChange) -> str:
    """Return a concise one-line description of a host change for display."""

    m = change.mechanism
    s = change.setting
    v = change.applied_value

    if m == "iptables_save":
        return f"iptables rules saved → {v}"
    if m in ("file_create", "file_remove"):
        return f"{s}: created {v}"
    if m == "groupadd":
        return f"group '{v}' created"
    if m == "usermod":
        parts = v.split(":")
        user, group = (parts[0], parts[1]) if len(parts) == 2 else (v, v)
        return f"user '{user}' added to group '{group}'"
    if m == "sysctl":
        orig = change.original_value or "0"
        return f"{s}: {orig} → {v}"
    if m == "noop" and s == "iptables_chains" and v == _CHAIN_EXISTS_MARKER:
        return "iptables chains already exist — keeping existing chain state"
    # Fallback: truncate long values
    orig = change.original_value or ""
    orig_display = (orig[:50] + "…") if len(orig) > 50 else orig
    return f"{s}: {orig_display!r} → {v!r}"


app = typer.Typer(
    help="Host configuration",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


def _abort_if_vms_running(action: str) -> None:
    """Exit with an error if any VMs are currently running.

    Args:
        action: Short description of the action being blocked (used in the error message).
    """
    from mvmctl.models.vm import VMState

    manager = get_vm_manager()
    running = [v for v in manager.list_all() if v.status == VMState.RUNNING]
    if running:
        names = ", ".join(v.name for v in running)
        print_error(f"Cannot {action}: {len(running)} VM(s) still running: {names}")
        print_error("Stop all VMs first with: mvm vm remove --name <name>")
        raise typer.Exit(code=1)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the host command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@app.command(name="init")
def init_cmd() -> None:
    """Apply host configuration changes. Idempotent.

    This command must be run with sudo the first time. It performs the
    following steps:

    - Creates the 'mvm' system group and adds the current user to it.
    - Installs a sudoers drop-in so group members can manage TAP devices,
      bridges, and iptables rules without a password.
    - Enables IP forwarding (net.ipv4.ip_forward=1).
    - Snapshots the pre-change host state so 'mvm host reset' can roll back.
    - Creates the default network bridge.

    After running, log out and back in (or run ``newgrp mvm``) for group
    membership to take effect.

    Examples:
        sudo mvm host init
    """
    cache_dir = get_cache_dir()
    try:
        changes = init_host(cache_dir)
    except HostError as e:
        if "Root privileges" in str(e):
            print_error("Root privileges required for: mvm host init")
            print_info("Run with sudo: sudo mvm host init")
            if typer.confirm("Run 'sudo mvm host init' now?", default=False):
                import subprocess

                if os.environ.get("MVM_SUDO_RESTART"):
                    print_error("Recursive sudo restart detected. Aborting to prevent lockout.")
                    print_info("Please run 'sudo mvm host init' manually.")
                    raise typer.Exit(code=1)

                try:
                    env = os.environ.copy()
                    env["MVM_SUDO_RESTART"] = "1"
                    env["MVM_ESCALATED"] = "1"
                    subprocess.run(["sudo"] + sys.argv, check=False, env=env)
                except FileNotFoundError:
                    print_error("sudo command not found")
        else:
            print_error(str(e))
        raise typer.Exit(code=1)

    from mvmctl.utils.audit import log_audit

    log_audit("host.init", f"changes={len(changes)}")

    if not changes:
        print_info("Host already configured — nothing to do.")
    else:
        applied_changes = 0
        for change in changes:
            if change.mechanism == "noop" and change.setting == "iptables_chains":
                print_warning(_format_change(change))
                continue
            applied_changes += 1
            print_success(_format_change(change))
        if applied_changes == 0:
            print_info("Host already configured — nothing to do.")
        else:
            print_success(f"Host initialized ({applied_changes} change(s) applied).")
            print_warning(
                "ACTION REQUIRED: Log out and back in for group membership to take effect."
            )
            print_info(f"Or run immediately: newgrp {PROJECT_GROUP}")

    try:
        restore_results = restore_networks()
        if restore_results:
            for result in restore_results:
                print_info(f"  {result}")
        else:
            ensure_default_network()
            print_success("Default network ready.")
    except MVMError as e:
        print_warning(f"Network setup skipped: {e}")

    chown_to_real_user(get_cache_dir())


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

    rows = [
        ["/dev/kvm", "ok" if kvm_ok else "FAIL", "accessible" if kvm_ok else "not accessible"],
        [
            "required binaries",
            "ok" if not missing else "FAIL",
            "all found" if not missing else f"missing: {', '.join(missing)}",
        ],
        ["ip_forward", "ok" if fwd_ok else "off", f"value={ip_fwd}"],
        [
            "state snapshot",
            "saved" if state else "none",
            state.init_timestamp if state else "no snapshot",
        ],
    ]
    print_table(title="Host Configuration", columns=["Check", "Status", "Detail"], rows=rows)


@app.command(name="clean")
def clean_cmd(
    force: bool = typer.Option(False, "--force", help="Skip confirmation"),
) -> None:
    """Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl or group."""
    _abort_if_vms_running("clean")

    if not force:
        print_warning(
            "This will tear down all network bridges, TAP devices, and iptables rules. "
            "Sysctl, sudoers, and group settings will NOT be touched."
        )
        typer.confirm("Proceed with host clean?", abort=True)

    cache_dir = get_cache_dir()
    try:
        summary = clean_host(cache_dir)
    except MVMError as e:
        handle_mvm_error(e)

    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                print_warning(f"  {item}")
            else:
                print_info(f"  {item}")

    print_success("Host cleaned successfully.")


@app.command(name="reset")
def reset_cmd(
    force: bool = typer.Option(False, "--force", help="Skip confirmation"),
) -> None:
    """Full rollback: remove networking, revert sysctl, remove sudoers and group.

    Reverts every change made by 'mvm host init':

    - Tears down all network bridges, TAP devices, and iptables rules.
    - Restores the original sysctl ip_forward value.
    - Removes the sudoers drop-in file.
    - Removes the 'mvm' system group.

    All running VMs must be stopped before running this command.

    Examples:
        sudo mvm host reset --force
    """
    _abort_if_vms_running("reset")

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
    except MVMError as e:
        handle_mvm_error(e)

    from mvmctl.utils.audit import log_audit

    log_audit("host.reset")

    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                print_warning(f"  {item}")
            else:
                print_info(f"  {item}")

    print_success("Host reset successfully.")


@app.command(name="clean-ready-pool")
def clean_ready_pool_cmd(
    force: bool = typer.Option(False, "--force", help="Skip confirmation"),
) -> None:
    """Clear the tmpfs ready pool to free RAM.

    The ready pool holds decompressed VM images in tmpfs (RAM) for fast cloning.
    This command removes all cached images to free up memory. Images will be
    re-decompressed on next VM creation.

    Examples:
        mvm host clean-ready-pool
        mvm host clean-ready-pool --force
    """

    ready_dir = get_ready_pool_dir()

    if not ready_dir.exists() or not any(ready_dir.iterdir()):
        print_info("Ready pool is already empty.")
        return

    if not force:
        print_warning(
            f"This will remove all cached images from {ready_dir} "
            "to free up RAM. Images will be re-decompressed on next VM creation."
        )
        typer.confirm("Proceed with cleaning ready pool?", abort=True)

    removed_count = clean_ready_pool()

    from mvmctl.utils.audit import log_audit

    log_audit("host.clean_ready_pool", f"removed={removed_count}")

    if removed_count > 0:
        print_success(f"Ready pool cleaned: removed {removed_count} image(s).")
    else:
        print_info("Ready pool is empty.")
