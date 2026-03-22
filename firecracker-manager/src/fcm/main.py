#!/usr/bin/env python3
"""Firecracker Manager CLI - Main entry point."""

import typer
from fcm.cli import vm, image, kernel, config

app = typer.Typer(
    name="fcm",
    help="Firecracker Manager - Manage microVMs",
    rich_markup_mode="rich",
)

app.add_typer(vm.app, name="vm", help="VM lifecycle management")
app.add_typer(image.app, name="image", help="Image management")
app.add_typer(kernel.app, name="kernel", help="Kernel management")
app.add_typer(config.app, name="config", help="Configuration commands")


@app.callback()
def callback(
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Enable verbose output"),
    debug: bool = typer.Option(False, "--debug", help="Enable debug mode"),
) -> None:
    """Firecracker Manager CLI."""
    pass


if __name__ == "__main__":
    app()
