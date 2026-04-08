"""Guided onboarding wizard — collapses first-time setup into a single flow."""

from __future__ import annotations

from pathlib import Path

import typer

from mvmctl.api.assets import (
    ensure_default_binary,
    fetch_binary,
    list_local_versions,
    list_remote_versions,
    set_active_version,
)
from mvmctl.api.config import initialize_default_config
from mvmctl.api.host import check_kvm_access, get_host_state, init_host
from mvmctl.exceptions import BinaryError, HostError, MVMError
from mvmctl.utils.console import print_info, print_success, print_warning
from mvmctl.utils.fs import get_cache_dir

app = typer.Typer(
    help="Initialize mvm",
    rich_markup_mode=None,
    add_completion=False,
)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the init command."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


def _run_host_init_noninteractive(cache_dir: Path) -> None:
    """Run host initialisation without prompts (for --non-interactive mode)."""
    try:
        init_host(cache_dir)
        print_success("  Host initialized")
    except HostError as e:
        print_warning(f"  Host init failed: {e}")
    try:
        from mvmctl.api.network import ensure_default_network

        ensure_default_network()
        print_success("  Default network ready")
    except MVMError:
        pass


def _run_host_init_interactive() -> None:
    """Prompt the user and run host initialisation interactively."""
    if typer.confirm("  Run 'sudo mvm host init' now?", default=True):
        import os
        import shutil
        import subprocess
        import sys

        mvm_bin = shutil.which("mvm") or sys.argv[0]
        env = os.environ.copy()
        # Signal to the subprocess that this escalation was user-prompted so
        # the "running as root" warning is suppressed.
        env["MVM_ESCALATED"] = "1"
        result = subprocess.run(["sudo", "-E", mvm_bin, "host", "init"], env=env)
        if result.returncode != 0:
            print_warning("  Host init failed. Run 'sudo mvm host init' manually.")
        # The subprocess already printed all status lines — no duplicate output here.
    else:
        print_info("  Skipped. Run 'sudo mvm host init' manually when ready.")


def _step_host(skip: bool, non_interactive: bool) -> None:
    """Step 1: Privilege setup and host initialisation."""
    print_info("\n[1/3] Privilege setup")
    if skip:
        print_info("  Skipped (--skip-host)")
        return

    cache_dir = get_cache_dir()
    state = None
    try:
        state = get_host_state(cache_dir)
    except HostError:
        pass

    kvm_ok = check_kvm_access()

    if state and kvm_ok:
        # Even if host state looks healthy, ensure network is materialized
        # (bridge, iptables chains, NAT may be missing after reboot)
        try:
            from mvmctl.api.network import ensure_default_network

            ensure_default_network()
            print_success("  Host and network ready")
        except MVMError as e:
            print_warning(f"  Network check failed: {e}")
        return

    if not kvm_ok:
        print_warning("  /dev/kvm is not accessible")

    print_info("  This requires sudo once to create the 'mvm' group and sudoers drop-in.")
    print_info("  After this, you won't need sudo for any mvm commands.")

    if non_interactive:
        _run_host_init_noninteractive(cache_dir)
    else:
        _run_host_init_interactive()


def _step_cache_init() -> None:
    from mvmctl.api.cache import init_all

    print_info("\n[2/4] Cache init")
    try:
        result = init_all()
        guestfs_built = bool(result.get("guestfs_appliance"))
        if guestfs_built:
            print_success("  Cache directories ready (libguestfs appliance built)")
        else:
            print_success("  Cache directories ready")
    except Exception as e:
        print_warning(f"  Cache init failed: {e}")


def _step_local_state() -> None:
    try:
        from mvmctl.api.init import init_database

        init_database()
        print_success("  Local state ready")
    except Exception:
        from mvmctl.utils.fs import get_mvm_db_path

        state_file = get_mvm_db_path()
        print_warning("  Local state initialization failed.")
        print_warning("  To recover, remove the file and run 'mvm init' again:")
        print_warning(f"    rm {state_file}")
        print_warning(
            "  WARNING: Removing this file will permanently delete all existing state data."
        )


