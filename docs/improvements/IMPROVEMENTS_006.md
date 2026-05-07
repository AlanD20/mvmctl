# JSON output mode

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

**Minimal change** — no API or Core modifications. Pure CLI layer.

## Why standalone

Zero dependencies on other features. Can be implemented anytime. Useful for scripting even without `--count` or volumes.
