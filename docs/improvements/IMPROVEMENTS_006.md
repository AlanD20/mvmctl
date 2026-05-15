# JSON output mode

> **STATUS: Current — partially implemented.** Per-command `--json` flags exist on most `ls`/`inspect` commands (vm, image, network, kernel, bin, host, key, volume), but there is NO global `--json` flag on the root CLI group.

> ## Status: ⚠️ PARTIALLY IMPLEMENTED
>
> Per-command `--json` flags exist on:
> - `cli/vm.py` (lines 64, 528)
> - `cli/image.py` (lines 71, 371)
> - `cli/network.py` (lines 65, 283, 347)
> - `cli/kernel.py` (lines 58, 99)
> - `cli/bin.py` (line 61)
> - `cli/host.py` (line 231)
> - `cli/key.py` (lines 56, 197)
> - `cli/volume.py` (lines 103, 154)
>
> What does NOT exist is a **global** `--json` flag on the root CLI group that applies to ALL commands (including mutations like `create`, `remove`, etc.).
>
> **Last verified:** 2026-05-15

**Phase:** Standalone — orthogonal to all features
**Complexity:** Low
**Depends on:** Nothing

## Goal

A global `--json` flag that switches all CLI output to machine-readable JSON instead of formatted text.

```bash
mvm vm ls --json
# → {"vms": [{"name": "my-vm", "status": "running", ...}]}

mvm vm create my-vm --json
# → {"status": "success", "vms": ["my-vm"], "group_id": null}
```

## What changes

The API layer already returns `OperationResult` with structured data. The CLI layer currently renders this into Rich/text output. `--json` just switches the renderer.

**CLI:** A global `--json` flag on the root `app` group (Click context). Each command checks `ctx.obj["json"]` and either prints formatted output or calls `json.dumps()` on the `OperationResult`.

**Minimal change** — no API or Core modifications. Pure CLI layer. However, the per-command `--json` flags already handle `ls` and `inspect` for most domains, so the global flag would primarily benefit mutation commands (create, remove, etc.) and provide a single consistent mechanism.

## Why standalone

Zero dependencies on other features. Can be implemented anytime. Useful for scripting even without `--count` or volumes.
