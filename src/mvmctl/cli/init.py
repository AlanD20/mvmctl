"""Guided onboarding wizard — thin CLI wrapper around InitOperation."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys

import typer

from mvmctl.api import InitOperation, InitResult
from mvmctl.utils._io import print_info, print_success, print_warning
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
    """Spawn ``sudo mvm host init`` and return the completed process.

    Stderr is left attached to the terminal so the sudo password prompt is
    visible.  Stdout is captured to avoid interleaving with the wizard UI.
    """
    mvm_bin = shutil.which("mvm") or sys.argv[0]
    env = os.environ.copy()
    env["MVM_ESCALATED"] = "1"
    print_info("")
    print_info("Running host init with sudo...")
    return subprocess.run(
        ["sudo", "-E", mvm_bin, "host", "init"],
        env=env,
        stdout=None,
        stderr=None,
        text=True,
    )


def _handle_interactive_flow(
    skip_host: bool,
    non_interactive: bool,
) -> InitResult:
    """Drive the init wizard, handling sudo and download prompts in the CLI."""
    sudo_was_completed = False
    result = InitOperation.run(
        skip_host=skip_host,
        non_interactive=non_interactive,
    )

    # ── Handle sudo prompt for host init ────────────────────────────────
    host_step = next((s for s in result.steps if s.step == "host"), None)
    if (
        host_step
        and host_step.needs_interaction
        and host_step.interaction_type == "sudo"
    ):
        if non_interactive:
            print_warning(
                "Host init requires root privileges. Run: sudo mvm host init"
            )
        else:
            print_warning(
                "This requires sudo once to create the 'mvm' group "
                "and sudoers drop-in."
            )
            print_info("After this, you won't need sudo for any mvm commands.")
            if typer.confirm("Run 'sudo mvm host init' now?", default=True):
                proc = _run_with_sudo()
                if proc.returncode != 0:
                    print_warning(
                        "Host init failed. Run 'sudo mvm host init' manually."
                    )
                    if proc.stdout:
                        print_warning(proc.stdout.strip())
                else:
                    sudo_was_completed = True
                    result = InitOperation.run(
                        skip_host=skip_host,
                        non_interactive=non_interactive,
                        sudo_completed=True,
                    )
            else:
                print_info(
                    "Skipped. Run 'sudo mvm host init' manually when ready."
                )

    # ── Handle download prompt for binary ───────────────────────────────
    binary_step = next((s for s in result.steps if s.step == "binary"), None)
    if (
        binary_step
        and binary_step.needs_interaction
        and binary_step.interaction_type == "confirm_download"
    ):
        latest = binary_step.interaction_data.get("latest_version", "")
        print_info(f"Latest available: v{latest}")
        if non_interactive or typer.confirm(
            f"Download v{latest}?", default=True
        ):
            print_info("")
            print_info(f"Downloading Firecracker v{latest}...")
            result = InitOperation.run(
                skip_host=skip_host,
                non_interactive=non_interactive,
                sudo_completed=sudo_was_completed,
                download_version=latest,
            )
        else:
            print_info("Skipped. Run 'mvm bin fetch <version>' manually.")

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
    print_info("mvm — Setup Wizard")
    print_info("=" * 40)

    result = _handle_interactive_flow(
        skip_host=skip_host,
        non_interactive=non_interactive,
    )

    # Print step results
    step_labels = {
        "local_state": "Local state",
        "host": "Host privileges",
        "cache": "Cache directories",
        "binary": "Firecracker binary",
    }
    print_info("")
    for step in result.steps:
        label = step_labels.get(step.step, step.step)
        if step.success:
            print_success(f"{label}: {step.message}")
        else:
            print_warning(f"{label}: {step.message}")

    # Missing steps (if any were skipped due to early return)
    present = {s.step for s in result.steps}
    for key, label in step_labels.items():
        if key not in present:
            print_warning(f"{label}: not checked")

    print_info("")
    if result.host_ready:
        print_success("Host ready!")
    else:
        print_warning("Host setup incomplete. Run 'mvm init' again.")


__all__ = ["init_app"]
