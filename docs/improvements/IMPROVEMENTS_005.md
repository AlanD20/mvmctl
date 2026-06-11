# Resource grouping (batch ID)

> **STATUS: Current — not implemented (as documented).** No `group_id` field exists in `vm_instances` table.
>
> **Last verified:** 2026-06-10

**Phase:** Standalone — after `--count` feature is stable
**Complexity:** Low
**Depends on:** `--count` for `mvm vm create` ✅

## Goal

When creating N VMs with `--count`, auto-generate a `group_id` so you can manage the whole batch as a unit.

```bash
mvm vm create my-vm --count 10
# → Created 10 VM(s): my-vm, my-vm-2, ... (group: abc-def-123)

mvm vm ls --group abc-def-123
# Lists all 10 VMs

mvm vm rm --group abc-def-123
# Removes all 10 VMs
```

## What changes

**DB:** Add `group_id TEXT` column to `vm_instances`. Nullable — single creates don't get one.

**CLI:**
- `mvm vm ls --group <id>` — filter by group
- `mvm vm rm --group <id>` — remove all VMs in group
- `mvm vm create` output shows the group ID

**API:** `VMOperation.Create()` generates a UUID for each `--count` batch, passes it to each per-VM creation context.

## Why standalone

Grouping is not needed for `--count` to work. It's a convenience layer on top. Keeping it separate avoids scope creep during the initial `--count` implementation.
