# Phase 3 — Status

> Source: `python-cli-phase-3.md`
> Last updated: 2026-03-23

---

## §1 Naming Convention Fix: `remove` everywhere

| Requirement | Status | Notes |
|---|---|---|
| `remove` is the canonical removal verb; `rm` is an alias | ✅ | |
| `vm rm` → `vm remove` (alias: `vm rm`) | ✅ | |
| `asset kernel rm` → `asset kernel remove` (alias: `asset kernel rm`) | ✅ | |
| `asset image rm` → `asset image remove` (alias: `asset image rm`) | ✅ | |
| `asset bin rm` → `asset bin remove` (alias: `asset bin rm`) | ✅ | |
| `delete` verb not used as a primary verb anywhere | ✅ | |

---

## §2 `network` — Network management

### Motivation / Architecture

| Requirement | Status | Notes |
|---|---|---|
| Named networks (persistent configuration objects) | ✅ | Each network stored in `<cache-root>/networks/<name>/` |
| Phase 2 automatic behaviour preserved as built-in "default" network | ✅ | Default network created during `host init` / `configure` |
| Multiple networks can coexist on the same host | ✅ | |
| VMs attach to a network at creation time | ✅ | `--network` flag on `vm create` |

### Subcommands

| Command | Status | Notes |
|---|---|---|
| `network ls` — name, bridge device, IP range, VM count, creation timestamp, default flag | ✅ | |
| `network create <name>` — allocate bridge, configure IP range, set up NAT | ✅ | |
| `network remove <name>` — fail if VMs attached; tear down bridge + NAT; alias `network rm` | ✅ | |
| `network inspect <name>` — CIDR, gateway, bridge, NAT status, iptables rules, attached VMs + IPs, creation date | ✅ | |

### `network create` flags

| Flag | Status | Notes |
|---|---|---|
| `--subnet <cidr>` (Phase 3 name; renamed `--cidr` in Phase 4) | ✅ | Phase 4 §2 uses `--cidr`; both handled |
| `--gateway <ip>` — default: first host in subnet | ✅ | |
| `--no-nat` — skip NAT/masquerade | ✅ | |

### VM integration

| Requirement | Status | Notes |
|---|---|---|
| `vm create --network <name>` | ✅ | |
| `--network` + `--ip` together: `--ip` used as static guest IP | ✅ | |
| `--network` only: auto-allocate IP from network's subnet | ✅ | |
| Neither flag: default network + auto-allocate (Phase 2 behaviour) | ✅ | |

### Network state persistence

| Requirement | Status | Notes |
|---|---|---|
| `<cache-root>/networks/<name>/config.json` — subnet, gateway, nat, bridge, creation time | ✅ | |
| `<cache-root>/networks/<name>/state.json` — host changes (same format as host/state.json) | ✅ | |
| `<cache-root>/networks/<name>/leases.json` — vm name → assigned IP | ✅ | |
| Networks survive process restarts and reboots | ✅ | |
| CLI reconciles stored state against actual host state on startup; warns on drift | ✅ | |

---

## §3 `vm create` — Additional flags

| Flag | Status | Notes |
|---|---|---|
| `--network <name>` | ✅ | See §2 above |
| `--ip` interaction with `--network` (IP outside network CIDR → clear error) | ✅ | |
| `--mac <mac-address>` — custom MAC; valid unicast MAC enforced | ✅ | Phase 4 §3 clarifies: random `02:` prefix if omitted |
| `--user-data <path>` — replace default user-data; validate exists + readable; warn if not `#cloud-config` | ✅ | |
| `--user-data` + `--ssh-key`: merge in memory — append key to `ssh_authorized_keys` or inject minimal block | ✅ | Phase 4 §3 clarifies append to all users + root |

---

## §4 `key` — SSH key management

### Subcommands

| Command | Status | Notes |
|---|---|---|
| `key ls` — Name, Fingerprint, Comment, Date Added, private key present flag | ✅ | |
| `key add <name> <public-key-path>` — import .pub into cache; fail if name exists (use `--overwrite`) | ✅ | |
| `key create <name>` — generate ED25519 keypair; write private to `--output`; register public in cache; print fingerprint + private key path | ✅ | |
| `key remove <name>` — remove from cache; warn if used by a VM; alias `key rm` | ✅ | |
| `key inspect <name>` — full public key content, fingerprint, algorithm, comment | ✅ | |

### `key create` flags

| Flag | Status |
|---|---|
| `--output <dir>` — default: `~/.ssh/` | ✅ |
| `--comment <string>` — default: `<name>@<hostname>` | ✅ |
| `--overwrite` — overwrite existing key file | ✅ |

### Integration with `vm create`

| Requirement | Status | Notes |
|---|---|---|
| `--ssh-key` accepts name (cache lookup) or file path | ✅ | |
| Resolution: name first, then path; fail clearly if neither resolves | ✅ | |

### Storage

| Requirement | Status | Notes |
|---|---|---|
| `<cache-root>/keys/<name>.pub` — public key file | ✅ | |
| `<cache-root>/keys/registry.json` — index: name → fingerprint, comment, date added, algorithm | ✅ | |
| Private keys never stored in cache | ✅ | |

---

## §5 `configure` — Guided onboarding

| Requirement | Status | Notes |
|---|---|---|
| Interactive wizard collapsing all setup steps into one command | ✅ | `cli/configure.py` |
| Step 1: host init check — explain changes, ask confirmation, or skip + print manual instructions | ✅ | |
| Step 2: binary download check — list latest version, confirm download | ✅ | |
| Step 3: kernel download check — ask minimal vs upstream | ✅ | |
| Step 4: image download check — numbered menu of supported types | ✅ | |
| Step 5: SSH key setup — generate new keypair or import existing | ✅ | |
| Step 6: summary — show what is configured + exact `vm create` command to run next | ✅ | |
| `--non-interactive` flag — use all defaults, skip interactive prompts | ✅ | |
| `--skip-host` flag — skip host init step | ✅ | |
| Fully idempotent — uses same API functions as individual commands | ✅ | |
| Never duplicates logic — calls `host ls`, `asset bin ls`, `asset kernel ls` etc. | ✅ | |

---

## §6 Firecracker API Socket (`--enable-api-socket`)

| Requirement | Status | Notes |
|---|---|---|
| Flag renamed from `--enable-socket` to `--enable-api-socket` | ✅ | |
| Config key renamed from `enable_socket` to `enable_api_socket` | ✅ | |
| Socket path: `<cache-root>/vms/<vm-name>/firecracker.api.socket` | ✅ | |
| `vm create --enable-api-socket` → Firecracker started with `--api-sock <path>` | ✅ | |
| Default (no flag) → Firecracker started with `--no-api` | ✅ | |
| `vm ls` indicates whether API socket is enabled for each VM | ✅ | |
| Socket path stored in VM's `config.json` | ✅ | |

---

## Summary of `vm create` flags (Phase 2 + Phase 3)

| Flag | Status |
|---|---|
| `--name` | ✅ |
| `--network <name>` | ✅ |
| `--ip <cidr>` | ✅ |
| `--mac <mac-address>` | ✅ |
| `--kernel <name\|path>` | ✅ |
| `--image <name\|path>` | ✅ |
| `--ssh-key <name\|path>` | ✅ |
| `--user-data <path>` | ✅ |
| `--vcpus <int>` | ✅ |
| `--memory <int>` | ✅ |
| `--enable-api-socket` | ✅ |
| `--enable-pci` | ✅ |

---

**Overall Phase 3 Status: ✅ COMPLETE**
