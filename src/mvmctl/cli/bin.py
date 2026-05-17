"""Binary management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, cast

import typer
from rich.console import Console

from mvmctl.api import BinaryInput as _BinaryInput
from mvmctl.api import BinaryOperation as _BinaryOperation
from mvmctl.api import BinaryPullInput as _BinaryPullInput
from mvmctl.core._shared import VersionError, VersionResolver
from mvmctl.models import BinaryItem

if TYPE_CHECKING:
    from mvmctl.api.binary_operations import BinaryOperation
    from mvmctl.api.inputs._binary_input import BinaryInput
    from mvmctl.api.inputs._binary_pull_input import BinaryPullInput
else:
    BinaryOperation = _BinaryOperation
    BinaryPullInput = _BinaryPullInput
    BinaryInput = _BinaryInput
from mvmctl.cli._completion import _complete_binary_versions
from mvmctl.models.result import OperationResult
from mvmctl.utils.cli import handle_errors, mvm_cli

bin_app = typer.Typer(
    help="Binary management",
    no_args_is_help=True,
    add_completion=False,
)


@bin_app.callback()
def bin_callback(ctx: typer.Context) -> None:
    pass


@bin_app.command(name="ls")
@handle_errors
def bin_ls(
    remote: bool = typer.Option(
        False, "--remote", "-r", help="Also show remote versions"
    ),
    limit: int = typer.Option(
        None, "--limit", help="Max remote versions to show"
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List local (and optionally remote) Firecracker versions."""
    local = cast(list[BinaryItem], BinaryOperation.list_all())
    local_versions = {b.version for b in local if b.name == "firecracker"}

    if json_output:
        data = [
            {
                "id": b.id,
                "name": b.name,
                "version": b.version,
                "full_version": b.full_version,
                "ci_version": b.ci_version,
                "path": b.path,
                "is_default": b.is_default,
                "is_present": b.is_present,
                "created_at": b.created_at,
                "updated_at": b.updated_at,
            }
            for b in local
        ]
        print(json.dumps(data, indent=2))
        raise typer.Exit(code=0)

    rows: list[list[str]] = []

    if remote:
        with Console().status("Fetching remote versions"):
            remote_versions = cast(
                list[str],
                BinaryOperation.list_all(remote=True, limit=limit),
            )

        for ver in remote_versions:
            cached = "✓" if ver in local_versions else " "
            rows.append([cached, ver])

        mvm_cli.table(columns=["Downloaded", "Version"], rows=rows)
        raise typer.Exit(code=0)

    for b in local:
        short_id = mvm_cli.format_id(b.id)
        rows.append(
            [
                mvm_cli.format_marker(b.is_default),
                short_id,
                b.name,
                b.version,
            ]
        )

    mvm_cli.table(
        columns=["", "ID", "Name", "Version"],
        rows=rows,
    )
    raise typer.Exit(code=0)


