# JSON output mode

> **STATUS: 🔶 Partial — per-command `--json` flags exist on `inspect`, `ls`, `status`, and `info` commands (see `internal/cli/` for each domain). There is NO global `--json` flag on the root CLI group. Mutation commands (`create`, `remove`) lack `--json` output. The API layer already returns structured data; only the CLI renderer needs switching.
>
> **Last verified:** 2026-06-27

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

The API layer already returns structured data. The CLI layer currently renders this into formatted text output. `--json` just switches the renderer.

**CLI:** A global `--json` flag on the root command. Each command checks the flag and either prints formatted output or calls `json.MarshalIndent()` on the result.

**Minimal change** — no API or Core modifications. Pure CLI layer. However, the per-command `--json` flags already handle `inspect` for most domains, so the global flag would primarily benefit mutation commands (create, remove, etc.) and provide a single consistent mechanism.

## Why standalone

Zero dependencies on other features. Can be implemented anytime. Useful for scripting even without `--count` or volumes.
