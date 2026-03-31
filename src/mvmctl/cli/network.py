"""Network management commands."""

import json

import typer
from rich.prompt import Prompt

from mvmctl.api.network import (
    create_network,
    get_iptables_rules_for_bridge,
    get_network_leases,
    inspect_network,
    list_network_interfaces,
    list_networks,
    remove_network,
    set_default_network,
)
from mvmctl.cli._helpers import check_name_arg, is_bridge_alive
from mvmctl.exceptions import NetworkError
from mvmctl.utils.console import (
    format_timestamp,
    print_error,
    print_info,
    print_inspect_header,
    print_key_value,
    print_section_header,
    print_success,
    print_table,
)
from mvmctl.utils.time import human_readable_time
from mvmctl.utils.validation import validate_entity_name

app = typer.Typer(
    help="Network management",
    no_args_is_help=True,
    rich_markup_mode=None,
    add_completion=False,
)


@app.command(name="help", hidden=True)
def help_cmd(ctx: typer.Context) -> None:
    """Show help for the network command group."""
    typer.echo(ctx.parent.get_help() if ctx.parent else "")
    raise typer.Exit()


@app.command(name="ls")
def ls(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """List all networks."""
    networks = list_networks()

    if json_output:
        data = [
            {
                "name": n.name,
                "cidr": n.cidr,
                "gateway": n.gateway,
                "bridge": n.bridge,
                "nat_enabled": n.nat_enabled,
                "created_at": n.created_at,
                "is_default": n.is_default,
                "vm_count": len(get_network_leases(n.name)),
            }
            for n in networks
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not networks:
        print_info("No networks found. Create one with: mvm network create <name>")

    rows = []
    for n in networks:
        is_bridge_missing = not is_bridge_alive(n.bridge)
        # Treat network named "default" as default regardless of is_default flag
        is_default = n.is_default or n.name == "default"
        # Prioritize default marker (*) over missing marker (X) for consistent UX
        if is_default:
            name_col = "* " + n.name
        else:
            name_col = ("X " if is_bridge_missing else "  ") + n.name
        rows.append(
            [
                name_col,
                n.cidr,
                n.gateway,
                n.bridge,
                "yes" if n.nat_enabled else "no",
                str(len(get_network_leases(n.name))),
                human_readable_time(n.created_at) if n.created_at else "-",
            ]
        )
    print_table(
        title="Networks",
        columns=["Name", "Network", "Gateway", "Bridge", "NAT", "VMs", "Created"],
        rows=rows,
    )


@app.command(
    name="set-default", context_settings={"allow_extra_args": True, "ignore_unknown_options": True}
)
def set_default(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name to set as default"),
) -> None:
    """Set a network as the default for VM creation."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "network")
    try:
        set_default_network(name)
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)
    print_success(f"Default network set to '{name}'")


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def create(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
    cidr: str | None = typer.Option(
        None, "--cidr", help="IP subnet in CIDR notation (e.g. 192.168.100.0/24)"
    ),
    gateway: str | None = typer.Option(None, "--gateway", help="Gateway IP for the bridge"),
    no_nat: bool = typer.Option(False, "--no-nat", help="Disable NAT/masquerade"),
    internet_iface: str | None = typer.Option(
        None,
        "--internet-iface",
        help="Physical interface for NAT (auto-detected if not provided)",
    ),
) -> None:
    """Create a named network."""
    name = check_name_arg(ctx, name)
    if cidr is None:
        print_error("Missing required option '--cidr'")
        raise typer.Exit(code=1)
    validate_entity_name(name, "network")

    # Auto-detect internet interface if not provided
    if internet_iface is None:
        interfaces = list_network_interfaces()
        if len(interfaces) == 0:
            print_error("No network interfaces found")
            raise typer.Exit(code=1)
        elif len(interfaces) == 1:
            internet_iface = interfaces[0]
        else:
            # Prompt user to select interface
            print_info("Select interface for NAT (internet access):")
            for i, iface in enumerate(interfaces, 1):
                print_info(f"  [{i}] {iface}")
            selected = Prompt.ask(
                "Select interface number",
                choices=[str(i) for i in range(1, len(interfaces) + 1)],
                default="1",
            )
            internet_iface = interfaces[int(selected) - 1]

    try:
        config = create_network(
            name=name,
            cidr=cidr,
            gateway=gateway,
            nat=not no_nat,
            internet_iface=internet_iface,
        )
    except NetworkError as e:
        error_msg = str(e)
        print_error(error_msg)
        print_info("")
        if "already exists" in error_msg.lower():
            print_info("To view existing networks:")
            print_info("  mvm network ls")
            print_info("")
            print_info("To remove the existing network:")
            print_info(f"  mvm network rm {name}")
        elif "overlaps" in error_msg.lower():
            print_info("Choose a different CIDR that doesn't conflict with existing networks.")
            print_info("Common private ranges:")
            print_info("  10.0.0.0/8     (very large)")
            print_info("  172.16.0.0/12  (large)")
            print_info("  192.168.0.0/16 (medium)")
            print_info("  192.168.100.0/24 (small, good for testing)")
        elif "bridge" in error_msg.lower() and "conflicts" in error_msg.lower():
            print_info("Try using a different network name.")
        elif "privilege" in error_msg.lower() or "permission" in error_msg.lower():
            print_info("Run with sudo or configure persistent access:")
            print_info("  sudo mvm host init")
            print_info("  (then log out and back in)")
        raise typer.Exit(code=1)

    print_success(f"Network '{config.name}' created")
    print_info(f"  CIDR:    {config.cidr}")
    print_info(f"  Gateway: {config.gateway}")
    print_info(f"  Bridge:  {config.bridge}")
    print_info(f"  NAT:     {'enabled' if config.nat_enabled else 'disabled'}")
    print_info(f"  Internet interface: {internet_iface}")


@app.command(
    name="remove",
    hidden=True,
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def remove(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
) -> None:
    """Remove a named network."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "network")

    try:
        remove_network(name)
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Network '{name}' removed")


@app.command(
    name="rm",
    context_settings={"allow_extra_args": True, "ignore_unknown_options": True},
)
def rm(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
) -> None:
    """Alias for remove."""
    remove(ctx=ctx, name=name)


@app.command(context_settings={"allow_extra_args": True, "ignore_unknown_options": True})
def inspect(
    ctx: typer.Context,
    name: str | None = typer.Argument(None, help="Network name"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a network."""
    name = check_name_arg(ctx, name)
    validate_entity_name(name, "network")
    try:
        info = inspect_network(name)
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    status = "active" if info.get("bridge_exists") else "inactive"
    print_inspect_header(f"Network: {info['name']}", status)

    print_section_header("BASIC INFO")
    print_key_value("Name", info["name"])
    print_key_value("CIDR", info.get("cidr", info.get("subnet", "-")))
    print_key_value("Gateway", info["gateway"])
    print_key_value("Bridge", info["bridge"])
    print_key_value("NAT", "enabled" if info["nat_enabled"] else "disabled")
    print_key_value("Created", format_timestamp(info.get("created_at")))

    print_section_header("RESOURCES")
    raw_vms = info.get("vms")
    vms = [v for v in (raw_vms if isinstance(raw_vms, list) else []) if isinstance(v, dict)]
    print_key_value("Bridge Active", "yes" if info.get("bridge_exists") else "no")
    print_key_value("Leases", f"{len(vms)} assigned")

    print_section_header("INTERFACES")
    print_key_value("Bridge", info["bridge"])

    # Show NAT config if enabled
    if info.get("nat_enabled"):
        print_section_header("NAT CONFIG")
        # Get iptables rules to find interface
        bridge = str(info["bridge"])
        rules = get_iptables_rules_for_bridge(bridge)
        iface = "-"
        for rule in rules:
            if "-o" in rule:
                parts = rule.split()
                for i, part in enumerate(parts):
                    if part == "-o" and i + 1 < len(parts):
                        iface = parts[i + 1]
                        break
            if iface != "-":
                break
        print_key_value("Interface", iface)
        print_key_value("MASQUERADE", "enabled" if rules else "disabled")

    # Show VMs if any
    if vms:
        print_section_header("VMS")
        for vm in vms:
            print_key_value(vm["vm_name"], vm["ip"], indent=2, key_width=20)
