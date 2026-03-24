# Firecracker Manager — Phase 4 Requirements

## Overview

Phase 4 refines and clarifies requirements introduced in earlier phases based on real usage
feedback. It fixes ambiguities in key management, networking, VM creation, and the host
command group. It also adds graceful VM shutdown, help command consistency, API
documentation, and prepares the project for public distribution via PyPI, pipx, and uvx.

As with all phases, requirements here override any conflicting requirement in a lower phase.
Read this document fully before implementing anything, then cross-reference phase 3 and
phase 2 for anything not covered here.

---

## 1. `key` — SSH key management (revised)

### What keys store

The key cache stores **named public keys only**. Each key has a name that acts as its
identifier everywhere in the CLI. The name is what the user passes to `--ssh-key` when
creating a VM, to `key remove` when deleting a key, and to `key inspect` when viewing one.

Private keys are never stored in the cache. If a key is generated with `key create`, the
private key is written to the user's chosen output directory (default `~/.ssh/`) and the
public key is registered in the cache under the given name. The cache only ever holds the
`.pub` file.

### Subcommand behaviour (canonical)

```
key ls              Print a table of all registered keys. Columns: Name, Fingerprint,
                      Algorithm, Comment, Date Added. The name column is the most important
                      — it is what users copy to pass as --ssh-key <name>.

key add <name> <path-to-public-key>
                    Import an existing .pub file into the cache under the given name.
                      Fails if a key with that name already exists (use --overwrite to
                      replace). Prints the fingerprint on success so the user can verify
                      they added the right key.

key create <name>   Generate a new ED25519 keypair. Writes the private key to --output
                      (default ~/.ssh/<name>) and the public key to --output/<name>.pub.
                      Automatically registers the public key in the cache under <name>.
                      Prints the private key path and public key fingerprint on completion.

key remove <name>   Remove a named key from the cache registry and delete its .pub file
                      from the cache. Does not touch any private key on disk. Warns if
                      the key was used to create a VM (the VM is unaffected — the key is
                      already baked into the rootfs). Alias: key rm
```

### Integration with `vm create --ssh-key`

`--ssh-key` accepts a key name (looked up in the cache registry) or a file path (used
directly as a public key). Resolution: try the name first; if no match, treat as a path.
If neither resolves, fail with a clear error listing available key names.

---

## 2. `network` — Network management (revised and clarified)

### What a network is

A network is a named Linux bridge with an associated CIDR, a host-side gateway IP, and
NAT rules that give VMs on that bridge outbound internet access. Each network is fully
independent — its bridge device, IP range, and iptables rules are separate from every
other network.

Networks are persistent: they survive process restarts and reboots (the bridge device and
iptables rules remain until explicitly removed or the host is pruned). The CLI stores each
network's configuration in the cache so it can reconstruct and verify the network's state
at any time.

### Default network

When the user runs `configure` (or `host init`), a default network is created
automatically using a sensible built-in CIDR (e.g. `10.10.0.0/24`). This network is named
`default` and is used automatically for any VM created without `--network`. The user never
needs to create or name a network unless they want isolation between groups of VMs or a
custom IP range.

The default network is created once and never automatically torn down, even when all VMs
are removed. It persists until the user explicitly runs `network remove default` or
`host prune`.

### Subcommands

```
network ls              List all networks. Columns: Name, Bridge Device, CIDR, Gateway,
                          VM Count, NAT Enabled. Mark the default network.

network create <name>   Create a named bridge network with the given CIDR. Allocates the
                          bridge device, assigns the gateway IP to it, and configures NAT
                          via iptables. The network name is used as an identifier when
                          attaching VMs. Fails if the name already exists or if the CIDR
                          overlaps an existing network.

network remove <name>   Remove a named network. Tears down the bridge device and removes
                          the associated iptables rules. Fails with a clear error if any
                          VM is currently attached to this network — those VMs must be
                          removed first. The default network can be removed, but doing so
                          will cause `vm create` without `--network` to fail until a new
                          default is established. Alias: network rm

network inspect <name>  Show full detail: CIDR, gateway, bridge device name, NAT status,
                          iptables rules applied, list of attached VMs with their assigned
                          IPs, and the date the network was created.
```

### `network create` flags

