"""Binary (Firecracker) management commands."""

from __future__ import annotations

import json
from pathlib import Path
from typing import List, Optional

import typer

from mvmctl.api.assets import (
    BinaryVersion,
    fetch_binary,
    list_binaries,
    list_local_versions,
    list_remote_versions,
    remove_version,
    set_active_version,
)
from mvmctl.exceptions import AssetNotFoundError, BinaryError
from mvmctl.utils.console import (
    get_combined_marker,
    print_error,
    print_success,
    print_table,
    print_warning,
)

bin_app = typer.Typer(
    help="Binary management",
    no_args_is_help=False,
    rich_markup_mode=None,
    add_completion=False,
)


def _format_bin_row(bv: BinaryVersion, is_missing: bool = False) -> list[str]:
    version = get_combined_marker(bv.is_active, is_missing) + bv.version
    return [version, str(bv.firecracker_path)]


@bin_app.command(name="ls")
def bin_ls(
    remote: bool = typer.Option(False, "--remote", "-r", help="Also show remote versions"),
    limit: int = typer.Option(None, "--limit", help="Max remote versions to show"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    local = list_local_versions()
    local_versions = {bv.version for bv in local}

    # Get all binary entries from metadata (includes missing files)
    all_entries = list_binaries()
    missing_binaries: list[tuple[str, Path]] = []
    present_binaries: list[tuple[str, Path, bool]] = []  # (version, path, is_default)

    for entry in all_entries:
        if not entry.file_exists and entry.version not in local_versions:
            missing_binaries.append((entry.version, Path(entry.binary_path)))
        elif entry.file_exists:
            present_binaries.append((entry.version, Path(entry.binary_path), entry.is_default))

    if json_output:
        data = [
            {
                "active": bv.is_active,
                "version": bv.version,
                "path": str(bv.firecracker_path) if bv.firecracker_path else "",
                "missing": False,
            }
            for bv in local
        ]
        # Add missing binaries
        for version, path in missing_binaries:
            data.append(
                {
                    "active": False,
                    "version": version,
                    "path": str(path),
                    "missing": True,
                }
            )
        print(json.dumps(data, indent=2))
        return

    if local or missing_binaries or present_binaries:
        rows = [_format_bin_row(bv, is_missing=False) for bv in local]
        # Add missing binaries with X mark
        for version, path in missing_binaries:
            rows.append([get_combined_marker(False, True) + version, str(path)])
        # Add present binaries from metadata not in standard dir
        for version, path, is_default in present_binaries:
            if version not in local_versions:
                rows.append([get_combined_marker(is_default, False) + version, str(path)])
        if rows:
            print_table(columns=["Version", "Path"], rows=rows)
        else:
            print_warning("No local binaries found")
    else:
        print_warning("No local binaries found")

    if remote:
        try:
            remote_versions = list_remote_versions(limit=limit)
        except BinaryError as exc:
            print_error(str(exc))
            raise typer.Exit(code=1)

        rows = []
        for ver in remote_versions:
            cached = "✓" if ver in local_versions else " "
            rows.append([cached, ver])

        print_table(columns=["Downloaded", "Version"], rows=rows)


@bin_app.command(name="fetch")
def bin_fetch(
    version: str = typer.Argument(..., help="Version to download (e.g. 1.15.0)"),
) -> None:
    """Download a specific Firecracker version."""
    try:
        bv = fetch_binary(version)
    except BinaryError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)

    print_success(f"Downloaded v{bv.version}: {bv.firecracker_path}")
    if bv.is_active:
        print_success(f"Default binary set to v{bv.version}")


@bin_app.command(name="set-default")
def bin_set_default(
    version: str = typer.Argument(..., help="Version to set as active default"),
) -> None:
    """Set the active Firecracker binary version."""
    try:
        set_active_version(version)
    except AssetNotFoundError as exc:
        print_error(str(exc))
        raise typer.Exit(code=1)
    print_success(f"Active version set to {version}")


@bin_app.command(name="rm")
def bin_rm(
    versions: Optional[List[str]] = typer.Argument(None, help="Version(s) to remove"),
) -> None:
    """Remove one or more cached Firecracker versions."""
    effective_versions: list[str] = list(versions) if versions else []
    if not effective_versions:
        print_error("Provide at least one version to remove")
        raise typer.Exit(code=1)

    exit_code = 0
    for version in effective_versions:
        try:
            remove_version(version)
            print_success(f"Removed v{version}")
        except AssetNotFoundError as exc:
            print_error(str(exc))
            exit_code = 1

    raise typer.Exit(code=exit_code)
