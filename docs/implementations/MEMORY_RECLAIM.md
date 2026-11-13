# Host Memory After VM Teardown

Stopping a VM ends its Firecracker process and releases that process's memory mappings. Host memory counters
may stay high because backing files and warm images still occupy cache or tmpfs. High usage alone does not
show that guest RAM leaked.

## Guest RAM and file cache

Fresh guest RAM uses anonymous memory. Snapshot restore also maps retained snapshot memory files; modified
private pages become anonymous memory. Process exit releases those mappings, while the backing files may
remain cached. A balloon device is not needed for teardown; mvmctl does not configure one.

Clean file-cache pages are reclaimable under pressure, including while their files still exist. Dirty pages
may need writeback first. Removing a VM does not delete independent snapshots, images, kernels or volumes,
and the CLI may explicitly retain caller-owned staging files. Removing a file also does not guarantee immediate
reclamation if another process still holds it open.

See [block caching](BLOCK_CACHING.md) for guest flush and host durability behavior.

## Warm images

The default warm pool is `/tmp/mvmctl-<uid>/ready`. It consumes tmpfs memory only when its filesystem is tmpfs;
`/tmp` is not necessarily memory-backed. `MVM_TEMP_DIR` changes the temporary location, and
`MVM_WARM_POOL=disk` selects the image cache's `ready` directory.

Review `mvm cache prune misc --dry-run` before pruning miscellaneous cache. That operation also covers the
selected GuestFS appliance cache. See [warm-image storage](../optimizations/fast-durable-image-copy.md).

## Cgroup accounting

A VM's root-controlled leaf is `/sys/fs/cgroup/mvmctl/<full-vm-id>`. The approved
[pressure-only policy](../adr/0019-pressure-only-vm-memory-control.md) sets `memory.high` to guest RAM plus
configured VMM headroom and `memory.max` to `max`. Swap, CPU and PID controls remain explicit. This permits
memory use above the pressure threshold; ancestor limits and host OOM still apply.

Cleanup removes an empty, verified cgroup leaf after process termination. Surviving file-cache charges can move
to an ancestor. Teardown does not force host-wide cache eviction or write `drop_caches`.

## Diagnose the actual consumer

Start with `mvm vm inspect VM --json` for authenticated process and cgroup observations. An administrator can
also read the running VM's exact `memory.stat`, `memory.current` and `memory.events` files. Split anonymous,
file-backed and shared-memory use rather than treating total usage as guest RAM.

```bash
grep -E 'MemAvailable|AnonPages|Cached|Shmem|SwapFree' /proc/meminfo
```

Host-wide counters include unrelated workloads and may change during a stop. They cannot prove one process's
cleanup on their own. Cached pages may remain available for reuse; tmpfs files and live anonymous pages need
their own accounting. If a VMM is killed, retain its process identity, leaf and ancestor OOM counters, kernel logs
and guest evidence before recovery. Bounded memory QA does not establish the cause of an older incident.