```
--cidr <cidr>           IP range for this network, e.g. 192.168.100.0/24.
                          Required. The CLI must validate the CIDR and reject it if it
                          overlaps any existing network's CIDR.

--gateway <ip>          IP address assigned to the host side of the bridge (used as the
                          default gateway by VMs on this network).
                          Default: first usable host address in the CIDR (e.g. .1).

--no-nat                Do not configure NAT/masquerade for this network. Use for
                          host-only networks where VMs do not need outbound internet access.
```

### IP allocation within a network

Every network maintains a lease table in the cache (`networks/<name>/leases.json`). When a
VM is created without `--ip`, the CLI automatically picks an unused IP from the network's
CIDR (excluding the gateway) and reserves it in the lease table. When a VM is removed, its
IP is released back to the pool.

When `--ip` is passed, the CLI validates that the IP falls within the network's CIDR and
is not already in use, then reserves it in the lease table.

There is no DHCP — all IP assignment is done statically by the CLI at VM creation time and
passed to the guest via the Firecracker boot args (`ip=` kernel parameter) and cloud-init
`network-config`, exactly as the bash proof-of-concept does.

---

## 3. `vm` — Revised behaviours

### Graceful shutdown in `vm remove`

`vm remove` must follow the exact shutdown sequence defined in `multi-vm/delete-vm.sh`:

1. Read the PID from `<cache-root>/vms/<name>/firecracker.pid`.
2. If the Firecracker API socket is enabled (`enable_api_socket: true` in the VM's stored
   config), attempt a graceful shutdown first by sending a `SendCtrlAltDel` action via the
   Firecracker HTTP API over the Unix socket. Wait up to 5 seconds for the process to exit.
3. If the process is still running after the graceful attempt (or if the API socket is not
   enabled), send SIGTERM. Wait 1 second.
4. If still running, send SIGKILL.
5. Clean up: remove the PID file, socket file, tap device associated with this VM, and
   release the VM's IP back to the network's lease table.
6. Remove the SSH known-hosts entry for the VM's IP (`ssh-keygen -R <ip>`).
7. Delete the VM's cache directory (`<cache-root>/vms/<name>/`).
8. If this was the last VM on a network, do **not** tear down the network — networks
   persist independently of VMs (see Section 2).

### No `vm setup`

There is no `vm setup` subcommand. Setup is handled automatically: the default network is
created during `configure` / `host init`, and any per-VM setup (rootfs copy, cloud-init
injection, Firecracker JSON generation) happens inside `vm create`. Nothing requires a
separate setup step.

### MAC address

If `--mac` is not passed to `vm create`, a random locally-administered MAC address is
generated (prefix `02:`). Static MACs passed via `--mac` are validated as well-formed
unicast MAC addresses before use. There is no deterministic generation from VM name —
random is simpler and equally correct for the use case.

### User data (`--user-data`)

The file passed to `--user-data` is a standard cloud-init config file (typically starting
with `#cloud-config`). It replaces the default generated `user-data` entirely. The CLI
must validate that the file exists and is readable. It should warn (not fail) if the file
does not begin with `#cloud-config` or a valid MIME boundary, since the user may
intentionally use a non-standard format.

If `--user-data` and `--ssh-key` are both passed: inject the SSH public key into the
provided user-data. If the file contains an `ssh_authorized_keys` section, append the key.
If it does not, add a minimal block. Do this merge in memory — never modify the original
file on disk.

---

## 4. `host` — Revised and extended

### `host prune` (replaces `asset cache clear` for networking)

`host prune` is a destructive command that removes **all** networking configuration added
by this tool from the host. This includes every bridge device, every tap device, every
iptables/nftables rule, and the IP forwarding sysctl change. It is the nuclear option —
use it to completely clean up a machine.

`host prune` must:
1. Print a clear warning listing everything it is about to remove.
2. Ask for explicit confirmation unless `--force` is passed.
3. Refuse to run if any VM is currently running (check for live PID files). Fail with a
   list of running VMs and instructions to remove them first.
4. After removing all networking, update the host state snapshot to reflect that the host
   has been restored to its pre-init state.
5. Not remove any VM cache files, images, kernels, or binaries — only networking.

This replaces the earlier `asset cache clear` for networking-related cleanup. The name
`cleanup` (mentioned in phase 3) is also retired — the canonical name is `prune`.

### `host init` idempotency

`host init` must be fully idempotent. Running it on a machine that is already initialised
must produce no changes and exit cleanly with a message confirming the host is already
configured. It must not fail or produce warnings when rerun.

---

