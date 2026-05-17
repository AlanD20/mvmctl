"""Host configuration commands."""

from __future__ import annotations

import json
import os
import sys
from typing import TYPE_CHECKING, Any

import typer

from mvmctl.api import HostOperation as _HostOperation

if TYPE_CHECKING:
    from mvmctl.api.host_operations import HostOperation
else:
    HostOperation = _HostOperation
from mvmctl.constants import CLI_NAME, MVM_UNIX_GROUP
from mvmctl.exceptions import HostError, PrivilegeError
from mvmctl.models.result import NeedsInteraction, OperationResult
from mvmctl.utils._io import (
    print_error,
    print_info,
    print_success,
    print_table,
    print_warning,
)
from mvmctl.utils._system import run_cmd
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
        print_error(f"{action} blocked: VMs still running: {names}")
        print_error("Stop all VMs first: mvm vm stop <name>")
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
@handle_errors
def host_init() -> None:
    f"""
    Apply host configuration changes. Idempotent.

    This command must be run with sudo the first time. It performs the
    following steps:

    - Creates the '{MVM_UNIX_GROUP}' system group and adds the current user to it.
    - Installs a sudoers drop-in so group members can manage TAP devices,
      bridges, and iptables rules without a password.
    - Enables IP forwarding (net.ipv4.ip_forward=1).
    - Snapshots the pre-change host state so '{CLI_NAME} host reset' can roll back.
    - Creates the default network bridge.

    After running, log out and back in (or run ``newgrp mvm``) for group
    membership to take effect.

    Examples:
        sudo mvm host init

    """
    cache_dir = CacheUtils.get_cache_dir()
    try:
        result = HostOperation.init(cache_dir)
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
        # Keep HostError handling for errors from sub-calls within init
        # (e.g., NetworkOperation called internally)
        print_error(f"Host init failed: {e}")
        raise typer.Exit(code=1) from e

    if isinstance(result, NeedsInteraction):
        if result.code == "privilege.sudo_required":
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
                    # Build env var assignments for the 'env' utility.
                    # We use 'sudo env VAR=val command' instead of relying on
                    # 'sudo -E' because 'sudo -E' requires sudoers to allow
                    # environment preservation, which is not guaranteed.
                    env_assignments: list[str] = [
                        "MVM_SUDO_RESTART=1",
                        "MVM_ESCALATED=1",
                    ]
                    for key in (
                        "MVM_CONFIG_DIR",
                        "MVM_CACHE_DIR",
                        "HOME",
                        "PATH",
                    ):
                        if key in os.environ:
                            env_assignments.append(f"{key}={os.environ[key]}")
                    run_cmd(
                        ["sudo", "env", *env_assignments, *sys.argv],
                        check=False,
                        capture=False,
                    )
                except FileNotFoundError:
                    print_error("sudo command not found")
            raise typer.Exit(code=1)
        # Unknown interaction code
        print_error(result.message)
        raise typer.Exit(code=1)

    if not isinstance(result, OperationResult):
        print_error(f"Unexpected result type: {type(result).__name__}")
        raise typer.Exit(code=1)

    if result.status == "success":
        changes = result.metadata.get("changes", [])
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
                f"Initialized: host ({applied_changes} change(s) applied)"
            )

        was_user_added = result.metadata.get("user_added_to_group", False)
        if was_user_added:
            print_warning(
                "Log out and back in for group membership to take effect"
            )
            print_info(f"Or run immediately: newgrp {MVM_UNIX_GROUP}")
    elif result.status == "skipped":
        print_info(result.message)
    elif result.status in ("error", "failure"):
        print_error(result.message)
        raise typer.Exit(code=1)

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


