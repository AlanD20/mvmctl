# CNI Network Backend

> **STATUS: Design Proposal — not implemented.** Current network stack uses raw TAP devices + Linux bridges + nftables/iptables firewall. No CNI support exists.
>
> **Last verified:** 2026-07-08

## 1. Motivation

mvmctl currently manages networking through its own hand-rolled stack:

- **`internal/infra/net/tap.go`** — creates TAP devices
- **`internal/infra/net/bridge.go`** — creates Linux bridges
- **`internal/infra/firewall/`** — nftables/iptables rules
- **`internal/infra/net/lease.go`** — DHCP-like IP lease pool

This works but has limitations:

- **No per-VM network namespace isolation** — all VMs share the host namespace. TAP devices, bridges, and firewall rules coexist globally. Name collisions, cross-VM interference, and cleanup orphans are real risks.
- **Hardcoded topology** — the bridge+TAP model is fixed. Users who want VLANs, macvlan, ipvlan, or custom CNI plugin chains cannot deviate.
- **IP management is primitive** — the built-in lease pool is a simple bitmask, not a real DHCP server. No DNS, no gateway configuration, no static IP reservation.
- **No DNS injection** — VMs must self-configure DNS. No `/proc/net/pnp` mechanism.
- **Cleanup is fragile** — stale TAPs and iptables rules accumulate on crash. The firewall reconciler helps but is reactive.

