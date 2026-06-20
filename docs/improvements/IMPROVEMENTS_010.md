# Per-VM Network Namespaces with Firecracker Jailer

> **STATUS:** Design Document — not implemented.
>
> **Last verified:** 2026-06-19

**Phase:** Future
**Complexity:** Very High
**Depends on:** Firecracker jailer binary, kernel netns support

---

## 1. Problem

When restoring a Firecracker from snapshot, the guest memory contains a **frozen IP address** and **MAC address** from the moment the snapshot was taken. Every clone restored from the same snapshot has the same IP/MAC in memory.

The current `mvm snapshot restore --network <X>` approach creates a new TAP with a new IP on the host bridge, but:

1. Firecracker's snapshot restore **ignores the `--config-file` network config** — the MAC and TAP name from the vmstate file are used instead.
2. `network_overrides` can remap the TAP device name, but the MAC stays frozen.
3. The guest's frozen IP doesn't match the new TAP's subnet, so networking breaks.

Two solutions exist:
- **Vsock agent exec** (current approach) — fix TAP with `network_overrides`, then exec `ip addr` commands inside the guest via vsock agent.
- **Per-VM network namespaces** (this document) — each clone runs in its own network namespace with the same frozen IP. No guest reconfiguration needed.

---

## 2. The Namespace Pattern

The canonical pattern (used by vm0, Firecracker docs) works like this:

```
Host
│
├─ netns: mvm-<clone-0>
│  ├── tap0          (192.168.241.1/29)  ← same IP for ALL clones
│  ├── veth-ns       (10.0.0.2/24)       ← unique per clone
│  └── VM            (192.168.241.2/29)  ← frozen snapshot IP
│       │
├─ netns: mvm-<clone-1>
│  ├── tap0          (192.168.241.1/29)  ← same IP
│  ├── veth-ns       (10.0.1.2/24)       ← unique
│  └── VM            (192.168.241.2/29)  ← frozen snapshot IP
│
Host namespace:
  veth-host-0  (10.0.0.1/24)
  veth-host-1  (10.0.1.1/24)
```

**Key insight:** Every clone can use the **same frozen IP** because each is in its own network namespace. No IP conflicts. No guest-side reconfiguration. The TAP inside each namespace has the same IP (192.168.241.1/29), matching the guest's frozen IP (192.168.241.2).

A veth pair connects each namespace to the host, with NAT for outside connectivity.

---

## 3. Firecracker Jailer Integration

### 3.1 Current Spawn Path

Currently, mvmctl spawns Firecracker directly:

```go
cmd = exec.Command(firecracker, "--api-sock", socketPath, ...)
```

No jailer, no namespace.

### 3.2 Spawn via Jailer with `--netns`

The Firecracker jailer supports `--netns` to join an existing network namespace:

```bash
jailer --id <vmID> \
       --exec-file /usr/bin/firecracker \
       --uid <uid> --gid <gid> \
       --netns /var/run/netns/mvm-<vmID> \
       --daemonize \
       -- \
       --api-sock /run/firecracker.socket
```

The jailer also:
- Creates a chroot jail at `/srv/jailer/firecracker/<vmID>/root/`
- Copies the Firecracker binary into the chroot
- Sets up `/dev/net/tun` and `/dev/kvm` inside the chroot
- Drops privileges to `--uid`/`--gid`
- Manages cgroups
- Daemonizes

### 3.3 What the Jailer Does NOT Do

The jailer does **not** automate network setup. It only joins an existing namespace. The following must be done **before** spawning the jailer:

1. Create the network namespace: `ip netns add mvm-<vmID>`
2. Create TAP inside namespace: `ip netns exec mvm-<vmID> ip tuntap add name tap0 mode tap`
3. Configure TAP with snapshot IP: `ip addr add 192.168.241.1/29 dev tap0`
4. Create veth pair and connect namespace to host
5. Set up NAT rules

---

## 4. Full Lifecycle

### 4.1 VM Create / Snapshot Restore

```
mvm snapshot restore --snapshot <id> --count 3
```

For each clone:

1. **Create namespace:**
   ```bash
   ip netns add mvm-<cloneID>
   ```

2. **Create and configure TAP inside namespace:**
   ```bash
   ip netns exec mvm-<cloneID> ip tuntap add name tap0 mode tap
   ip netns exec mvm-<cloneID> ip addr add 192.168.241.1/29 dev tap0
   ip netns exec mvm-<cloneID> ip link set tap0 up
   ```

3. **Create veth pair:**
   ```bash
   ip link add veth-<cloneID> type veth peer name veth-ns-<cloneID>
   ip link set veth-ns-<cloneID> netns mvm-<cloneID>
   ip addr add 10.0.<N>.1/24 dev veth-<cloneID>
   ip netns exec mvm-<cloneID> ip addr add 10.0.<N>.2/24 dev veth-ns-<cloneID>
   ip link set veth-<cloneID> up
   ip netns exec mvm-<cloneID> ip link set veth-ns-<cloneID> up
   ```