@bin_app.command(name="pull")
@handle_errors
def bin_pull(
    name: str = typer.Argument(
        ...,
        help="Binary name (only 'firecracker' is supported)",
        autocompletion=_complete_binary_versions,
    ),
    version: str | None = typer.Option(
        None,
        "--version",
        help="Version to download (e.g. 1.15.0, latest)",
    ),
    git_ref: str | None = typer.Option(
        None,
        "--git-ref",
        help="Git ref (branch/tag/commit) to build from source. "
        "Mutually exclusive with --version.",
    ),
    set_default: bool = typer.Option(
        False,
        "--default",
        "-d",
        help="Set as default after download",
    ),
    force: bool = typer.Option(
        False,
        "--force",
        "-f",
        help="Re-download even if version already exists",
    ),
) -> None:
    """Download a Firecracker version or build from source."""
    from mvmctl.exceptions import BinaryNotFoundError

    # Only firecracker is supported for download/build
    if name.lower() != "firecracker":
        mvm_cli.error(
            f"Unsupported binary: '{name}'. "
            "Only 'firecracker' is supported for download or build."
        )
        raise typer.Exit(code=1)

    # --git-ref and --version are mutually exclusive
    if git_ref and version:
        mvm_cli.error(
            "--git-ref and --version are mutually exclusive. "
            "Use --git-ref to build from source, or --version to download a release."
        )
        raise typer.Exit(code=1)

    # ---- Git build path ----
    if git_ref:
        mvm_cli.info(
            f"Building Firecracker from ref '{git_ref}' via Docker-based devtool..."
        )
        mvm_cli.info("  Phase 1: Cloning/updating Firecracker source (git)")
        mvm_cli.info("  Phase 2: Building release binary (5-15 min via Docker)")
        mvm_cli.info("  The build output will appear below once it starts:\n")

        inputs = BinaryPullInput(
            version="",
            name=name,
            git_ref=git_ref,
            set_default=set_default,
            download_override=False,
        )
        result: OperationResult[list[BinaryItem]] = BinaryOperation.pull(inputs)  # type: ignore[assignment]

        mvm_cli.info("")  # spacing after build output

        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)

        binaries = result.item or []
        for binary in binaries:
            short_id = mvm_cli.format_id(binary.id)
            mvm_cli.success(
                f"Built: {binary.name} {binary.version}: {binary.resolved_path}"
            )
            mvm_cli.info(f"  ID: {short_id}")

        if set_default:
            mvm_cli.success(
                f"Default binary set to: {binaries[0].version}"
                if binaries
                else ""
            )

        raise typer.Exit(code=0)

    # ---- Release download path ----
    # Resolve version from remote
    remote_versions = cast(
        list[str],
        BinaryOperation.list_all(remote=True, limit=20),
    )
    if not remote_versions:
        mvm_cli.error("No remote Firecracker versions found")
        raise typer.Exit(code=1)

    if version is not None:
        spec = VersionResolver.parse_spec(version)
        try:
            resolved_version = VersionResolver.resolve(remote_versions, spec)
        except VersionError:
            mvm_cli.error(
                f"Version '{version}' not found in remote versions. "
                f"Available: {', '.join(remote_versions[:10])}"
            )
            raise typer.Exit(code=1)
    else:
        resolved_version = remote_versions[0]

    normalized = resolved_version.removeprefix("v")

    # Check if version already exists (may not exist yet — that's OK)
    try:
        already_exists = BinaryOperation.get(
            BinaryInput(
                identifiers=["firecracker", "jailer"], version=normalized
            )
        )
    except BinaryNotFoundError:
        already_exists = []

    download_override = force

    if already_exists and not force:
        mvm_cli.warning(f"Binary v{normalized} already exists.")
        if not typer.confirm("Re-download?", default=False):
            mvm_cli.info("Aborted")
            raise typer.Exit(code=0)
        download_override = True

    inputs = BinaryPullInput(
        version=normalized,
        name=name,
        set_default=set_default,
        download_override=download_override,
    )
    result = BinaryOperation.pull(inputs)  # type: ignore[assignment]

    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)

    if result.status == "skipped":
        mvm_cli.info(result.message)
        binaries = result.item or []
        for binary in binaries:
            short_id = mvm_cli.format_id(binary.id)
            mvm_cli.info(f"  {binary.name} v{binary.version}: {short_id}")
        raise typer.Exit(code=0)

    binaries = result.item or []
    for binary in binaries:
        short_id = mvm_cli.format_id(binary.id)
        mvm_cli.success(
            f"Downloaded: {binary.name} v{binary.version}: {binary.resolved_path}"
        )
        mvm_cli.info(f"  ID: {short_id}")

    if set_default:
        mvm_cli.success(f"Default binary set to: v{normalized}")

    raise typer.Exit(code=0)


@bin_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def bin_rm(
    identifiers: list[str] | None = typer.Argument(
        None,
        help="Binary ID(s) to remove (6-char prefix accepted)",
        autocompletion=_complete_binary_versions,
    ),
    version: str = typer.Option(
        None,
        "--version",
        help="Remove both firecracker and jailer for this version",
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
    ),
) -> None:
    """Remove one or more binaries. Use --version to remove by version pair."""
    if version is not None:
        result = BinaryOperation.remove_by_version(version, force=force)
        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        mvm_cli.success(f"Removed: v{version}")
        raise typer.Exit(code=0)

    effective_ids: list[str] = list(identifiers) if identifiers else []
    if not effective_ids:
        mvm_cli.error(
            "Provide at least one binary ID to remove or use --version"
        )
        raise typer.Exit(code=1)

    inputs = BinaryInput(identifiers=effective_ids)
    batch_result = BinaryOperation.remove(inputs, force=force)

    for item_result in batch_result.items:
        if item_result.is_ok:
            mvm_cli.success(item_result.message or "Removed")
        else:
            mvm_cli.error(item_result.message or "Remove failed")

    if batch_result.has_any_error:
        raise typer.Exit(code=1)


@bin_app.command(name="default")
@handle_errors
def bin_default(
    identifier: str = typer.Argument(
        ...,
        help="Binary ID to set as default (6-char prefix accepted)",
        autocompletion=_complete_binary_versions,
    ),
) -> None:
    """Set a binary as the active default."""
    inputs = BinaryInput(identifiers=[identifier])
    result = BinaryOperation.set_default(inputs)

    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)

    mvm_cli.success(result.message or f"Default binary set to: {identifier}")
    raise typer.Exit(code=0)


__all__ = ["bin_app"]
