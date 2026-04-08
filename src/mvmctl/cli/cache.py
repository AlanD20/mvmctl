"""Cache management commands."""

from typing import Optional

import typer

from mvmctl.api import cache as cache_api
from mvmctl.utils.console import print_error, print_info, print_success, print_warning

cache_app = typer.Typer(
    help="Cache management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@cache_app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the cache command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@cache_app.command(name="init")
def cache_init() -> None:
    """Initialize all cache resources."""
    try:
        result = cache_api.init_all()
        print_success("Cache initialized successfully")
        for resource, path in result.items():
            if path:
                print_info(f"  {resource}: {path}")
    except Exception as e:
        print_error(f"Failed to initialize cache: {e}")
        raise typer.Exit(code=1)


@cache_app.command(name="prune")
def cache_prune(
    resource: Optional[str] = typer.Argument(
        None, help="Resource to prune: vm, network, image, kernel, all"
    ),
    include_stopped: bool = typer.Option(
        False, "--include-stopped", help="Include stopped VMs in pruning (default: only ERROR VMs)"
    ),
    include_running: bool = typer.Option(
        False, "--include-running", help="Include running VMs in pruning (USE WITH CAUTION)"
    ),
    all_resources: bool = typer.Option(
        False,
        "--all",
        "-a",
        help="With a resource: remove all items of that type (bypass protections). Without a resource: prune all types.",
    ),
    dry_run: bool = typer.Option(
        False, "--dry-run", help="Show what would be removed without actually removing"
    ),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation prompts"),
) -> None:
    """Prune cache resources.

    Examples:
        mvm cache prune vm --all                 # Remove ALL VMs (all states)
        mvm cache prune vm --include-stopped     # Prune stopped VMs
        mvm cache prune image --all              # Remove ALL images (including default/referenced)
        mvm cache prune network                  # Prune unused networks
        mvm cache prune all --force              # Prune all resource types
    """
    if resource == "vm":
        try:
            all_vms = all_resources
            removed = cache_api.prune_vms(
                all_vms or include_stopped,
                all_vms or include_running,
                dry_run,
            )
            if removed:
                print_success(f"Pruned {len(removed)} VM(s): {', '.join(removed)}")
            else:
                print_info("No VMs to prune")
        except Exception as e:
            print_error(f"Failed to prune VMs: {e}")
            raise typer.Exit(code=1)

    elif resource == "network":
        try:
            removed = cache_api.prune_networks(dry_run, all_resources)
            if removed:
                print_success(f"Pruned {len(removed)} network(s): {', '.join(removed)}")
            else:
                print_info("No networks to prune")
        except Exception as e:
            print_error(f"Failed to prune networks: {e}")
            raise typer.Exit(code=1)

    elif resource == "image":
        try:
            removed = cache_api.prune_images(dry_run, all_resources)
            if removed:
                print_success(f"Pruned {len(removed)} image(s): {', '.join(removed)}")
            else:
                print_info("No images to prune")
        except Exception as e:
            print_error(f"Failed to prune images: {e}")
            raise typer.Exit(code=1)

    elif resource == "kernel":
        try:
            removed = cache_api.prune_kernels(dry_run, all_resources)
            if removed:
                print_success(f"Pruned {len(removed)} kernel(s): {', '.join(removed)}")
            else:
                print_info("No kernels to prune")
        except Exception as e:
            print_error(f"Failed to prune kernels: {e}")
            raise typer.Exit(code=1)

    elif all_resources or resource == "all":
        if not force:
            print_warning("This will remove ALL unused cache resources:")
            print_warning("  - VMs (based on --include-stopped/--include-running flags)")
            print_warning("  - Networks not referenced by any VM")
            print_warning("  - Images not referenced by any VM")
            print_warning("  - Kernels not referenced by any VM")
            if not typer.confirm("Continue?"):
                print_info("Aborted")
                raise typer.Exit()

        try:
            result = cache_api.prune_all(include_stopped, include_running, dry_run)

            vms_result: list[str] | bool = result.get("vms", [])
            if isinstance(vms_result, list) and vms_result:
                print_success(f"Pruned {len(vms_result)} VM(s)")
            networks_result: list[str] | bool = result.get("networks", [])
            if isinstance(networks_result, list) and networks_result:
                print_success(f"Pruned {len(networks_result)} network(s)")
            images_result: list[str] | bool = result.get("images", [])
            if isinstance(images_result, list) and images_result:
                print_success(f"Pruned {len(images_result)} image(s)")
            kernels_result: list[str] | bool = result.get("kernels", [])
            if isinstance(kernels_result, list) and kernels_result:
                print_success(f"Pruned {len(kernels_result)} kernel(s)")

        except Exception as e:
            print_error(f"Failed to prune cache: {e}")
            raise typer.Exit(code=1)

    else:
        if resource is None:
            print_error("No resource specified")
        else:
            print_error(f"Unknown resource: {resource}")
        print_info("Valid resources: vm, network, image, kernel, all")
        raise typer.Exit(code=1)