## 5. `help` — Consistent help behaviour

The following must all produce identical output:

```
<binary> <command> --help
<binary> <command> -h
<binary> <command> help
<binary> help <command>
```

The `help` subcommand (or positional argument) must be treated as a trigger for the same
help output as `--help` / `-h` at every level of the command hierarchy. This means:

```
key help        → same as: key --help
network help    → same as: network --help
vm help         → same as: vm --help
key add help    → same as: key add --help
```

This is straightforward to implement with Typer or Click by registering a `help` subcommand
that calls the parent group's `get_help()` method, or by adding a `help` argument that
triggers `ctx.get_help()`. Pick whichever approach produces the cleanest implementation
for the chosen CLI framework.

---

## 6. API documentation (`API.md`)

Create `firecracker-manager/docs/API.md`. This document is for developers who want to use
the Python API directly — either to build a TUI/GUI, write automation scripts, or
contribute to the project — without going through the CLI.

The document must cover:

### Structure

- **Introduction** — one paragraph explaining that every CLI command maps 1:1 to a
  Python function in `firecracker_manager/api/`, and that the CLI is a thin presentation
  layer on top of this API.
- **Installation** — how to install the package so the API is importable:
  `pip install firecracker-manager` or `pip install -e .` from source.
- **Module overview** — a table mapping each `api/` module to its responsibility:

  | Module | Responsibility |
  |---|---|
  | `api/vms.py` | VM lifecycle: create, remove, list, ssh, logs |
  | `api/network.py` | Network management: create, remove, list, inspect, IP allocation |
  | `api/assets.py` | Asset management: kernels, images, binaries |
  | `api/keys.py` | SSH key registry: add, create, remove, list |
  | `api/host.py` | Host initialisation, state inspection, prune |

- **Data models** — document every public model (dataclass or Pydantic model) in
  `models.py` with field names, types, and a one-line description of each field.
- **Error handling** — document the exception hierarchy in `exceptions.py`. Show a code
  example of catching typed exceptions.
- **Function reference** — for each public function in each `api/` module, document:
  - Signature with type annotations
  - What it does (one paragraph)
  - Parameters (name, type, default, description)
  - Return value (type and what it represents)
  - Exceptions it may raise
- **End-to-end example** — a complete working Python script that:
  1. Calls `host.init()` to ensure the host is configured
  2. Calls `assets.fetch_binary()` to ensure a Firecracker binary is available
  3. Calls `assets.fetch_kernel()` to ensure a kernel is available
  4. Calls `assets.fetch_image()` to ensure a rootfs image is available
  5. Calls `keys.add()` to register an SSH key
  6. Calls `network.create()` to create a named network
  7. Calls `vms.create()` to create a VM on that network
  8. Calls `vms.list()` and prints the result
  9. Calls `vms.remove()` to clean up

  The example must be complete and runnable — not pseudocode. Use realistic values.

---

## 7. Versioning

The CLI must expose its version via both `--version` and the `version` subcommand. The
version value must be read from `pyproject.toml` under `[project] version` using the same
build-time injection mechanism already established for the project name. There must be no
separate version constant anywhere else in the codebase — the version has one source of
truth, the same way the project name does.

```
<binary> --version      Print the version string and exit.
<binary> version        Identical output to --version. Follows the same help consistency
                          rule: version help and version --help both show usage.
```

`CONTRIBUTING.md` must include a dedicated "Bumping the version" section explaining the
one file to edit (`pyproject.toml`) and the exact steps required to cut a release. This
section must reference `docs/RELEASE.md` for the full release process.

---

## 8. Release documentation (`docs/RELEASE.md`)

Create `firecracker-manager/docs/RELEASE.md` as the single reference for anyone cutting a
release. Nothing release-related should require reading multiple files.

The document must cover:

- **Bumping the version** — edit `[project] version` in `pyproject.toml`. Explain
  semantic versioning (`MAJOR.MINOR.PATCH`) and when to increment each component.
- **Tagging and pushing** — the exact git commands to create an annotated tag
  (`git tag -a v1.2.3 -m "..."`) and push it (`git push origin v1.2.3`). Explain that
  pushing a tag is what triggers the `release.yml` workflow.
- **What the release workflow does automatically** — binary builds on ubuntu-22.04 and
  ubuntu-24.04, PyPI publish, GitHub release creation, artifact upload. The human does not
  need to do any of this manually.