The [firecracker-go-sdk](https://github.com/firecracker-microvm/firecracker-go-sdk) demonstrates a proven alternative: **CNI-based networking** with per-VM network namespaces. This proposal outlines adding CNI as a second backend, not replacing the current stack.

## 2. What is CNI?

[CNI (Container Network Interface)](https://github.com/containernetworking/cni) is a standard originally from the container ecosystem. A CNI **plugin** is a standalone binary that the runtime invokes to set up or tear down network interfaces.

Basic flow:

```
mvmctl → libcni (Go library) → exec plugin binary with stdin JSON config → plugin creates/deletes network
```

### Plugin types

| Category | Examples | Purpose |
|----------|----------|---------|
| **Main** | `bridge`, `ptp`, `macvlan`, `ipvlan`, `vlan` | Creates the network interface |
| **IPAM** | `host-local`, `dhcp`, `static` | Allocates IP addresses |
| **Meta** | `firewall`, `portmap`, `bandwidth`, `tc-redirect-tap` | Adds capabilities on top |

A typical CNI chain config (`/etc/cni/conf.d/fcnet.conflist`):

```json
{
  "name": "fcnet",
  "cniVersion": "0.3.1",
  "plugins": [
    {
      "type": "ptp",
      "ipMasq": true,
      "ipam": { "type": "host-local", "subnet": "192.168.127.0/24" }
    },
    {
      "type": "firewall"
    },
    {
      "type": "tc-redirect-tap"
    }
  ]
}
```

The `tc-redirect-tap` plugin (from [awslabs/tc-redirect-tap](https://github.com/awslabs/tc-redirect-tap)) is specifically designed for Firecracker — it creates a TAP device mirroring the veth endpoint, which Firecracker attaches as its guest NIC.

### What the SDK does with CNI (per VM)

1. **Creates a netns** — bind-mounts a new network namespace at `/var/run/netns/<vm-id>`
2. **Invokes CNI** inside that netns — creates veth pair, assigns IP, adds firewall rules
3. **Chains `tc-redirect-tap`** — creates a TAP device mirroring the veth endpoint
4. **Attaches the TAP to Firecracker** — via `PUT /network-interfaces/{id}`
5. **Injects IP config** into the guest — generates `ip=` kernel boot param, or sets up `/proc/net/pnp` for DNS

## 3. Proposed Architecture

Add CNI as a **second network backend** alongside the existing raw TAP+bridge stack. Users select which to use via a config setting or per-VM flag.

```
┌──────────────────────────────────────────────────┐
│                  mvmctl                          │
│                                                  │
│   NetworkBackend interface                       │
│   ├── SetupTap(ctx, vm) → tapName, mac, err      │
│   ├── TeardownTap(ctx, vm) → err                 │
│   ├── SetupNAT(ctx, subnet) → err                │
│   ├── TeardownNAT(ctx, subnet) → err             │
│   └── AllocateIP(ctx, vm) → ip, gateway, dns     │
│                                                  │
│   Implementations:                                │
│   ├── RawBridgeBackend  ← current stack           │
│   └── CNIBackend        ← this proposal           │
│                                                  │
│   CNIBackend details:                             │
│   ├── Uses libcni (embedded Go library)           │
│   ├── Plugin binaries from ~/.cache/mvmctl/cni/   │
│   ├── Per-VM netns management                     │
│   └── IP from CNI result, not lease pool          │
└──────────────────────────────────────────────────┘
```

### Interface sketch

```go
// NetworkBackend abstracts VM network setup/teardown.
type NetworkBackend interface {
    // SetupTap creates and configures a network interface for a VM.
    // Returns the TAP device name and MAC address.
    SetupTap(ctx context.Context, vm *model.VMItem) (tapName, mac string, err error)

    // TeardownTap removes the network interface for a VM.
    TeardownTap(ctx context.Context, vm *model.VMItem) error

    // SetupNAT configures NAT/masquerade for a subnet.
    SetupNAT(ctx context.Context, subnet string) error

    // TeardownNAT removes NAT/masquerade for a subnet.
    TeardownNAT(ctx context.Context, subnet string) error

    // AllocateIP assigns an IP address for a VM.
    AllocateIP(ctx context.Context, vm *model.VMItem) (ip, gateway string, dns []string, err error)
}
```

### CNIBackend implementation sketch

```go
type CNIBackend struct {
    cniConfig   *libcni.NetworkConfigList
    binPath     string
    confDir     string
    cacheDir    string
}

func (b *CNIBackend) SetupTap(ctx context.Context, vm *model.VMItem) (tapName, mac string, err error) {
    // 1. Create per-VM netns at /var/run/netns/<vm-id>
    // 2. Invoke CNI plugin chain inside netns
    // 3. Parse CNI result for TAP name, MAC, IP, gateway, DNS
    // 4. Store netns path + CNI result in VM metadata
    // 5. Return tapName, mac
}

func (b *CNIBackend) TeardownTap(ctx context.Context, vm *model.VMItem) error {
    // 1. Invoke CNI DEL
    // 2. Delete netns
    // 3. Clean up state
}
```

## 4. CNI Plugin Binary Management

CNI plugins are standalone binaries that `libcni` exec's. They cannot be embedded into the mvmctl static binary (they're designed as subprocesses with stdin/stdout IPC).

### Distribution strategy

Follow the same pattern as firecracker/jailer:

1. **Download** — `mvm cni plugins install` fetches the [containernetworking-plugins](https://github.com/containernetworking/plugins/releases) release tarball and extracts plugin binaries to `~/.cache/mvmctl/cni/plugins/`
2. **Additional plugins** — `tc-redirect-tap` is fetched from its own release
3. **Config** — default CNI config is generated by `mvm init` or `mvm config set network.backend cni`

```
~/.cache/mvmctl/cni/
├── plugins/
│   ├── bridge
│   ├── host-local
│   ├── firewall
│   ├── tc-redirect-tap
│   └── ... (other CNI plugins)
└── config/
    └── fcnet.conflist    # auto-generated or user-provided
```

### What the user needs on their machine

| Requirement | Source | Notes |
|------------|--------|-------|
| `libcni` Go library | Embedded in mvmctl binary | Pure Go, zero C deps, compiles in |
| CNI plugin binaries | Downloaded by `mvm cni plugins install` | ~15MB for the full suite |
| CNI config file | Auto-generated or user-provided | Standard `.conflist` format |
| `CAP_NET_ADMIN` + `CAP_SYS_ADMIN` | Kernel capability | Required for netns + TAP creation |
| `tc-redirect-tap` plugin | Downloaded separately or chained | Only needed for Firecracker VMs |

**No package manager dependency.** Everything is self-contained within mvmctl's cache directory, just like firecracker and jailer binaries.

## 5. Migration Path

### Phase 1: NetworkBackend interface (no behavioral change)

- Extract `NetworkBackend` interface from current raw TAP/bridge/firewall code
- Rename existing implementation to `RawBridgeBackend`
- No behavioral change — defaults to `RawBridgeBackend`

### Phase 2: CNIBackend implementation (behind config flag)

- Implement `CNIBackend` with per-VM netns + `libcni` integration
- Add `mvm cni plugins install` subcommand
- Add `mvm config set defaults.network.backend cni` toggle
- Wire backend selection into VM create/respawn flows

### Phase 3: IP management unification

- Extract `IPAllocator` interface
- `RawBridgeBackend` uses the existing lease-pool allocator
- `CNIBackend` parses IP from CNI result (or uses a bridge-based allocator inside the netns)
- DNS configuration via `/proc/net/pnp` for both backends

### Phase 4: Default flip (eventual)

- Default backend remains `RawBridgeBackend` for backward compatibility
- Users opt into CNI explicitly
- Future major version may flip the default

## 6. Dependencies

| Go dependency | Purpose | Risk |
|--------------|---------|------|
| `github.com/containernetworking/cni/libcni` | CNI config parsing + plugin invocation | Low — pure Go, widely used, stable API |
| `github.com/containernetworking/cni/pkg/types/100` | CNI result types | Low — same module |
| `github.com/containernetworking/plugins/pkg/ns` | Netns management (create, delete, execute in) | Low — small, stable |

Non-Go dependencies (downloaded as assets, not linked):

| Binary | Source | Size |
|--------|--------|------|
| `bridge` (CNI plugin) | containernetworking-plugins release | ~5MB |
| `host-local` (IPAM) | containernetworking-plugins release | ~4MB |
| `firewall` (meta) | containernetworking-plugins release | ~4MB |
| `tc-redirect-tap` | GitHub release | ~5MB |
| Other plugins (ptp, macvlan, etc.) | containernetworking-plugins release | Included in tarball |

Total download: ~15-20MB for the full CNI plugin suite.

## 7. Open Questions

| Question | Options |
|----------|---------|
| **How does the user configure the CNI chain?** | Auto-generate a sensible default (ptp + host-local + tc-redirect-tap) with opt-in customization. Or require the user to place a `.conflist` file. |
| **Single vs. multiple interfaces per VM?** | CNI with IP config only supports 1 interface per VM (kernel `ip=` param limitation). Multiple interfaces without IP would work. Same restriction as the SDK. Match it for now. |
| **Bridge mode inside the netns?** | Users might want VMs on the same bridge inside the netns. This would require a `bridge` CNI plugin instead of `ptp`. Make it configurable. |
| **How does `mvm network ls` work with CNI?** | Current `mvm network` commands query bridge state. With CNI, networks are ephemeral per-VM. The `mvm network` command set would need to show CNI configs, not bridge state. |
| **Rate limiting?** | The SDK supports `InRateLimiter`/`OutRateLimiter` per interface. Should we expose this? |
| **MMDS?** | Firecracker's MMDS requires `AllowMMDS` on a network interface. The SDK supports this. Should we? |

## 8. Relationship to VMM-Agnostic Architecture

This proposal is **independent** of [MOVING_TO_AGNOSTIC_ARCHITECTURE.md](./MOVING_TO_AGNOSTIC_ARCHITECTURE.md). CNI is a network backend abstraction, not a VMM abstraction. CNI works with Firecracker specifically (via `tc-redirect-tap`) and would need different TAP creation strategies for other VMMs.

However, both proposals share the same pattern: **interface-based backends with runtime selection**. The `NetworkBackend` interface here mirrors the proposed `IVMMDriver` interface in the VMM-agnostic architecture. Design decisions from one can inform the other.

## 9. Prior Art

| Project | Approach |
|---------|----------|
| **firecracker-go-sdk** | `NetworkInterface` with optional `CNIConfiguration`. Netns management + CNI invocation baked into `Machine.Start()`. Uses `tc-redirect-tap` for TAP creation. |
| **firecracker-containerd** | Full CNI integration with custom plugins. Netns per VM, CNI for network setup, fuse/overlay for rootfs. |
| **Kata Containers** | CNI-based networking with `tc-redirect-tap` or `bridge` plugin. Netns per VM. |
| **Weave Ignite** | CNI-based with custom `ignite` plugin. Netns per VM. |
