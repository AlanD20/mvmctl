"""Config management commands."""

from __future__ import annotations

from typing import TYPE_CHECKING

import typer

from mvmctl.api import ConfigOperation as _ConfigOperation

if TYPE_CHECKING:
    from mvmctl.api.config_operations import ConfigOperation
else:
    ConfigOperation = _ConfigOperation
from mvmctl.cli._completion import (
    _complete_config_categories,
    _complete_config_keys,
)
from mvmctl.utils.cli import handle_errors, mvm_cli

config_app = typer.Typer(
    help="Configuration management",
    no_args_is_help=True,
    add_completion=False,
)


@config_app.command(name="get")
@handle_errors
def config_get(
    category: str = typer.Argument(
        ...,
        help="Setting category (e.g. defaults.vm)",
        autocompletion=_complete_config_categories,
    ),
    key: str | None = typer.Argument(
        None,
        help="Setting key (e.g. vcpu_count)",
        autocompletion=_complete_config_keys,
    ),
) -> None:
    """Get a config value."""
    value = ConfigOperation.get(category, key)
    if isinstance(value, dict):
        for k, info in value.items():
            override = info.get("override")
            default = info.get("default")
            typ = info.get("type")
            if override is not None:
                mvm_cli.info(
                    f"{k} = {override} (override: {override}, type: {typ})"
                )
            else:
                mvm_cli.info(
                    f"{k} = {default} (default: {default}, type: {typ})"
                )
    elif value is not None:
        mvm_cli.info(f"{category}.{key} = {value}")
    else:
        mvm_cli.info(f"{category}.{key} = (default)")


@config_app.command(name="set")
@handle_errors
def config_set(
    category: str = typer.Argument(
        ...,
        help="Setting category (e.g. defaults.vm)",
        autocompletion=_complete_config_categories,
    ),
    key: str = typer.Argument(
        ...,
        help="Setting key (e.g. vcpu_count)",
        autocompletion=_complete_config_keys,
    ),
    value: str = typer.Argument(..., help="New value"),
) -> None:
    """Set a config value."""
    result = ConfigOperation.set(category, key, value)
    if result.is_error:
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(result.message)


@config_app.command(name="reset")
@handle_errors
def config_reset(
    category: str | None = typer.Argument(
        None, help="Setting category (e.g. defaults.vm)"
    ),
    key: str | None = typer.Argument(
        None, help="Setting key (e.g. vcpu_count)"
    ),
    all_overrides: bool = typer.Option(
        False, "--all", "-a", help="Reset all overrides globally"
    ),
) -> None:
    """Reset a config value to its default."""
    if all_overrides:
        result = ConfigOperation.reset(all_overrides=True)
        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        mvm_cli.success(f"Reset: {result.item} override(s) globally")
    elif category is not None and key is not None:
        result = ConfigOperation.reset(category, key)
        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        if result.item and result.item > 0:
            mvm_cli.success(f"Reset: {category}.{key}")
        else:
            mvm_cli.info(f"{category}.{key} was already at default")
    elif category is not None:
        result = ConfigOperation.reset(category, key=None)
        if result.is_error:
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        mvm_cli.success(f"Reset: {result.item} override(s) in {category}")
    else:
        mvm_cli.info("Provide a category, category and key, or use --all")


@config_app.command(name="list")
@handle_errors
def config_list() -> None:
    """List all overridable settings and their current values."""
    settings = ConfigOperation.list_all()
    for category, keys in settings.items():
        mvm_cli.info(f"\n[{category}]")
        for key, info in keys.items():
            override = info["override"]
            if override is not None:
                mvm_cli.info(f"  {key} = {override} (type: {info['type']})")
            else:
                mvm_cli.info(f"  {key} = (default, type: {info['type']})")
