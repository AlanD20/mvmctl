"""Guided onboarding wizard — thin CLI wrapper around InitOperation."""

from __future__ import annotations

import os
import pwd
import shutil
import subprocess
import sys
from pathlib import Path
from typing import TYPE_CHECKING

import typer
from rich.console import Console
from rich.markup import escape as rich_escape

from mvmctl.api import InitOperation as _InitOperation
from mvmctl.api import InitResult as _InitResult

if TYPE_CHECKING:
    from mvmctl.api.init_operations import InitOperation, InitResult
else:
    InitOperation = _InitOperation
    InitResult = _InitResult

from mvmctl.constants import MVM_UNIX_GROUP, SUDOERS_DROP_IN_PATH
from mvmctl.models.result import ProgressEvent
from mvmctl.utils._io import print_info, print_success, print_warning
from mvmctl.utils._system import run_cmd
from mvmctl.utils.cli import handle_errors

init_app = typer.Typer(
    name="init",
    help="Initialize mvm",
    invoke_without_command=True,
    rich_markup_mode=None,
    add_completion=False,
)


@init_app.command(name="help", hidden=True)
@handle_errors
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the init command."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


def _run_with_sudo() -> subprocess.CompletedProcess[str]:
    """
    Spawn ``sudo mvm host init`` and return the completed process.

    Stderr is left attached to the terminal so the sudo password prompt is
    visible.
    """
    mvm_bin = shutil.which("mvm") or sys.argv[0]

    # Build env var assignments for the 'env' utility.
    # We use 'sudo env VAR=val command' instead of 'sudo -E command'
    # because 'sudo -E' requires sudoers to allow environment preservation,
    # which is not guaranteed on all systems.
    env_assignments: list[str] = ["MVM_ESCALATED=1"]
    for key in ("MVM_CONFIG_DIR", "MVM_CACHE_DIR", "HOME", "PATH"):
        if key in os.environ:
            env_assignments.append(f"{key}={os.environ[key]}")

    print_info("")
    print_info("Running host init with sudo...")
    return run_cmd(
        ["sudo", "env", *env_assignments, mvm_bin, "host", "init"],
        check=False,
        capture=False,
    )


def _check_host_state() -> dict[str, bool]:
    """Check current host setup state.

    Returns:
        Dict with keys: group_exists, sudoers_exists, user_in_group.
        Permission errors are silently caught (we're likely not root).
    """
    state: dict[str, bool] = {
        "group_exists": False,
        "sudoers_exists": False,
        "user_in_group": False,
    }

    try:
        import grp

        g = grp.getgrnam(MVM_UNIX_GROUP)
        state["group_exists"] = True
        username = pwd.getpwuid(os.getuid()).pw_name
        state["user_in_group"] = username in g.gr_mem
    except (KeyError, PermissionError):
        pass

    try:
        sudoers_path = Path(SUDOERS_DROP_IN_PATH)
        state["sudoers_exists"] = sudoers_path.exists()
    except PermissionError:
        pass

    return state


def _compose_host_setup_message(
    before: dict[str, bool], after: dict[str, bool]
) -> str:
    """Compose a human-readable message about what host setup changed.

    Compares state snapshots taken before and after ``sudo mvm host init``
    to determine what was actually configured.
    """
    parts: list[str] = []

    if not before["group_exists"] and after["group_exists"]:
        parts.append("group created")
    if not before["sudoers_exists"] and after["sudoers_exists"]:
        parts.append("sudoers configured")
    if not before["user_in_group"] and after["user_in_group"]:
        parts.append("user added to group")

    if parts:
        return "Host " + ", ".join(parts)

    # Nothing changed — everything was already set up
    return "Host already configured"