4. **Set up NAT:**
   ```bash
   # Inside namespace: SNAT guest traffic to veth
   ip netns exec mvm-<cloneID> iptables -t nat -A POSTROUTING \
     -o veth-ns-<cloneID> -s 192.168.241.0/29 -j SNAT --to 10.0.<N>.2

   # Host: MASQUERADE namespace traffic to upstream
   iptables -t nat -A POSTROUTING -s 10.0.<N>.0/24 -o <uplink> -j MASQUERADE

   # Host: FORWARD rules
   iptables -A FORWARD -i <uplink> -o veth-<cloneID> -j ACCEPT
   iptables -A FORWARD -o <uplink> -i veth-<cloneID> -j ACCEPT
   ```

5. **Spawn jailer inside namespace:**
   ```go
   cmd = exec.Command("jailer",
       "--id", cloneID,
       "--exec-file", firecrackerPath,
       "--uid", strconv.Itoa(fcUID),
       "--gid", strconv.Itoa(fcGID),
       "--netns", fmt.Sprintf("/var/run/netns/mvm-%s", cloneID),
       "--daemonize",
       "--",
       "--api-sock", "/run/firecracker.socket",
   )
   ```

6. **Load snapshot via API:**
   - Connect to the jailer's API socket (inside chroot at `/srv/jailer/firecracker/<cloneID>/root/run/firecracker.socket`)
   - Call `PUT /snapshot/load`
   - Guest resumes with frozen IP → TAP inside namespace has matching IP → **networking works immediately**

7. **SSH access:**
   - For egress-only: via NAT through veth pair
   - For ingress: DNAT from a unique clone address to the guest IP within the namespace, or reach via the veth host endpoint

### 4.2 VM Stop / Destroy

```bash
# Remove NAT rules
iptables -D FORWARD -i <uplink> -o veth-<cloneID> -j ACCEPT
iptables -D FORWARD -o <uplink> -i veth-<cloneID> -j ACCEPT
iptables -t nat -D POSTROUTING -s 10.0.<N>.0/24 -o <uplink> -j MASQUERADE

# Delete veth pair (deletes both ends)
ip link delete veth-<cloneID>

# Delete namespace (deletes all devices inside)
ip netns delete mvm-<cloneID>

# Clean up jailer chroot
rm -rf /srv/jailer/firecracker/<cloneID>/
```

---

## 5. Integration Points in mvmctl

### 5.1 Files to Modify

| File | Change |
|------|--------|
| `internal/core/vm/firecracker.go` | New `SpawnWithJailer()` method, optional `--netns` flag |
| `internal/core/vm/firecracker.go` | `buildNetworkConfig()` → keep using `"eth0"` iface_id |
| `internal/lib/model/firecracker.go` | Add `NetNS` field to `FirecrackerConfig` |
| `internal/service/network/` | New namespace manager: create/delete netns, veth pairs, NAT rules |
| `pkg/api/vm.go` | `vmRespawnFirecracker` → conditional jailer/namespace path |
| `pkg/api/snapshot.go` | `SnapshotRestore` → set up namespace + TAP before spawn |
| `internal/cli/snapshot.go` | Potentially new `--jailer` / `--namespace` flags |
| `internal/core/vm/firecracker_test.go` | Tests for jailer spawn path |

### 5.2 mvmctl Config Additions

```yaml
firecracker:
  jailer_path: /usr/bin/jailer        # or resolved from binary repo
  jailer_uid: 1000                    # uid to drop privileges to
  jailer_gid: 1000                    # gid to drop privileges to
  jailer_chroot_base: /srv/jailer

network:
  namespace_per_vm: false             # opt-in for now
  namespace_prefix: mvm-              # prefix for netns names
```

---

## 6. Comparison: Namespaces vs Vsock Agent

| Aspect | Network Namespaces | Vsock Agent Exec |
|--------|-------------------|------------------|
| **Guest reconfiguration** | None — frozen IP matches TAP | Required — `ip addr` via agent |
| **Extra latency** | Namespace setup before spawn | Agent probe + exec after restore |
| **Infra complexity** | High — netns, veth, NAT, jailer | Low — one vsock call |
| **Security** | Higher — jailer chroot + privilege drop | Same as current (root) |
| **SSH access** | Via veth IP + NAT/port-forward | Direct bridge IP |
| **Batch clones** | Trivially parallel | Each needs agent connect |
| **Guest dependency** | None | Requires running vsock agent |
| **Changes to spawn path** | Major — jailer + namespace | None |

---

## 7. Migration Path

1. **Phase 1 (now):** Vsock agent exec — fix snapshot restore networking within current architecture.
2. **Phase 2 (future):** Add namespace-per-VM as optional feature behind config flag.
3. **Phase 3:** Make namespaces default, deprecate direct bridge mode.
4. **Phase 4:** Add jailer for chroot + privilege drop.
