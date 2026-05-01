"""Host configuration commands."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from typing import TYPE_CHECKING

import typer

from mvmctl.api import HostOperation
from mvmctl.constants import MVM_UNIX_GROUP
from mvmctl.exceptions import HostError, PrivilegeError
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils.cli import handle_errors
from mvmctl.utils.common import CacheUtils, CommonUtils
from mvmctl.utils.fs import FsUtils

if TYPE_CHECKING:
    from mvmctl.models import HostStateChangeItem

_CHAIN_EXISTS_MARKER = "MVM chains already exist"


def _format_change(change: HostStateChangeItem) -> str:
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
    if m == "modprobe" and s == "kernel_module_load":
        return f"loaded kernel module '{v}'"
    if m == "network_create":
        return f"Default network '{v}' ready"
    # Fallback: truncate long values
    orig = change.original_value or ""
    orig_display = (orig[:50] + "…") if len(orig) > 50 else orig
    return f"{s}: {orig_display!r} → {v!r}"


host_app = typer.Typer(
    name="host",
    help="Host configuration",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


def _abort_if_vms_running(action: str) -> None:
    """Exit with an error if any VMs are currently running."""
    try:
        running = HostOperation.get_running_vms()
    except Exception:
        # DB may not exist yet — no VMs can be running.
        return
    if running:
        names = ", ".join(v.name for v in running)
        print_error(
            f"Cannot {action}: {len(running)} VM(s) still running: {names}"
        )
        print_error("Stop all VMs first with: mvm vm remove --name <name>")
        raise typer.Exit(code=1)


@host_app.callback()
def host_callback(ctx: typer.Context) -> None:  # noqa: ARG001
    pass


@host_app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the host command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@host_app.command(name="init")
def host_init() -> None:
    """
    Apply host configuration changes. Idempotent.

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
    cache_dir = CacheUtils.get_cache_dir()
    try:
        changes = HostOperation.init(cache_dir)
    except PrivilegeError as exc:
        print_error(str(exc))
        if exc.details:
            detail_msg = exc.details.get("message", "")
            if detail_msg:
                print_warning(f"Details: {detail_msg}")
            print_info("Options:")
            for suggestion in exc.details.get("suggestions", []):
                print_info(f"  - {suggestion}")
        raise typer.Exit(code=1) from exc
    except HostError as e:
        if "Root privileges" in str(e):
            print_warning("Root privileges required for: mvm host init")
            print_info("Run with sudo: sudo mvm host init")
            if typer.confirm("Run 'sudo mvm host init' now?", default=False):
                if os.environ.get("MVM_SUDO_RESTART"):
                    print_error(
                        "Recursive sudo restart detected. Aborting to prevent lockout."
                    )
                    print_info("Please run 'sudo mvm host init' manually.")
                    raise typer.Exit(code=1)

                try:
                    env = os.environ.copy()
                    env["MVM_SUDO_RESTART"] = "1"
                    env["MVM_ESCALATED"] = "1"
                    subprocess.run(["sudo"] + sys.argv, check=False, env=env)
                except FileNotFoundError:
                    print_error("sudo command not found")
            raise typer.Exit(code=1)
        raise

    if not changes:
        print_info("Host already configured — nothing to do.")
    else:
        applied_changes = 0
        for change in changes:
            if (
                change.mechanism == "noop"
                and change.setting == "iptables_chains"
            ):
                print_warning(_format_change(change))
                continue
            applied_changes += 1
            print_success(_format_change(change))
        if applied_changes == 0:
            print_info("Host already configured — nothing to do.")
        else:
            print_success(
                f"Host initialized ({applied_changes} change(s) applied)."
            )
            print_warning(
                "ACTION REQUIRED: Log out and back in for group membership to take effect."
            )
            print_info(f"Or run immediately: newgrp {MVM_UNIX_GROUP}")

    FsUtils.chown_to_real_user(CacheUtils.get_cache_dir())


@host_app.command(name="ls")
@handle_errors
def host_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show current host configuration state vs expected."""
    kvm_ok = HostOperation.check_kvm_access()
    missing = HostOperation.check_required_binaries()

    try:
        ip_fwd = HostOperation.get_ip_forward_status()
    except HostError:
        ip_fwd = "unknown"
    fwd_ok = ip_fwd == "1"

    state = None
    try:
        state = HostOperation.get_state()
    except HostError:
        pass

    if json_output:
        data = {
            "kvm_accessible": kvm_ok,
            "required_binaries": {"ok": not missing, "missing": missing},
            "ip_forward": {"value": ip_fwd, "ok": fwd_ok},
            "state_snapshot": {
                "exists": state is not None,
                "timestamp": CommonUtils.human_readable_datetime(
                    state.initialized_at
                )
                if state
                else None,
            },
        }
        typer.echo(json.dumps(data, indent=2))
        return

    rows = [
        [
            "/dev/kvm",
            "ok" if kvm_ok else "FAIL",
            "accessible" if kvm_ok else "not accessible",
        ],
        [
            "required binaries",
            "ok" if not missing else "FAIL",
            "all found" if not missing else f"missing: {', '.join(missing)}",
        ],
        ["ip_forward", "ok" if fwd_ok else "off", f"value={ip_fwd}"],
        [
            "state snapshot",
            "saved" if state else "none",
            CommonUtils.human_readable_datetime(state.initialized_at)
            if state
            else "no snapshot",
        ],
    ]
    print_table(columns=["Check", "Status", "Detail"], rows=rows)


@host_app.command(name="clean")
@handle_errors
def host_clean(
    force: bool = typer.Option(False, "--force", help="Skip confirmation"),
) -> None:
    """Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl or group."""
    _abort_if_vms_running("clean")

    if not force:
        print_warning(
            "This will remove all VM networking: bridges, TAP devices, iptables rules, "
            "and the default network configuration."
        )
        print_info(
            "Sysctl settings, sudoers, and the 'mvm' group will NOT be affected."
        )
        print_info("")
        typer.confirm("Proceed with host clean?", abort=True)

    cache_dir = CacheUtils.get_cache_dir()
    summary = HostOperation.clean(cache_dir)

    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                print_warning(f"  {item}")
            else:
                print_info(f"  {item}")

    print_success("Host cleaned successfully.")


@host_app.command(name="reset")
@handle_errors
def host_reset(
    force: bool = typer.Option(False, "--force", help="Skip confirmation"),
) -> None:
    """
    Full rollback: remove networking, revert sysctl, remove sudoers and group.

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
        print_info("")
        typer.confirm("Proceed with host reset?", abort=True)

    cache_dir = CacheUtils.get_cache_dir()
    summary = HostOperation.reset(cache_dir)

    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                print_warning(f"  {item}")
            else:
                print_info(f"  {item}")

    print_success("Host reset successfully.")


__all__ = ["host_app"]
