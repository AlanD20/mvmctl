# Firecracker Manager — Phase 3 Requirements

## Overview

Phase 3 builds on the foundation established in Phase 2. It introduces first-class network
management (modelled after Docker's network CLI), SSH key management, a guided onboarding
flow, and a set of quality-of-life improvements to `vm create`. It also standardises the
removal verb across all subcommands and clarifies how the Firecracker API socket is
handled.

Read the Phase 2 requirements first. This document only describes what is new or changed.

---

## 1. Naming Convention Fix: `remove` everywhere

**Change:** Replace every use of `delete` and `rm` as removal verbs with `remove` across
all command groups. Keep `rm` as an alias so existing users and scripts are not broken, but
`remove` is the canonical verb going forward.

**Rationale:** `delete` and `rm` are used inconsistently across the existing command groups
(some use `rm`, some use `delete`). Docker uses `rm`, but `remove` reads more clearly in
help text and is easier to discover for new users. Standardising on `remove` with `rm` as
a short alias gives both consistency and ergonomics.

**Affected subcommands — rename to `remove`:**

- `vm rm` → `vm remove` (alias: `vm rm`)
- `asset kernel rm` → `asset kernel remove` (alias: `asset kernel rm`)
- `asset image rm` → `asset image remove` (alias: `asset image rm`)
- `asset bin rm` → `asset bin remove` (alias: `asset bin rm`)
- Any other `rm` or `delete` subcommands introduced in this phase or future phases must use
  `remove` as the canonical name from the start

---

## 2. `network` — Network management

### Motivation

In Phase 2, networking is fully automatic: a single shared bridge is created when the
first VM starts and torn down when the last VM stops. This works for simple cases but
gives the user no control over network topology, IP ranges, isolation between VM groups,
or naming.

Phase 3 introduces named networks, modelled after `docker network`. A network is a named,
persistent configuration object that encapsulates a bridge device, an IP range, and NAT
rules. VMs are attached to a network at creation time. Multiple networks can coexist on
the same host, providing isolation between groups of VMs.

The automatic Phase 2 behaviour is preserved as a built-in default network (e.g.
`default`) that is created implicitly when the first VM is created if no `--network` flag
is passed. This means Phase 2 behaviour still works unchanged for users who do not use
the new `network` subcommand.

### Subcommands

```
network ls                        List all networks: name, bridge device, IP range,
                                    number of attached VMs, and creation timestamp.
                                    Includes a flag on the default network.

network create <name>             Create a named network. Allocates a bridge device,
                                    configures the IP range, and sets up NAT. The network
                                    is immediately usable. Does not require any VM to exist.

network remove <name>             Remove a named network. Fails with a clear error if any
                                    VM is currently attached to this network — the user must
                                    remove those VMs first. Tears down the bridge device and
                                    NAT rules. Cannot remove the default network while any VM
                                    exists (alias: network rm)

network inspect <name>            Show full detail for a network: bridge device name,
                                    IP range, gateway IP, NAT rules applied, list of
                                    attached VMs with their IPs, and the snapshot of host
                                    state changes made when the network was created.
```

### `network create` flags

```
--subnet <cidr>         IP range for this network, e.g. 192.168.100.0/24
                          Default: next available /24 block from a configured pool,
                          or the value in the config file under network.subnet_pool.
                          The CLI must detect and reject overlapping subnets.

--gateway <ip>          IP address assigned to the host bridge (gateway for VMs).
                          Default: first usable host in the subnet (e.g. .1)

--no-nat                Do not configure NAT/masquerade for this network.
                          Use this for host-only networks where outbound internet
                          access from VMs is not needed.
```

### Attaching a VM to a network

`vm create` gains a `--network` flag:

```
--network <name>        Name of the network to attach this VM to.
                          Default: "default" (the auto-managed Phase 2 network).
```

If `--network` and `--ip` are both passed, `--ip` is used as the static guest IP.
If only `--network` is passed, the guest IP is auto-allocated from the network's subnet.
If neither is passed, the default network is used with auto-allocated IP — preserving
Phase 2 behaviour exactly.

### Network state persistence

Each network's configuration is stored in the cache under `<cache-root>/networks/`:

```
<cache-root>/
  networks/
    <network-name>/
      config.json       # subnet, gateway, nat enabled, bridge device name, creation time
      state.json        # host changes made for this network (same format as host/state.json)
      leases.json       # IP allocations: vm name → assigned IP, indexed for fast lookup
```

This persistence means networks survive process restarts and reboots. On startup, the CLI
reconciles the stored network state against the actual host state (bridge device exists,
NAT rules present) and warns the user if drift is detected.

---

## 3. `vm create` — Additional flags

### `--network`

Described in the network section above.

### `--ip`

Already exists in Phase 2 but its interaction with `--network` must now be explicitly
handled: if `--ip` is given without `--network`, it is assigned within the default network.
If the given IP falls outside the target network's subnet, fail with a clear error.

### `--mac`

```
--mac <mac-address>     Custom MAC address for the VM's network interface.
                          Must be a valid unicast MAC address (locally administered bit set
                          is recommended: second hex digit must be 2, 6, A, or E).
                          Default: auto-generated deterministically from the VM name using
                          a stable hash, prefixed with a locally administered OUI
                          (e.g. 02:xx:xx:xx:xx:xx). This ensures the same VM name always
                          gets the same MAC, which is useful for DHCP reservations and
                          debugging.
```

### `--user-data`

```
--user-data <path>      Path to a custom cloud-init user-data file. This file is injected
                          directly into the VM's rootfs at /var/lib/cloud/seed/nocloud/
                          user-data, replacing the default generated user-data entirely.
                          The file must be valid cloud-init syntax (starts with #cloud-config
                          or is a valid multipart MIME document). The CLI must validate the
                          file exists and is readable before proceeding; it should warn (not
                          fail) if the file does not begin with a recognised cloud-init header.
                          This allows power users to fully customise first-boot behaviour:
                          package installs, user creation, arbitrary scripts, etc.
```

**Interaction with `--ssh-key`:** if `--user-data` is passed alongside `--ssh-key`, the
CLI must merge the SSH public key into the provided user-data rather than silently ignoring
one or the other. The merge strategy: if the user-data already contains an `ssh_authorized_keys`
section, append the key to it; if it does not, inject a minimal `users` block. Document
this behaviour clearly in `README.md`.

---

## 4. `key` — SSH key management

### Motivation

In Phase 2, SSH keys are referenced by file path. There is no central registry of known
keys, and users must remember which key goes with which VM. The `key` subcommand introduces
a named key store backed by the cache folder, making it easy to reference keys by name
when creating VMs and to audit which keys are in use.

### Subcommands

```
key ls                            List all keys in the cache: name, fingerprint,
                                    comment, date added, and whether the private key is
                                    also present locally.

key add <name> <public-key-path>  Import an existing public key into the cache under the
                                    given name. Only the public key is stored — the CLI
                                    never reads or stores private keys. The name is used
                                    to reference this key with --ssh-key <name> in vm create.

key create <name>                 Generate a new ED25519 keypair. The private key is written
                                    to the location specified by --output (default: ~/.ssh/).
                                    The public key is automatically added to the cache under
                                    the given name. Prints the public key fingerprint and the
                                    path of the private key on completion.

key remove <name>                 Remove a key from the cache. Does not delete the key file
                                    from disk. Warns if any existing VM was created with this
                                    key (those VMs are not affected — the key is already baked
                                    into their rootfs). (alias: key rm)

key inspect <name>                Show the full public key content, fingerprint, algorithm,
                                    and comment for a named key.
```

### `key create` flags

```
--output <dir>          Directory where the private key file is written.
                          Default: ~/.ssh/
                          The private key is named <name> and the public key <name>.pub,
                          matching standard OpenSSH naming conventions.

--comment <string>      Comment embedded in the public key. Default: <name>@<hostname>

--overwrite             Overwrite an existing key file if one already exists at the output
                          path. Without this flag, the command fails if the file exists.
```

### Integration with `vm create`

`vm create --ssh-key` must accept either a file path (existing Phase 2 behaviour) or a
named key from the cache. Resolution order: if the value matches a name in the key cache,
use the cached public key; otherwise treat it as a file path. This is the same
name-or-path resolution already used for `--kernel` and `--image`.

### Storage

Keys are stored in `<cache-root>/keys/`:

```
<cache-root>/
  keys/
    <name>.pub          # the public key file
    registry.json       # index: name → fingerprint, comment, date added, algorithm
```

Only public keys are stored in the cache. The `registry.json` index allows fast listing
without reading every key file individually.

---

## 5. `configure` — Guided onboarding

### Motivation

New users face a sequence of steps before they can create their first VM: initialise the
host, download a kernel, download an image, and add an SSH key. These steps are documented
but require the user to read the docs carefully and run multiple commands in the right
order. The `configure` subcommand collapses this into a single guided, interactive flow
that is safe to run multiple times (fully idempotent).

### Behaviour

`configure` is an interactive wizard that runs the following steps in order, prompting the
user at each one and skipping steps that are already complete:

1. **Host initialisation** — checks if `host init` has already been run (snapshot exists
   and settings are in the expected state). If not, explains what changes will be made to
   the host and asks for confirmation before running `host init`. If the user declines,
   prints instructions for manual setup and continues.

2. **Binary download** — checks if a Firecracker binary is already in the cache. If not,
   lists the latest available version and asks the user to confirm download. Equivalent to
   `asset bin fetch <latest>` followed by `asset bin use <latest>`.

3. **Kernel download** — checks if a kernel is already cached. If not, asks the user which
   kernel type they want (minimal or upstream, with a description of each) and downloads
   accordingly.

4. **Image download** — checks if an image is already cached. If not, presents the
   supported image types as a numbered menu and downloads the selected one.

5. **SSH key setup** — checks if any key exists in the key cache. If not, asks the user
   whether to generate a new keypair or import an existing public key:
   - If generating: runs `key create` interactively (prompts for name and output path)
   - If importing: prompts for the path to an existing public key and runs `key add`

6. **Summary** — prints a final summary showing everything that is now configured and the
   exact command the user can run to create their first VM.

### Flags

```
--non-interactive       Run configure in non-interactive mode, using all defaults and
                          skipping any step that requires user input. Suitable for use
                          in provisioning scripts or CI. Missing required assets are
                          downloaded automatically using default selections.

--skip-host             Skip the host init step (useful if the user has already done it
                          manually or does not have sudo access).
```

### Idempotency requirement

Every check in `configure` must use the same underlying API functions used by the
individual commands (`host ls`, `asset bin ls`, `asset kernel ls`, etc.). It must never
duplicate logic — it is purely a UX layer on top of existing API calls. This means
`configure` stays correct automatically as those APIs evolve.

---

## 6. Firecracker API Socket (`--enable-api-socket`)

### Change from Phase 2

In Phase 2, the flag is named `--enable-socket`. Rename it to `--enable-api-socket` for
clarity — "socket" alone is ambiguous; "API socket" makes clear it is the Firecracker
HTTP-over-Unix-socket management API.

Update the alias in the config file from `enable_socket` to `enable_api_socket`.

### Socket path

The socket file must be created inside the VM's cache directory, not in a system location:

```
<cache-root>/vms/<vm-name>/firecracker.api.socket
```

This is consistent with the existing convention of keeping all per-VM runtime files under
the VM's cache subdirectory. It also means the socket is automatically cleaned up when
`vm remove` deletes the VM's cache directory.

### Behaviour

- If `--enable-api-socket` is passed to `vm create`, Firecracker is started with
  `--api-sock <cache-root>/vms/<vm-name>/firecracker.api.socket`.
- If the flag is not passed (default), Firecracker is started with `--no-api`.
- `vm ls` should indicate whether the API socket is enabled for each running VM (e.g. a
  column or symbol in the table output).
- The socket path must be stored in the VM's `config.json` so `vm inspect` (if
  implemented) and other tooling can locate it without guessing.

---

## Summary of `vm create` flags (Phase 2 + Phase 3 combined)

For reference, the complete flag set for `vm create` after Phase 3:

```
--name <string>             VM name; hostname inside the guest and cache directory name
--network <name>            Named network to attach to (default: "default")
--ip <cidr>                 Static guest IP; auto-allocated from network subnet if omitted
--mac <mac-address>         Custom MAC address; deterministically generated if omitted
--kernel <name|path>        Cached kernel name or file path
--image <name|path>         Cached image name or file path
--ssh-key <name|path>       Named key from key cache or path to a public key file
--user-data <path>          Path to a custom cloud-init user-data file
--vcpus <int>               Number of vCPUs (default: 2)
--memory <int>              Memory in MiB (default: 2048)
--enable-api-socket         Enable the Firecracker HTTP API socket (default: off)
--enable-pci                Enable PCI device support (default: off)
```