@host_app.command(name="info")
@handle_errors
def host_info(
    refresh: bool = typer.Option(
        False, "--refresh", help="Re-detect host hardware and limits"
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show host hardware, limits, and VM capacity projection.

    Displays detected CPU, memory, storage, kernel limits, current resource
    usage, and a recommended maximum VM count based on available resources.

    Use --refresh to re-detect hardware and limits before displaying.
    """
    if refresh:
        result = HostOperation.refresh_capacity()
    else:
        result = HostOperation.info()

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    item = result.item
    if item is None:
        print_error("No host info available.")
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(item, indent=2, default=str))
    else:
        _print_info_human(item)


def _print_info_human(info: dict[str, Any]) -> None:
    """Print host info in human-readable format."""
    typer.echo("Host:")
    typer.echo(f"  Hostname:           {info.get('hostname', 'N/A')}")
    typer.echo(f"  Detected at:        {info.get('detected_at', 'N/A')}")

    os_info = info.get("os", {})
    typer.echo("OS:")
    typer.echo(f"  Kernel:             {os_info.get('kernel', 'N/A')}")
    typer.echo(f"  Release:            {os_info.get('release', 'N/A')}")

    cpu_info = info.get("cpu", {})
    typer.echo("CPU:")
    typer.echo(f"  Model:              {cpu_info.get('model', 'N/A')}")
    typer.echo(f"  Vendor:             {cpu_info.get('vendor', 'N/A')}")
    typer.echo(f"  Cores:              {cpu_info.get('cores', 'N/A')}")
    typer.echo(f"  Architecture:       {cpu_info.get('architecture', 'N/A')}")
    typer.echo(f"  NUMA nodes:         {cpu_info.get('numa_nodes', 'N/A')}")

    mem_info = info.get("memory", {})
    typer.echo("Memory:")
    total_mib = mem_info.get("total_mib", 0)
    avail_mib = mem_info.get("available_mib", 0)
    if isinstance(total_mib, (int, float)) and isinstance(
        avail_mib, (int, float)
    ):
        typer.echo(f"  Total:              {total_mib:,} MiB")
        typer.echo(f"  Available:          {avail_mib:,} MiB")
        if total_mib > 0:
            pct = int((total_mib - avail_mib) / total_mib * 100)
            typer.echo(f"  Used:               {pct}%")

    storage_info = info.get("storage", {})
    typer.echo("Storage:")
    total_bytes = storage_info.get("total_bytes", 0)
    free_bytes = storage_info.get("free_bytes", 0)
    if isinstance(total_bytes, (int, float)) and isinstance(
        free_bytes, (int, float)
    ):
        typer.echo(
            f"  Total:              {CommonUtils.format_bytes_human_readable(int(total_bytes))}"
        )
        typer.echo(
            f"  Free:               {CommonUtils.format_bytes_human_readable(int(free_bytes))}"
        )

    limits_info = info.get("limits", {})
    typer.echo("Limits:")
    typer.echo(f"  PID max:            {limits_info.get('pid_max', 'N/A'):,}")
    typer.echo(f"  FD max:             {limits_info.get('fd_max', 'N/A'):,}")
    port_range = limits_info.get("ip_local_port_range", [])
    if isinstance(port_range, list) and len(port_range) == 2:
        typer.echo(f"  Ephemeral ports:    {port_range[0]}-{port_range[1]}")
    cmax = limits_info.get("conntrack_max", 0)
    tmax = limits_info.get("tap_devices_max", 0)
    typer.echo(
        f"  Conntrack max:      {cmax:,}"
        if cmax
        else "  Conntrack:          N/A (module not loaded)"
    )
    typer.echo(
        f"  TAP max devices:    {tmax:,}"
        if tmax
        else "  TAP devices:        Unlimited"
    )

    cap_info = info.get("capacity", {})
    current = cap_info.get("current", {})
    typer.echo("Capacity:")
    typer.echo(f"  PIDs in use:        {current.get('pids', 0):,}")
    typer.echo(f"  FDs in use:         {current.get('fds', 0):,}")
    typer.echo(f"  Conntrack in use:   {current.get('conntrack', 0):,}")
    typer.echo(f"  TAP devices used:   {current.get('tap_devices', 0)}")
    typer.echo(f"  ARP entries:        {current.get('arp_entries', 0)}")
    rec_vms = cap_info.get("recommended_max_vms", "N/A")
    lim_res = cap_info.get("limiting_resource")
    if lim_res:
        typer.echo(f"  Recommended VMs:    {rec_vms}  (bottleneck: {lim_res})")
    else:
        typer.echo(f"  Recommended VMs:    {rec_vms}")

    setup_info = info.get("setup", {})
    typer.echo("Setup:")
    initialized = setup_info.get("initialized", False)
    typer.echo(f"  Initialized:        {'Yes' if initialized else 'No'}")
    if initialized:
        init_at = setup_info.get("initialized_at", "")
        if init_at:
            try:
                from mvmctl.utils.common import CommonUtils as _CommonUtils

                human = _CommonUtils.human_readable_datetime(init_at)
                typer.echo(f"  Initialized at:     {human}")
            except Exception:
                typer.echo(f"  Initialized at:     {init_at}")


@host_app.command(name="clean")
@handle_errors
def host_clean(
    force: bool = typer.Option(
        False, "--force", "-f", help="Skip confirmation"
    ),
) -> None:
    """Remove all networking config (bridges, TAPs, iptables). Does not touch sysctl or group."""
    _abort_if_vms_running("clean")

    if not force:
        print_warning(
            "This will remove all VM networking: bridges, TAP devices, iptables rules, "
            "and the default network configuration."
        )
        print_info(
            f"Sysctl settings, sudoers, and the '{MVM_UNIX_GROUP}' group will NOT be affected."
        )
        print_info("")
        if not typer.confirm("Proceed with host clean?"):
            print_info("Aborted")
            raise typer.Exit(code=0)

    cache_dir = CacheUtils.get_cache_dir()
    result = HostOperation.clean(cache_dir)

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    summary = result.item or []
    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                remainder = item[len("Warning:") :].strip()
                print_warning(f"  {remainder}")
            else:
                print_info(f"  {item}")

    print_success(result.message)


@host_app.command(name="reset")
@handle_errors
def host_reset(
    force: bool = typer.Option(
        False, "--force", "-f", help="Skip confirmation"
    ),
) -> None:
    f"""
    Full rollback: remove networking, revert sysctl, remove sudoers and group.

    Reverts every change made by '{CLI_NAME} host init':

    - Tears down all network bridges, TAP devices, and iptables rules.
    - Restores the original sysctl ip_forward value.
    - Removes the sudoers drop-in file.
    - Removes the '{MVM_UNIX_GROUP}' system group.

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
        if not typer.confirm("Proceed with host reset?"):
            print_info("Aborted")
            raise typer.Exit(code=0)

    cache_dir = CacheUtils.get_cache_dir()
    result = HostOperation.reset(cache_dir)

    if result.is_error:
        print_error(result.message)
        raise typer.Exit(code=1)

    summary = result.item or []
    if summary:
        for item in summary:
            if item.startswith("Warning:"):
                remainder = item[len("Warning:") :].strip()
                print_warning(f"  {remainder}")
            else:
                print_info(f"  {item}")

    print_success(result.message)


__all__ = ["host_app"]