def _step_binary(non_interactive: bool) -> None:
    """Step 3: Binary download."""
    print_info("\n[3/4] Firecracker binary")

    local = list_local_versions()
    if local:
        active = [v for v in local if v.is_active]
        if active:
            print_success(f"  Binary available (v{active[0].version})")
        else:
            repaired = ensure_default_binary()
            if repaired:
                print_success(f"  Binary available (v{repaired}) — set as default")
            else:
                print_success(f"  Binary available (v{local[0].version})")
        return

    if non_interactive:
        try:
            versions = list_remote_versions(limit=1)
            if versions:
                bv = fetch_binary(versions[0])
                set_active_version(bv.version)
                print_success(f"  Downloaded v{bv.version}")
            else:
                print_warning("  No remote versions found")
        except BinaryError as e:
            print_warning(f"  Download failed: {e}")
        return

    print_info("  No Firecracker binary found in cache.")
    try:
        versions = list_remote_versions(limit=5)
    except BinaryError:
        print_warning("  Could not list remote versions.")
        print_info("  Run 'mvm bin fetch <version>' manually.")
        return

    if not versions:
        print_warning("  No remote versions available.")
        return

    print_info(f"  Latest available: {versions[0]}")
    if typer.confirm(f"  Download v{versions[0]}?", default=True):
        try:
            bv = fetch_binary(versions[0])
            set_active_version(bv.version)
            print_success(f"  Downloaded v{bv.version}")
        except BinaryError as e:
            print_warning(f"  Download failed: {e}")
    else:
        print_info("  Skipped. Run 'mvm bin fetch <version>' manually.")


def _step_summary() -> None:
    """Step 3: Print summary."""
    print_info("\n[4/4] Summary")

    cache_dir = get_cache_dir()

    # Check host and network only
    checks: list[tuple[str, bool]] = []

    try:
        state = get_host_state(cache_dir)
        checks.append(("Host init", state is not None))
    except HostError:
        checks.append(("Host init", False))

    # Check default network
    from mvmctl.api.network import list_networks

    networks = list_networks()
    default_net = next((n for n in networks if n.name == "default"), None)
    checks.append(("Default network", default_net is not None))

    all_ok = True
    for label, ok in checks:
        if ok:
            print_success(f"  {label}")
        else:
            print_warning(f"  {label}: missing")
            all_ok = False

    if all_ok:
        print_info("")
        print_success("Host ready!")
    else:
        print_info("")
        print_warning("Host setup incomplete. Run 'mvm init' again.")


@app.callback(invoke_without_command=True)
def init_run(
    non_interactive: bool = typer.Option(
        False, "--non-interactive", help="Use defaults, skip prompts"
    ),
    skip_host: bool = typer.Option(False, "--skip-host", help="Skip host init step"),
) -> None:
    """Initialize mvm host, network, and binary — run this to get started.

    Performs host privilege setup, downloads the Firecracker binary if missing,
    and ensures the default network is materialized (bridge, iptables chains, NAT).
    This is a focused one-time setup command that downloads the Firecracker binary
    if missing, but does NOT download kernels, images, or create SSH keys.

    After init, run these separately:
        mvm kernel fetch
        mvm image fetch <id>
        mvm key create <name>

    Examples:
        mvm init
        mvm init --non-interactive
        mvm init --skip-host --non-interactive
    """
    print_info("mvm — Setup Wizard")
    print_info("=" * 40)

    initialize_default_config()

    # DB migration must run before host/network steps so SQLite tables exist
    # when host_setup.py and network_manager.py attempt dual-writes.
    _step_local_state()
    _step_host(skip=skip_host, non_interactive=non_interactive)
    _step_cache_init()
    _step_binary(non_interactive)
    _step_summary()
