# User-Facing Timeouts Are Connect/Probe Timeouts

**Status:** Active  
**Date:** 2026-06-25  
**Deciders:** architect + user

**Table of Contents**

- [Context](#context)
- [Decision](#decision)
- [Considered Options](#considered-options)
- [Implementation Notes](#implementation-notes)

## Context

`mvm ssh --cmd` and `mvm exec --timeout` were interpreted as **absolute command-duration limits**. A command such as

```bash
mvm ssh myvm --cmd 'mycustomcommand'
```

was killed after the configured `ssh_timeout_sec` (default 10s) even when the command was actively making progress. The same applied to `mvm exec --timeout`.

This conflicted with how users intuit `--timeout`: it should guard against an **unresponsive** target, not cap legitimate long-running work. Once a connection is established and bytes are moving, the operation should continue until completion or explicit cancellation (Ctrl-C).

## Decision

User-facing `--timeout` flags mean **connect/probe timeout only**. After the connection succeeds and the command starts, execution is unbounded.

### Affected commands

| Command | Old meaning of `--timeout` | New meaning |
|---|---|---|
| `mvm ssh --timeout` | Total budget for SSH readiness probe + command execution | SSH readiness/connect probe only |
| `mvm exec --timeout` | Total budget for agent probe + guest command execution | Vsock agent probe/connect only |

### Internal taxonomy

| Timeout type | Meaning | Example |
|---|---|---|
| **Connect/probe timeout** | Time allowed to become responsive | SSH probe, vsock agent probe |
| **Idle timeout** | Time allowed with no bytes/activity | Future optional flag; not exposed today |
| **Absolute timeout** | Hard cap on total duration | Internal use only (downloads, builds, cleanup) |
| **Graceful shutdown timeout** | Time after SIGTERM before SIGKILL | Firecracker stop, relay shutdown |
| **Service lifetime** | How long a background service runs | `mvm run nocloudnet serve --kill-after` |

### Consequences

- `mvm ssh myvm --cmd 'sleep 300'` now succeeds instead of being killed after 10s.
- `mvm exec myvm --timeout 30 -- 'long-task'` waits up to 30s for the agent, then runs `long-task` until it finishes.
- OpenSSH's existing `-o ServerAliveInterval=2 -o ServerAliveCountMax=3` still detects dead/hung SSH connections after ~6s of silence.
- The vsock agent still supports an internal absolute exec timeout (`Timeout` field) for agent upgrade/restore, but user execs pass `0`.

## Considered Options

### Option A: Keep absolute timeout, add `--connect-timeout` and `--idle-timeout` (rejected)

Would preserve backward compatibility but keeps the confusing default that `--timeout` kills active work. The user explicitly rejected this: absolute command timeouts are "genuinely useless".

### Option B: Rename `--timeout` to `--connect-timeout` (rejected)

Clearer semantics, but breaks muscle memory and existing scripts. The `--timeout` name is conventional and acceptable once documented as "time to wait for the thing to respond".

### Option C: Repurpose `--timeout` as connect/probe timeout only (selected)

Matches user expectation and common CLI conventions (e.g., `curl --connect-timeout`). Keeps the flag name; changes only the semantics of what happens after connect.

## Implementation Notes

- `internal/core/ssh/service.go`: `s.timeout` is now probe-only. `Connect` and `StreamCommand` run the actual command with `Timeout: 0`.
- `pkg/api/exec.go`: `input.Timeout` overrides `defaults.vm.vsock_probe_timeout`; `client.Exec` receives `0` for the command timeout.
- CLI help text updated to say "connect/probe timeout".
- Internal absolute timeouts (downloads, builds, graceful shutdown) are untouched.
