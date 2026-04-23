"""Network management commands."""

from __future__ import annotations

import json
from typing import TYPE_CHECKING

import typer
from rich.prompt import Prompt

from mvmctl.api.inputs._network_create_input import NetworkCreateInput
from mvmctl.api.inputs._network_input import NetworkInput
from mvmctl.api.network_operations import NetworkOperation
from mvmctl.utils.cli import CliUtils, handle_errors
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.console import (
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.network import NetworkUtils

if TYPE_CHECKING:
    from mvmctl.models.network import NetworkItem, NetworkLeaseItem

network_app = typer.Typer(
    help="Network management",
    no_args_is_help=True,
    rich_markup_mode=None,
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
        data = [
            {
                "name": n.name,
                "subnet": n.subnet,
                "ipv4_gateway": n.ipv4_gateway,
                "bridge": n.bridge,
                "nat_enabled": n.nat_enabled,
                "created_at": n.created_at,
                "is_default": n.is_default,
                "vm_count": len(n.leases) if n.leases else 0,
            }
            for n in networks
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    rows = []
    for n in networks:
        is_default = n.is_default
        name_col = CommonUtils._get_combined_marker(
            is_default, not n.is_present
        ) + n.name
        vm_count = len(n.leases) if n.leases else 0
        rows.append(
            [
                name_col,
                n.subnet,
                n.ipv4_gateway,
                n.bridge,
                "yes" if n.nat_enabled else "no",
                str(vm_count),
                CommonUtils.human_readable_datetime(n.created_at),
            ]
        )
    print_table(
        columns=[
            "Name",
            "Network",
            "IPv4 Gateway",
            "Bridge",
            "NAT",
            "VMs",
            "Created",
        ],
        rows=rows,
    )


@network_app.command(
    name="set-default",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_set_default(
    ctx: typer.Context,
    name: str | None = typer.Argument(
        None, help="Network name to set as default"
    ),
) -> None:
    """Set a network as the default for VM creation."""
    name = CliUtils.check_name_arg(ctx, name)
    NetworkOperation.set_default(NetworkInput(name=[name]))
    print_success(f"Default network set to '{name}'")


def _resolve_user_nat_gateways() -> str:
    interfaces = NetworkUtils.get_physical_interfaces()
    if not interfaces:
        print_error("No network interfaces found")
        raise typer.Exit(code=1)
    if len(interfaces) == 1:
        return interfaces[0]
    print_info("Select interface(s) for NAT (internet access):")
    for i, iface in enumerate(interfaces, 1):
        print_info(f"  [{i}] {iface}")
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
        print_error(f"Invalid interface selection: {selected}")
        raise typer.Exit(code=1)
    if not selected_interfaces:
        print_error("No valid interface indices selected")
        raise typer.Exit(code=1)
    return ",".join(selected_interfaces)


def _print_create_error(error_msg: str, name: str) -> None:
    print_error(error_msg)
    print_info("")
    lowered = error_msg.lower()
    if "already exists" in lowered:
        print_info("To view existing networks:")
        print_info("  mvm network ls")
        print_info("")
        print_info("To remove the existing network:")
        print_info(f"  mvm network rm {name}")
    elif "overlaps" in lowered:
        print_info(
            "Choose a different SUBNET that doesn't conflict with existing networks."
        )
        print_info("Common private ranges:")
        print_info("  10.0.0.0/8     (very large)")
        print_info("  172.16.0.0/12  (large)")
        print_info("  192.168.0.0/16 (medium)")
        print_info("  192.168.100.0/24 (small, good for testing)")
    elif "bridge" in lowered and "conflicts" in lowered:
        print_info("Try using a different network name.")
    elif "privilege" in lowered or "permission" in lowered:
        print_info("Run with sudo or configure persistent access:")
        print_info("  sudo mvm host init")
        print_info("  (then log out and back in)")


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
) -> None:
    """Create a named network."""
    name = CliUtils.check_name_arg(ctx, name)

    if subnet is None:
        print_error("Missing required option '--subnet'")
        raise typer.Exit(code=1)

    if nat_gateways is None and not no_nat:
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
    config = result.result

    print_success(f"Network '{config.name}' created")
    print_info(f"  SUBNET:    {config.subnet}")
    print_info(f"  IPv4 Gateway: {config.ipv4_gateway}")
    print_info(f"  Bridge:  {config.bridge}")
    print_info(f"  NAT:     {'enabled' if config.nat_enabled else 'disabled'}")
    if config.nat_gateways:
        print_info(f"  NAT gateways: {', '.join(config.nat_gateways_list)}")


@network_app.command(
    name="remove",
    hidden=True,
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_remove(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
) -> None:
    """Remove a named network."""
    name = CliUtils.check_name_arg(ctx, name)
    NetworkOperation.remove(NetworkInput(name=[name]))
    print_success(f"Network '{name}' removed")


@network_app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_rm(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
) -> None:
    """Alias for remove."""
    network_remove(ctx=ctx, name=name)


@network_app.command(
    name="inspect",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
@handle_errors
def network_inspect(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a network."""
    name = CliUtils.check_name_arg(ctx, name)
    info = NetworkOperation.inspect(
        NetworkInput(name=[name]), is_json=json_output
    )

    if isinstance(info, dict):
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    status = "active" if info.bridge_active else "inactive"
    print_inspect_header(f"Network: {info.name}", status)

    print_section_header("BASIC INFO")
    print_key_value("Name", info.name)
    print_key_value("Subnet", info.subnet or "-")
    print_key_value("IPv4 Gateway", info.ipv4_gateway or "-")
    print_key_value("Bridge", info.bridge)
    print_key_value("NAT", "enabled" if info.nat_enabled else "disabled")
    print_key_value(
        "Created", CommonUtils.human_readable_datetime(info.created_at)
    )

    print_section_header("RESOURCES")
    leases: list[NetworkLeaseItem] = info.leases or []
    print_key_value("Bridge Active", "yes" if info.bridge_active else "no")
    print_key_value("Leases", f"{len(leases)} assigned")

    # Show NAT config if enabled
    if info.nat_enabled:
        nat_gateways: list[str] = info.nat_gateways_list or []
        print_section_header("NAT CONFIG")
        print_key_value(
            "NAT Gateways", ", ".join(nat_gateways) if nat_gateways else "-"
        )

    # Show VMs if any
    if leases:
        print_section_header("VMS")
        for lease in leases:
            if lease.vm_id:
                label = f"{lease.vm_id}"
                print_key_value(
                    label, lease.ipv4 or "-", indent=2, key_width=28
                )
