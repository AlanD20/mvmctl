# Loopmount over Guestfs as Default Provisioning Backend

**Status:** superseded by ADR-0010

Loop-mount provisioning (via `mvm-provision` binary using `losetup` + `mount` + chroot commands) is the default rootfs provisioning backend. Guestfs (libguestfs Python bindings) was originally designed as a fallback when the loop-mount binary is not available. Loopmount creates VMs in ~200ms vs guestfs ~2600ms for the same operation. Guestfs is disabled by default (`guestfs_enabled = False` in `constants.py`).

The decision to prefer loopmount over guestfs is still active. However, the original ADR incorrectly described the relationship between the two backends as a "fallback" when in fact the code implements mutual exclusion via the `guestfs_enabled` toggle. See ADR-0006 (loopmount-guestfs-mutual-exclusion) for the corrected architecture rules.
