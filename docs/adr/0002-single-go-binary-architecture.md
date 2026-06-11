# Single Go Binary Architecture

**Status:** accepted
**Date:** 2026-05-22
**Replaces:** ADR-0002 (Nuitka two-binaries standalone build)

The `mvm` project is distributed as a single compiled Go binary with no runtime dependencies. The binary contains both the CLI entry point and all background service subprocesses (console relay, nocloudnet server, loopmount provisioner) via subcommand dispatch (`mvm run <service>`). This replaces the previous Python Nuitka build which produced two separate binaries (`mvm` and `mvm-services`) with multidist symlink dispatch.

## Decision

| Aspect | Selection | Rationale |
|--------|-----------|-----------|
| Language | Go 1.26 | Compiled, zero runtime dependencies, fast startup, excellent concurrency primitives |
| Binary count | Single binary | One `mvm` binary contains all CLI commands and service subcommands |
| Service dispatch | Subcommand-based (`mvm run <service>`) | Services are hidden subcommands within the same binary, launched via `system.SpawnService()` |
| Distribution | Single statically-linked ELF | No shared library dependencies beyond libc; no Python interpreter needed |

## Architecture

### Entry Point (`cmd/mvm/main.go`)

```go
func main() {
    // Signal handling and context setup
    ctx := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    
    // Application initialization
    op, cleanup, err := app.Initialize(ctx)
    // ... dispatch to CLI or service subcommand
}
```

### Service Subcommands

Background services are compiled into the same binary as hidden subcommands:

| Service | Subcommand | Package |
|---------|-----------|---------|
| Console relay | `mvm run console relay` | `internal/service/console/` |
| NoCloud HTTP server | `mvm run nocloudnet serve` | `internal/service/nocloudnet/` |
| Loop-mount provisioner | `mvm run provision` | `internal/service/loopmount/` |

These are launched via `system.SpawnService()` which resolves the executable path and optionally prepends `sudo`.

## Why Go

- **Zero runtime dependencies**: A single compiled binary with no interpreter, no virtual environment, no package manager.
- **Fast startup**: Go binaries start in milliseconds vs Python's multi-second import time.
- **Type safety**: Compile-time type checking prevents entire classes of runtime errors.
- **Concurrency**: Native goroutines and channels for the parallel execution model (pool.Do, pool.Gather, pool.Seq).
- **Cross-compilation**: Easy to build for different architectures (x86_64, aarch64) from a single toolchain.
- **Static analysis**: `go vet`, `go fmt`, and the compiler enforce code quality without external tools.

## Why Not Python

The previous Python implementation used Nuitka to compile to a standalone binary, but this approach had several problems:
- **Build complexity**: Nuitka required careful `--include-module` flags for dynamic imports (passlib, jinja2, rich). Missing one caused `ModuleNotFoundError` in production.
- **Two binaries**: The main CLI (`mvm`) and service binary (`mvm-services`) with symlink dispatch added complexity.
- **Self-extraction**: `--onefile` mode extracted to `/tmp/` on first run, causing startup latency and temp directory cleanup issues.
- **Size**: ~35 MB release binary with aggressive optimization vs ~10-15 MB for a Go binary with equivalent functionality.

## Consequences

- **Single binary**: Users download and install one file. No symlinks, no multidist dispatch.
- **No build-time dependency tracking**: Go's static compilation eliminates the need for explicit module inclusion lists.
- **Service spawning**: `system.SpawnService()` launches the same binary with different subcommand arguments — no separate binary path resolution.
- **Sudoers simplicity**: The sudoers file only needs the `mvm` binary path, not a separate `mvm-services` path.
- **Build speed**: `go build` is significantly faster than Nuitka compilation (seconds vs minutes).
- **Cross-compilation**: `GOOS=linux GOARCH=amd64 go build` produces a target binary without requiring the target toolchain.

## Related Decisions

- ADR-0003: Provisioning backend mutual exclusion — the `mvm run provision` subcommand is the loop-mount entry point.
- CONTEXT.md "Provisioner Backend" — mount/umount consolidated in `mvm run provision` subcommand.
