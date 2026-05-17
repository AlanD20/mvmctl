"""Network management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING

import typer
from rich.prompt import Prompt

from mvmctl.api import NetworkCreateInput as _NetworkCreateInput
from mvmctl.api import NetworkInput as _NetworkInput
from mvmctl.api import NetworkOperation as _NetworkOperation

if TYPE_CHECKING:
    from mvmctl.api.inputs._network_create_input import NetworkCreateInput
    from mvmctl.api.inputs._network_input import NetworkInput
    from mvmctl.api.network_operations import NetworkOperation
else:
    NetworkOperation = _NetworkOperation
    NetworkInput = _NetworkInput
    NetworkCreateInput = _NetworkCreateInput
from mvmctl.cli._completion import _complete_network_names
from mvmctl.models.result import OperationResult
from mvmctl.utils.cli import handle_errors, mvm_cli
from mvmctl.utils.network import NetworkUtils

if TYPE_CHECKING:
    from mvmctl.models import NetworkItem

network_app = typer.Typer(
    help="Network management",
    no_args_is_help=True,
    add_completion=False,
)


@network_app.callback()
def network_callback(ctx: typer.Context) -> None:
    pass


@network_app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the network command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@network_app.command(name="ls")
@handle_errors
def network_ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all networks."""
    networks: list[NetworkItem] = NetworkOperation.list_all()

    if json_output:
        data = NetworkOperation.to_json(networks)
        typer.echo(json.dumps(data, indent=2, default=str))
        return

    rows = []
    for n in networks:
        vm_count = len(n.leases) if n.leases else 0
        rows.append(
            [
                mvm_cli.format_marker(n.is_default),
                mvm_cli.format_id(n.id),
                mvm_cli.format_name(n.name, not n.is_present),
                n.subnet,
                n.bridge,
                "True" if n.nat_enabled else "False",
                str(vm_count),
                mvm_cli.format_timestamp(n.created_at),
            ]
        )
    mvm_cli.table(
        columns=[
            "",
            "ID",
            "Name",
            "Network",
            "Bridge",
            "NAT",
            "VMs",
            "Created",
        ],
        rows=rows,
    )


