# JSON output mode

> **STATUS: Historical proposal — global JSON output is implemented.** The root command defines a persistent
> `--json` flag for supported finite reads and mutations. See the [command reference](../REFERENCES.md) for the
> current response envelope and exclusions. The design and illustrative output below predate that contract.
>
> **Status checked:** 2026-10-16 against `internal/cli/root.go` and VM creation result handling in `internal/cli/vm.go`.

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
