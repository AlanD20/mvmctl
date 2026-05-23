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

from mvmctl.constants import CLI_NAME, MVM_UNIX_GROUP, SUDOERS_DROP_IN_PATH
from mvmctl.models.result import ProgressEvent
from mvmctl.utils._system import run_cmd
from mvmctl.utils.cli import handle_errors, mvm_cli

init_app = typer.Typer(
    name="init",
    help=f"Initialize {CLI_NAME}",
    invoke_without_command=True,
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
    Spawn ``sudo host init`` with elevated privileges and return the completed process.

    Stderr is left attached to the terminal so the sudo password prompt is
    visible.
    """
    mvm_bin = shutil.which(CLI_NAME) or sys.argv[0]

    # Build env var assignments for the 'env' utility.
    # We use 'sudo env VAR=val command' instead of 'sudo -E command'
    # because 'sudo -E' requires sudoers to allow environment preservation,
    # which is not guaranteed on all systems.
    env_assignments: list[str] = ["MVM_ESCALATED=1"]
    for key in ("MVM_CONFIG_DIR", "MVM_CACHE_DIR", "HOME", "PATH"):
        if key in os.environ:
            env_assignments.append(f"{key}={os.environ[key]}")

    mvm_cli.info("")
    mvm_cli.info("Running host init with sudo...")
    return run_cmd(
        ["sudo", "env", *env_assignments, mvm_bin, "host", "init"],
        check=False,
        capture=False,
    )


def _check_host_state() -> dict[str, bool]:
    """Check current host setup state.

    Returns:
        Dict with keys: group_exists, sudoers_exists, user_in_group.
        ``session_has_group`` is now provided by the API layer
        via ``NeedsInteraction.context``.
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

    Compares state snapshots taken before and after ``sudo host init``
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
    skip_network: bool,
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
                skip_network=skip_network,
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
                    skip_network=skip_network,
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
            # Run pre-flight probes before prompting for sudo
            try:
                from mvmctl.api.host_operations import HostOperation

                probe_result = HostOperation.check_readiness()
                if probe_result.has_critical:
                    mvm_cli.warning("Pre-flight checks found issues:")
                    for check in probe_result.critical:
                        mvm_cli.warning(f"  {check.name}: {check.message}")
                    if not typer.confirm(
                        "Continue with host init? Some features may not work.",
                        default=False,
                    ):
                        mvm_cli.info("Aborted")
                        raise typer.Exit(code=1)
                if probe_result.warnings:
                    for check in probe_result.warnings:
                        mvm_cli.info(f"  {check.name}: {check.message}")
            except Exception:
                pass

            host_state_before = _check_host_state()

            # Group exists and user is a member, but the current session
            # doesn't have the group GID active (user hasn't logged out/in
            # after being added to the group).  Running sudo here would
            # succeed, but the user would still need to start a new session
            # before mvm works without sudo — so skip the sudo prompt and
            # guide them instead.
            # The API returns session_has_group in the NeedsInteraction
            # context so we don't need a separate os.getgroups() call.
            session_has_group = interaction.context.get(
                "session_has_group", False
            )
            if (
                host_state_before["group_exists"]
                and host_state_before["user_in_group"]
                and not session_has_group
            ):
                mvm_cli.warning(
                    f"mvm group — session not active "
                    f"(log out and back in, or run: newgrp {MVM_UNIX_GROUP})"
                )
                skip_host = True
                continue

            if host_state_before["group_exists"]:
                mvm_cli.warning("sudoers file is missing")
                mvm_cli.info(f"run:  sudo {CLI_NAME} host init")
            else:
                mvm_cli.warning("this requires sudo once")
                mvm_cli.info(
                    f"creates the {MVM_UNIX_GROUP} group and sudoers drop-in for "
                    "passwordless sudo on future runs"
                )

            if non_interactive:
                mvm_cli.info(
                    f"Run 'sudo {CLI_NAME} host init' manually."
                )
                break
            proceed_with_sudo = typer.confirm(
                f"Run 'sudo {CLI_NAME} host init' now?", default=True
            )
            if proceed_with_sudo:
                proc = _run_with_sudo()
                if proc.returncode != 0:
                    mvm_cli.warning(
                        f"host init failed. Run 'sudo {CLI_NAME} host init' manually."
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
                mvm_cli.info(
                    f"skipped. Run 'sudo {CLI_NAME} host init' manually when ready."
                )
                break

        # ── Handle binary download confirmation ────────────────────────
        if interaction.code == "binary.confirm_download":
            latest = interaction.context.get("latest_version", "")
            if not latest:
                mvm_cli.warning(
                    "no Firecracker binary found and no remote versions available."
                )
                break

            mvm_cli.info(f"latest available: v{latest}")

            if non_interactive or typer.confirm(
                f"Download v{latest}?", default=True
            ):
                mvm_cli.info("")
                mvm_cli.info(f"downloading Firecracker v{latest} ...")
                download_version = latest
                # Don't wrap download in spinner — BinaryOperation.pull
                # has its own ASCIIProgressBar.
                continue  # Re-run with download_version set
            else:
                mvm_cli.info(
                    f"skipped. Run '{CLI_NAME} bin pull <version>' manually."
                )
                break

        # ── Handle guestfs enable prompt ──────────────────────────────
        if interaction.code == "guestfs.confirm_enable":
            if non_interactive:
                guestfs_enabled = False
            else:
                guestfs_enabled = typer.confirm(
                    "Enable libguestfs as a provisioning fallback?",
                    default=False,
                )
            continue  # Re-run with guestfs_enabled set

        # Unknown interaction — stop
        mvm_cli.warning(f"unhandled interaction: {interaction.code}")
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
    skip_network: bool = typer.Option(
        False, "--skip-network", help="Skip default network creation"
    ),
) -> None:
    f"""Initialize {CLI_NAME} host, network, and binary — run this to get started."""
    mvm_cli.info("")
    mvm_cli.info(f"{CLI_NAME} init — first-time setup")
    mvm_cli.info("─" * 40)

    result = _handle_interactive_flow(
        skip_host=skip_host,
        skip_network=skip_network,
        non_interactive=non_interactive,
    )

    # Print step results — compact one-liners
    step_labels = {
        "local_state": "Local State",
        "service_binaries": "Service Binaries",
        "host": f"sudoers / {MVM_UNIX_GROUP} group",
        "network_setup": "Network Setup (Sync + Default)",
        "guestfs": "libguestfs",
        "cache": "Cache Directories",
        "binary": "Firecracker Binary",
    }
    mvm_cli.info("")
    for step in result.steps:
        label = step_labels.get(step.step, step.step)
        if step.success:
            if step.message:
                mvm_cli.success(f"{label}  ({step.message})")
            else:
                mvm_cli.success(label)
        else:
            mvm_cli.warning(f"{label} — {step.message}")

    # Missing steps (if any were skipped due to early return)
    present = {s.step for s in result.steps}
    for key, label in step_labels.items():
        if key not in present:
            mvm_cli.warning(f"{label} — not checked")

    mvm_cli.info("")
    if result.host_ready:
        mvm_cli.success("all set")
    else:
        mvm_cli.warning(f"setup incomplete — run '{CLI_NAME} init' again")
        raise typer.Exit(code=1)


__all__ = ["init_app"]