@network_app.command(
    name="default",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_set_default(
    ctx: typer.Context,
    name: str | None = typer.Argument(
        None,
        help="Network name to set as default",
        autocompletion=_complete_network_names,
    ),
) -> None:
    """Set a network as the default for VM creation."""
    name = mvm_cli.check_name_arg(ctx, name)
    result = NetworkOperation.set_default(NetworkInput(name=[name]))
    if result.status in ("error", "failure"):
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    mvm_cli.success(f"Default network set to: {name}")


def _resolve_user_nat_gateways() -> str:
    """Interactive prompt for NAT gateway selection — CLI-only, no business logic."""
    interfaces = NetworkUtils.get_physical_interfaces()
    if not interfaces:
        mvm_cli.error("No network interfaces found")
        raise typer.Exit(code=1)
    if len(interfaces) == 1:
        return interfaces[0]

    mvm_cli.info("Select interface(s) for NAT (internet access):")
    for i, iface in enumerate(interfaces, 1):
        mvm_cli.info(f"  [{i}] {iface}")
    selected = Prompt.ask(
        "Select interface number(s) [comma-separated]", default="1"
    )
    try:
        indices = [int(x.strip()) for x in selected.split(",") if x.strip()]
        selected_interfaces = [
            interfaces[idx - 1]
            for idx in indices
            if 1 <= idx <= len(interfaces)
        ]
    except ValueError:
        mvm_cli.error(f"Invalid interface selection: {selected}")
        raise typer.Exit(code=1)
    if not selected_interfaces:
        mvm_cli.error("No valid interface indices selected")
        raise typer.Exit(code=1)
    return ",".join(selected_interfaces)


@network_app.command(
    name="create",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_create(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
    subnet: str | None = typer.Option(
        None,
        "--subnet",
        help="IP subnet in SUBNET notation (e.g. 192.168.100.0/24)",
    ),
    ipv4_gateway: str | None = typer.Option(
        None, "--ipv4-gateway", help="Gateway IPv4 for the bridge"
    ),
    no_nat: bool = typer.Option(
        False, "--no-nat", help="Disable NAT/masquerade"
    ),
    nat_gateways: str | None = typer.Option(
        None,
        "--nat-gateways",
        help="Physical interfaces for NAT (comma-separated, auto-detected if not provided)",
    ),
    non_interactive: bool = typer.Option(
        False,
        "--non-interactive",
        help="Skip interactive prompts (auto-detect NAT interfaces)",
    ),
) -> None:
    """Create a named network."""
    name = mvm_cli.check_name_arg(ctx, name)

    if subnet is None:
        mvm_cli.error("Missing required option '--subnet'")
        raise typer.Exit(code=1)

    if nat_gateways is None and not no_nat and not non_interactive:
        nat_gateways = _resolve_user_nat_gateways()

    nat_gateways_list = (
        [g.strip() for g in nat_gateways.split(",") if g.strip()]
        if nat_gateways
        else []
    )

    create_input = NetworkCreateInput(
        name=name,
        subnet=subnet,
        ipv4_gateway=ipv4_gateway,
        nat_enabled=not no_nat,
        nat_gateways=nat_gateways_list,
    )
    result = NetworkOperation.create(create_input)
    if isinstance(result, OperationResult):
        if result.status in ("error", "failure"):
            mvm_cli.error(result.message)
            raise typer.Exit(code=1)
        if result.status == "skipped":
            mvm_cli.info(result.message)
            raise typer.Exit(code=0)
        config = result.item
        if config is None:
            mvm_cli.error("Network created but no item returned")
            raise typer.Exit(code=1)
    else:
        # NeedsInteraction — not expected for network creation
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)

    mvm_cli.success(f"Created: {config.name}")
    mvm_cli.info(f"  SUBNET:    {config.subnet}")
    mvm_cli.info(f"  IPv4 Gateway: {config.ipv4_gateway}")
    mvm_cli.info(f"  Bridge:  {config.bridge}")
    mvm_cli.info(f"  NAT:     {'True' if config.nat_enabled else 'False'}")
    if config.nat_gateways:
        mvm_cli.info(f"  NAT gateways: {', '.join(config.nat_gateways_list)}")


@network_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_rm(
    names: list[str] = typer.Argument(
        None,
        help="Network names to remove",
        autocompletion=_complete_network_names,
    ),
    force: bool = typer.Option(
        False, "--force", "-f", help="Remove even if referenced by VMs"
    ),
) -> None:
    """Remove one or more networks by name."""
    effective_names = list(names) if names else []
    if not effective_names:
        mvm_cli.error("Provide at least one network name")
        raise typer.Exit(code=1)

    result = NetworkOperation.remove(
        NetworkInput(name=effective_names), force=force
    )
    if result.status in ("error", "failure"):
        mvm_cli.error(result.message)
        raise typer.Exit(code=1)
    for name in effective_names:
        mvm_cli.success(f"Removed: {name}")


@network_app.command(
    name="inspect",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_inspect(
    ctx: typer.Context,
    name: str | None = typer.Argument(
        None,
        help="Network name",
        autocompletion=_complete_network_names,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
    tree: bool = typer.Option(False, "--tree", help="Output in tree format"),
) -> None:
    """Show detailed information about a network."""
    name = mvm_cli.check_name_arg(ctx, name)
    info = NetworkOperation.inspect(NetworkInput(name=[name]))

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    net_name = info.get("network", {}).get("name", name)
    mvm_cli.print_dict_tree(info, title=f"Network: {net_name}")


@network_app.command(
    name="sync",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_sync(
    ctx: typer.Context,
    identifier: str | None = typer.Argument(
        None,
        help="Network name or ID (omit for all)",
        autocompletion=_complete_network_names,
    ),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Sync iptables rules between database and host."""
    network_id: str | None = None
    if identifier is not None:
        network = NetworkOperation.get(NetworkInput(name=[identifier]))
        network_id = network.id

    sync_result = NetworkOperation.sync(network_id)
    if sync_result.status in ("error", "failure"):
        mvm_cli.error(sync_result.message)
        raise typer.Exit(code=1)
    results = sync_result.item
    if results is None:
        mvm_cli.error("Sync returned no results")
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(results, indent=2))
        return

    # Build a name map for all networks to avoid N+1 lookups
    all_networks = NetworkOperation.list_all()
    name_map = {n.id: n.name for n in all_networks}

    rows = []
    for nid, counts in results.items():
        short_id = mvm_cli.format_id(nid)
        name = name_map.get(nid, nid[:8])
        rows.append(
            [
                short_id,
                name,
                str(counts["verified"]),
                str(counts["added"]),
                str(counts["orphaned"]),
            ]
        )

    mvm_cli.table(
        columns=["ID", "Name", "Verified", "Added", "Orphaned"],
        rows=rows,
    )