- **Verifying a release** — how to confirm the binary works (`curl` download + `--version`
  check), how to confirm the PyPI package is live (`pip install <project-name>==<version>`),
  and how to confirm pipx/uvx work.
- **Issuing a hotfix** — branch from the release tag, fix, bump patch version, tag, push.
- **Yanking a bad release** — how to yank from PyPI (`pip install twine` + `twine ... yank`)
  and how to mark a GitHub release as a pre-release or delete it.

---

## 9. Distribution — binary and public package

The project must be fully prepared for public distribution through three channels:
the prebuilt binary (GitHub releases), pip, and pipx/uvx.

### `pyproject.toml` requirements

The `pyproject.toml` must be complete and production-ready:

```toml
[project]
name = "<project-name>"           # single source of truth for the project name
version = "0.1.0"
description = "A CLI for managing Firecracker microVMs"
readme = "README.md"
license = { text = "MIT" }
requires-python = ">=3.13"
dependencies = [
  # list all runtime dependencies with minimum version pins
]

[project.scripts]
<project-name> = "firecracker_manager.cli.main:app"

[project.optional-dependencies]
dev = [
  "pytest",
  "pytest-cov",
  "pytest-mock",
  "ruff",
  "mypy",
  "pyinstaller",
]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"
```

The `[project.scripts]` entry is what makes the CLI available as a command after
`pip install`. It must point to the Typer/Click app object in `cli/main.py`.

### pipx and uvx compatibility

The package must be installable and runnable via:

```bash
pipx install firecracker-manager
uvx firecracker-manager
```

For `uvx` (uv's tool runner), the package must declare its entry point correctly in
`[project.scripts]` and must not have any import-time side effects that require root
or a specific system state. All system interactions (KVM access, iptables, etc.) must
happen lazily, only when the relevant command is actually invoked.

### Binary build (PyInstaller)

The `release.yml` GitHub Actions workflow (defined in phase 2) must produce a
self-contained binary using PyInstaller in `--onefile` mode. The binary must:

- Be named `<project-name>` (derived from `pyproject.toml`)
- Include all runtime dependencies bundled inside
- Run on the target machine with no Python installation required
- Be built on both `ubuntu-22.04` and `ubuntu-24.04` and uploaded as separate release
  assets (glibc version differences mean a binary from 24.04 will not run on 22.04)

The binary build command must be documented in `README.md` under "Building from source"
(as specified in phase 2) and in `CONTRIBUTING.md` under the build system section.

### `README.md` additions for this phase

Add an "Installation" section that covers all three methods side by side:

```markdown
## Installation

**Download the binary** (no Python required):
Download the latest release from the GitHub releases page for your Ubuntu version.

**Install via pip:**
pip install <project-name>

**Install via pipx** (recommended for CLI tools — isolates dependencies):
pipx install <project-name>

**Install via uvx** (run without installing):
uvx <project-name> --help

**Build from source:**
See the "Building from source" section below.
```

---

## 10. Summary of all `vm create` flags (phases 1–4 combined)

For the agent's reference, the complete and final flag set for `vm create`:

```
--name <string>             VM name; used as hostname inside the guest and as the
                              name of the VM's cache subdirectory

--network <name>            Name of the network to attach this VM to.
                              Default: "default" (created during host init / configure)

--ip <address>              Static IP address for the guest, must fall within the
                              named network's CIDR. If omitted, an IP is automatically
                              allocated from the network's available address pool.

--mac <mac-address>         Static MAC address for the guest network interface.
                              If omitted, a random locally-administered MAC is generated
                              (prefix 02:).

--kernel <name|path>        Cached kernel name or file path to use for booting.

--image <name|path>         Cached rootfs image name or file path.

--ssh-key <name|path>       Name of a key registered in the key cache, or a path to a
                              public key file on disk.

--user-data <path>          Path to a cloud-init config file (typically #cloud-config).
                              Replaces the default generated user-data. If --ssh-key is
                              also passed, the public key is merged into this file in
                              memory without modifying the original.

--vcpus <int>               Number of vCPUs. Default: 2.

--memory <int>              Memory in MiB. Default: 2048.

--enable-api-socket         Enable the Firecracker HTTP API socket. The socket is created
                              at <cache-root>/vms/<name>/firecracker.api.socket.
                              Default: off.

--enable-pci                Enable PCI device support. Default: off.
                              Required by some Linux distributions to boot correctly.
```