def _handle_interactive_flow(
    skip_host: bool,
    non_interactive: bool,
) -> InitResult:
    """Drive the init wizard, handling sudo and download prompts in the CLI."""
    console = Console()
    sudo_was_completed = False
    download_version: str | None = None
    host_setup_message: str | None = None
    guestfs_enabled: bool | None = None
    result: InitResult

    while True:
        # When downloading a binary, don't wrap in Rich spinner —
        # BinaryOperation.pull uses ASCIIProgressBar which needs direct
        # terminal access (\r carriage return). A concurrent Live display
        # (console.status) would interfere with the cursor tracking.
        if download_version:
            result = InitOperation.run(
                skip_host=skip_host,
                non_interactive=non_interactive,
                on_progress=None,
                sudo_completed=sudo_was_completed,
                host_setup_message=host_setup_message,
                download_version=download_version,
                guestfs_enabled=guestfs_enabled,
            )
        else:
            with console.status("", spinner="dots") as status:

                def _on_progress(event: ProgressEvent) -> None:
                    if event.message:
                        status.update(
                            f"[dim]{rich_escape(event.message)}[/dim]"
                        )

                result = InitOperation.run(
                    skip_host=skip_host,
                    non_interactive=non_interactive,
                    on_progress=_on_progress,
                    sudo_completed=sudo_was_completed,
                    host_setup_message=host_setup_message,
                    download_version=download_version,
                    guestfs_enabled=guestfs_enabled,
                )

        if result.needs_interaction is None:
            break

        interaction = result.needs_interaction

        # ── Handle sudo escalation ─────────────────────────────────────
        if interaction.code == "privilege.sudo_required":
            host_state_before = _check_host_state()

            if (
                host_state_before["group_exists"]
                and host_state_before["user_in_group"]
            ):
                print_warning("group active, but not in this session")
                print_info("run:  newgrp mvm")
            elif host_state_before["group_exists"]:
                print_warning("sudoers file is missing")
                print_info("run:  sudo mvm host init")
            else:
                print_warning("this requires sudo once")
                print_info(
                    "creates the mvm group and sudoers drop-in for "
                    "passwordless sudo on future runs"
                )

            if non_interactive:
                print_warning(
                    "Host init requires root privileges. Run: sudo mvm host init"
                )
                break

            if typer.confirm("Run 'sudo mvm host init' now?", default=True):
                proc = _run_with_sudo()
                if proc.returncode != 0:
                    print_warning(
                        "host init failed. Run 'sudo mvm host init' manually."
                    )
                    break

                # Determine what the sudo subprocess actually did
                host_state_after = _check_host_state()
                host_setup_message = _compose_host_setup_message(
                    host_state_before, host_state_after
                )

                sudo_was_completed = True
                download_version = None
                # Re-run with sudo completed and host_setup_message so
                # the wizard summary reflects actual changes
                continue
            else:
                print_info(
                    "skipped. Run 'sudo mvm host init' manually when ready."
                )
                break

        # ── Handle binary download confirmation ────────────────────────
        if interaction.code == "binary.confirm_download":
            latest = interaction.context.get("latest_version", "")
            if not latest:
                print_warning(
                    "no Firecracker binary found and no remote versions available."
                )
                break

            print_info(f"latest available: v{latest}")

            if non_interactive or typer.confirm(
                f"Download v{latest}?", default=True
            ):
                print_info("")
                print_info(f"downloading Firecracker v{latest} ...")
                download_version = latest
                # Don't wrap download in spinner — BinaryOperation.pull
                # has its own ASCIIProgressBar.
                continue  # Re-run with download_version set
            else:
                print_info("skipped. Run 'mvm bin pull <version>' manually.")
                break

        # ── Handle guestfs enable prompt ──────────────────────────────
        if interaction.code == "guestfs.confirm_enable":
            if non_interactive or typer.confirm(
                "Enable libguestfs as a provisioning fallback?",
                default=False,
            ):
                guestfs_enabled = True
            else:
                guestfs_enabled = False
            continue  # Re-run with guestfs_enabled set

        # Unknown interaction — stop
        print_warning(f"unhandled interaction: {interaction.code}")
        break

    return result


@init_app.callback(invoke_without_command=True)
@handle_errors
def init_run(
    non_interactive: bool = typer.Option(
        False, "--non-interactive", help="Use defaults, skip prompts"
    ),
    skip_host: bool = typer.Option(
        False, "--skip-host", help="Skip host init step"
    ),
) -> None:
    """Initialize mvm host, network, and binary — run this to get started."""
    print_info("")
    print_info("mvm init — first-time setup")
    print_info("─" * 40)

    result = _handle_interactive_flow(
        skip_host=skip_host,
        non_interactive=non_interactive,
    )

    # Print step results — compact one-liners
    step_labels = {
        "local_state": "local state",
        "service_binaries": "service binaries",
        "host": "sudoers / mvm group",
        "guestfs": "libguestfs",
        "cache": "cache directories",
        "binary": "firecracker binary",
    }
    print_info("")
    for step in result.steps:
        label = step_labels.get(step.step, step.step)
        if step.success:
            if step.message:
                print_success(f"{label}  ({step.message})")
            else:
                print_success(label)
        else:
            print_warning(f"{label} — {step.message}")

    # Missing steps (if any were skipped due to early return)
    present = {s.step for s in result.steps}
    for key, label in step_labels.items():
        if key not in present:
            print_warning(f"{label} — not checked")

    print_info("")
    if result.host_ready:
        print_success("all set")
    else:
        print_warning("setup incomplete — run 'mvm init' again")
        raise typer.Exit(code=1)


__all__ = ["init_app"]
