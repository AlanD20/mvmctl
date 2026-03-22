"""Network management commands."""

import json
from typing import cast

import typer
from rich.console import Console
from rich.table import Table

from fcm.core.network import get_iptables_rules_for_bridge
from fcm.core.network_manager import (
    DEFAULT_NETWORK_NAME,
    create_network,
    get_network_leases,
    inspect_network,
    list_networks,
    remove_network,
)
from fcm.exceptions import NetworkError
from fcm.utils.console import print_error, print_info, print_success

app = typer.Typer(help="Network management", no_args_is_help=True)
console = Console()


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
                "vm_count": len(get_network_leases(n.name)),
            }
            for n in networks
        ]
        typer.echo(json.dumps(data, indent=2))
        return

    if not networks:
        print_info("No networks found. Create one with: fcm network create <name>")
        return

    table = Table(title="Networks")
    table.add_column("Name", style="cyan", no_wrap=True)
    table.add_column("CIDR", style="green")
    table.add_column("Gateway")
    table.add_column("Bridge")
    table.add_column("NAT")
    table.add_column("VM Count")
    table.add_column("Created")

    for n in networks:
        nat_str = "[green]yes[/green]" if n.nat_enabled else "[dim]no[/dim]"
        created = n.created_at[:19] if n.created_at else "-"
        vm_count = str(len(get_network_leases(n.name)))
        name_str = f"{n.name} (default)" if n.name == DEFAULT_NETWORK_NAME else n.name
        table.add_row(name_str, n.cidr, n.gateway, n.bridge, nat_str, vm_count, created)

    console.print(table)


@app.command(name="list", hidden=True)
def list_cmd(
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Alias for ls."""
    ls(json_output=json_output)


@app.command()
def create(
    name: str = typer.Argument(..., help="Network name"),
    cidr: str = typer.Option(
        ..., "--cidr", help="IP subnet in CIDR notation (e.g. 192.168.100.0/24)"
    ),
    gateway: str | None = typer.Option(
        None, "--gateway", help="Gateway IP for the bridge"
    ),
    no_nat: bool = typer.Option(False, "--no-nat", help="Disable NAT/masquerade"),
) -> None:
    """Create a named network."""
    try:
        config = create_network(
            name=name,
            cidr=cidr,
            gateway=gateway,
            nat=not no_nat,
        )
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Network '{config.name}' created")
    print_info(f"  CIDR:    {config.cidr}")
    print_info(f"  Gateway: {config.gateway}")
    print_info(f"  Bridge:  {config.bridge}")
    print_info(f"  NAT:     {'enabled' if config.nat_enabled else 'disabled'}")


@app.command(name="remove")
def remove(
    name: str = typer.Argument(..., help="Network name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Remove a named network."""
    if not force:
        typer.confirm(f"Remove network '{name}'?", abort=True)

    try:
        remove_network(name)
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    print_success(f"Network '{name}' removed")


@app.command(name="rm", hidden=True)
def rm(
    name: str = typer.Argument(..., help="Network name"),
    force: bool = typer.Option(False, "--force", "-f", help="Skip confirmation"),
) -> None:
    """Alias for remove."""
    remove(name=name, force=force)


@app.command()
def inspect(
    name: str = typer.Argument(..., help="Network name"),
    json_output: bool = typer.Option(False, "--json", help="Output as JSON"),
) -> None:
    """Show detailed information about a network."""
    try:
        info = inspect_network(name)
    except NetworkError as e:
        print_error(str(e))
        raise typer.Exit(code=1)

    if json_output:
        typer.echo(json.dumps(info, indent=2, default=str))
        return

    print_info(f"Network: {info['name']}")
    print_info(f"  CIDR:         {info.get('cidr', info.get('subnet', ''))}")
    print_info(f"  Gateway:      {info['gateway']}")
    print_info(f"  Bridge:       {info['bridge']}")
    print_info(f"  NAT:          {'enabled' if info['nat_enabled'] else 'disabled'}")
    print_info(f"  Bridge alive: {'yes' if info['bridge_exists'] else 'no'}")
    print_info(f"  Created:      {info['created_at']}")

    vms = cast(list[dict[str, str]], info.get("vms") or [])
    if vms:
        print_info(f"  VMs ({len(vms)}):")
        for vm in vms:
            print_info(f"    {vm['vm_name']}: {vm['ip']}")
    else:
        print_info("  VMs: none")

    # iptables rule dump for this bridge
    bridge = str(info["bridge"])
    rules = get_iptables_rules_for_bridge(bridge)
    if rules:
        print_info(f"  iptables rules for {bridge}:")
        for rule in rules:
            print_info(f"    {rule}")
    else:
        print_info(f"  iptables rules for {bridge}: none")
