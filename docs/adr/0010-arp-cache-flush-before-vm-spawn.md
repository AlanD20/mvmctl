# 0010 — ARP cache flush before VM spawn: stale entries inflate SSH timing

The project flushes the bridge ARP cache (`ip neigh flush dev <bridge>`) after creating the TAP device and before spawning Firecracker. Without this flush, a new VM that reuses an IP address from a recently-removed VM inherits the host's stale ARP entry pointing to the old (now-dead) TAP. SSH connections route to the dead interface, hit TCP SYN timeouts (~3s), and retry — inflating perceived boot time by 5-10s.

## Context

The bridge network allocates IPs from a small subnet (172.31.99.0/24). When VMs are rapidly created and destroyed (e.g. during benchmarks or CI), a new VM frequently receives the same IP as its predecessor. The host's ARP cache still maps that IP to the old TAP's MAC. Since the old TAP no longer exists, TCP SYN packets to port 22 time out. The kernel eventually sends a new ARP request after the timeout, but this adds 3-8s to every SSH connection attempt — indistinguishable from a slow boot to monitoring tools.

Early measurements attributed this delay to boot-time services (pollinate, snapd, etc.) when it was purely a network-layer artifact.

## Decision

Flush the bridge's ARP cache (`ip neigh flush dev <bridge>`) after the TAP device is created and attached, just before spawning the Firecracker process. This forces the host to ARP for the VM's MAC when the first packet is sent — which succeeds immediately because the new TAP is already up and the VM's kernel brings up the virtio-net device within ~500ms of boot.

## Implementation

The flush is a single `run_cmd` call in `NetworkService.flush_arp()`:

```python
def flush_arp(self, bridge: str) -> None:
    """Flush ARP cache entries for the bridge.
    
    Without this, a new VM reusing an old IP will experience TCP timeouts
    from stale ARP entries pointing to a deleted TAP device.
    """
    run_cmd(["ip", "neigh", "flush", "dev", bridge], privileged=True)
```

Called from `vm_operations.py` at the end of the `network_setup` phase, after the TAP is attached to the bridge.

## Considered alternatives

1. **Assign unique IPs per VM** — The lease system already tracks IPs, but reusing freed IPs is normal and desirable for subnet efficiency. Avoiding reuse would require a monotonically increasing allocation or exhausting the subnet, neither of which scales.

2. **Gratuitous ARP from the VM** — Firecracker doesn't send GARP on boot. Adding it would require a first-boot service or kernel modification — far more complex than a host-side cache flush.

3. **Do nothing** — The 5-10s delay only affects the first SSH connection. Subsequent connections work fine once ARP is resolved. But this makes benchmarks unreliable and frustrates interactive use.
