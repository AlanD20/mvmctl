#!/usr/bin/env python3
"""Firecracker Manager CLI - Main entry point."""

import logging
import os

import typer
from fcm.cli import vm, image, kernel, config, asset, host, network, key, configure

app = typer.Typer(
    name="fcm",
    help="Firecracker Manager - Manage microVMs",
    rich_markup_mode="rich",
)

app.add_typer(vm.app, name="vm", help="VM lifecycle management")
app.add_typer(network.app, name="network", help="Network management")
app.add_typer(asset.app, name="asset", help="Asset management")
app.add_typer(image.app, name="image", help="Image management")
app.add_typer(kernel.app, name="kernel", help="Kernel management")
app.add_typer(config.app, name="config", help="Configuration commands")
app.add_typer(host.app, name="host", help="Host configuration")
app.add_typer(key.app, name="key", help="SSH key management")
app.add_typer(configure.app, name="configure", help="Guided setup wizard")


@app.callback()
def callback(
    verbose: bool = typer.Option(False, "--verbose", "-v", help="Enable verbose output"),
    debug: bool = typer.Option(False, "--debug", help="Enable debug mode"),
) -> None:
    """Firecracker Manager CLI."""
    # Determine log level: --debug > --verbose > FCM_LOG_LEVEL env var > WARNING
    if debug:
        level = logging.DEBUG
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get("FCM_LOG_LEVEL", "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    logging.basicConfig(
        level=level,
        format="%(levelname)s: %(name)s: %(message)s",
    )


if __name__ == "__main__":
    app()
