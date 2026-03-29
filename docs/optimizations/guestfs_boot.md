# libguestfs Boot Time Optimizations

## Overview

This document describes the boot-time optimizations applied when using libguestfs for cloud-init injection in mvmctl. These optimizations reduce appliance startup time by configuring the backend directly, minimizing resource allocation, and disabling unnecessary services.

## Applied Optimizations

The following optimizations are implemented in `src/mvmctl/core/rootfs_injector.py`:

### 1. Direct Backend (Environment Variable)

The libguestfs appliance uses the `direct` backend (QEMU/KVM directly) instead of libvirt. This eliminates libvirt IPC overhead and dependency resolution delays.

**Implementation:**
```python
os.environ["LIBGUESTFS_BACKEND"] = "direct"
```

**Why environment variable:** The backend must be configured before `GuestFS()` instantiation. The backend reads this variable during handle initialization.

### 2. Launch Timeout (Environment Variable)

The appliance launch timeout is configured via `LIBGUESTFS_BACKEND_SETTINGS`. This replaces the deprecated `set_timeout()` handle method.

**Implementation:**
```python
os.environ["LIBGUESTFS_BACKEND_SETTINGS"] = f"timeout={DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT}"
```

**Value:** Uses `DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT` from `mvmctl.constants`.

### 3. Networking Disabled (Handle Method)

Network interface initialization is skipped, eliminating DHCP client startup and link state waits.

**Implementation:**
```python
if hasattr(g, "set_network"):
    g.set_network(False)
```

### 4. Minimal vCPUs (Handle Method)

The appliance runs with a single vCPU, reducing hardware initialization time.

**Implementation:**
```python
if hasattr(g, "set_smp"):
    g.set_smp(1)
```

### 5. Minimal Memory (Handle Method)

Memory allocation is reduced to 256MB, significantly faster than the default 500MB+ allocation.

**Implementation:**
```python
if hasattr(g, "set_memsize"):
    g.set_memsize(256)
```

## Implementation Details

### Environment Variable Scoping

Environment variables are set immediately before handle creation and restored immediately after to avoid polluting the parent process:

```python
# Save original values
orig_backend = os.environ.get("LIBGUESTFS_BACKEND")
orig_backend_settings = os.environ.get("LIBGUESTFS_BACKEND_SETTINGS")

# Set required values
os.environ["LIBGUESTFS_BACKEND"] = "direct"
os.environ["LIBGUESTFS_BACKEND_SETTINGS"] = f"timeout={DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT}"

try:
    g = guestfs.GuestFS(python_return_dict=True)
finally:
    # Restore original values
    if orig_backend is not None:
        os.environ["LIBGUESTFS_BACKEND"] = orig_backend
    elif "LIBGUESTFS_BACKEND" in os.environ:
        del os.environ["LIBGUESTFS_BACKEND"]
    # ... same for LIBGUESTFS_BACKEND_SETTINGS
```

### Compatibility Guards

All handle methods use `hasattr` guards for compatibility with older python3-guestfs bindings:

| Method | Guard | Fallback Behavior |
|--------|-------|-------------------|
| `set_network` | `hasattr(g, "set_network")` | Network initialized (slower but functional) |
| `set_smp` | `hasattr(g, "set_smp")` | Default vCPU count used |
| `set_memsize` | `hasattr(g, "set_memsize")` | Default memory allocation used |

### Configuration Priority

| Setting | Method | Applied When |
|---------|--------|--------------|
| Backend | Environment variable | Before `GuestFS()` instantiation |
| Timeout | Environment variable | Before `GuestFS()` instantiation |
| Network | Handle method | After handle creation, before `launch()` |
| vCPUs | Handle method | After handle creation, before `launch()` |
| Memory | Handle method | After handle creation, before `launch()` |

## Expected Performance

With all optimizations applied, appliance boot time for cloud-init injection is typically **< 5 seconds** on modern hardware with KVM acceleration.

## Troubleshooting

### Slow Boot Times

If injection takes significantly longer:

1. **Check KVM availability:** The direct backend requires `/dev/kvm` access
2. **Verify environment variables:** Use `LIBGUESTFS_DEBUG=1` to see backend selection
3. **Check method availability:** Some distro packages may omit certain bindings

### Compatibility Issues

If the appliance fails to launch:

1. **Fallback to libvirt:** Temporarily remove `LIBGUESTFS_BACKEND=direct` to test with libvirt
2. **Increase timeout:** Override `DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT` if needed
3. **Check method support:** Verify `python3-guestfs` package version

## References

- [libguestfs backend documentation](https://libguestfs.org/guestfs.3.html#backend)
- [libguestfs launch timeout](https://libguestfs.org/guestfs.3.html#libguestfs_backend_settings)
- `src/mvmctl/core/rootfs_injector.py` - Implementation
- `tests/unit/test_rootfs_injector.py` - Test coverage
