> **⚠️ ARCHIVED — Historical document from an earlier phase.**
> The project has evolved significantly. See [CONTEXT.md](../CONTEXT.md) for current domain language,
> [docs/PROJECT_ARCHITECTURE.md](../docs/PROJECT_ARCHITECTURE.md) for the current architecture,
> and [docs/API.md](../docs/API.md) for the current API reference.
> This file is kept for historical reference only.

# Firecracker Manager — Phase 5 Requirements

## Overview

Phase 5 introduces two focused changes:

1. **Privilege management** — a group-based, sudoers drop-in approach that lets users run
   the CLI without `sudo`, with all privileged binaries and their required permissions
   declared explicitly in `constants.py` so nothing is hidden and future contributors can
   easily audit or extend the list.

2. **`host` command rename** — `host prune` and `host restore` are renamed to `host clean`
   and `host reset` respectively to better reflect their scope. `host init` is unchanged.

3. **Top-level `help` subcommand** — `mvm help` behaves identically to `mvm --help`.

As always, requirements here override any conflicting requirement in a lower phase.

---

## 1. Privilege management — group and sudoers drop-in

### Goal

Users must be able to run all CLI commands without prefixing anything with `sudo`. The
privilege elevation must happen transparently inside the CLI for the operations that
require it. The mechanism must be set up once during `host init` and never require
the user to think about it again.

### Mechanism

`host init` performs two privilege-related steps:

1. Creates a system group named after the project (e.g. `mvm`) if it does not already
   exist, and adds the current user to it.
2. Writes a sudoers drop-in file to `/etc/sudoers.d/<project-name>` granting members of
   the project group passwordless `sudo` access to the specific binaries that require
   elevated privileges.

Both of these actions require the user to run `host init` once with `sudo` (or as root).
This is the only time in the entire onboarding flow where the user needs root access.
Every subsequent CLI command works without any privilege escalation from the user.

`host reset` (see Section 2) removes both the sudoers file and the group as part of its
full rollback. `host clean` does not touch either — it only removes networking.

### The `PRIVILEGED_BINARIES` constant

All binaries that require elevated privileges must be declared in a single constant in
`constants.py`. This is the authoritative list — the sudoers file is generated from it,
the privilege-check logic reads from it, and future contributors add to it here. Nothing
is hardcoded anywhere else.

```python
# constants.py

# Binaries that require elevated privileges for network and system operations.
# This list is used to generate the sudoers drop-in file during host init.
# If a future operation requires a new privileged binary, add it here.
PRIVILEGED_BINARIES: list[str] = [
    "/usr/sbin/ip",
    "/usr/sbin/iptables",
    "/usr/sbin/iptables-restore",
    "/usr/sbin/iptables-save",
    "/usr/sbin/sysctl",
]
```

The agent implementing this must not hardcode binary paths anywhere else in the codebase.
Any subprocess call to a binary in this list must reference the path via the constant or
resolve it at runtime using `shutil.which()` and then validate it against the list.

At `host init` time, the CLI must verify that every binary in `PRIVILEGED_BINARIES` is
actually present on the host before writing the sudoers file. If any binary is missing,
fail with a clear error naming the missing binary and the package that provides it on
common distributions.

### Sudoers drop-in format

The file written to `/etc/sudoers.d/<project-name>` must:

- Be generated programmatically from `PRIVILEGED_BINARIES` at `host init` time — never
  hardcoded as a static string
- Include a comment header identifying it as managed by the project and how to remove it
- Use `NOPASSWD` for all listed binaries
- Be validated with `visudo -c -f <file>` before being written to the final location to
  prevent writing a syntactically invalid sudoers file that could lock users out

Example of the generated content (not hardcoded — generated from `PRIVILEGED_BINARIES`):

```
# Managed by mvm — do not edit manually.
# To remove: mvm host reset
%mvm ALL=(root) NOPASSWD: /usr/sbin/ip, /usr/sbin/iptables, /usr/sbin/iptables-restore, /usr/sbin/iptables-save, /usr/sbin/sysctl
```

### Group membership and session activation

After `host init` adds the user to the project group, the new group membership is not
active in the current shell session until the user logs out and back in (or runs
`newgrp <project-name>`). The CLI must handle this gracefully:

- After writing the sudoers file and adding the user to the group, print a clear notice
  explaining that a logout/login is required for group membership to take effect.
- Provide an alternative one-liner the user can run immediately to activate the group in
  the current session: `newgrp <project-name>`.
- Do not silently fail or produce confusing errors if the group membership is not yet
  active — detect this condition and print a helpful message directing the user to run
  `newgrp` or re-login.

### Detecting missing privileges at runtime

Every API function that calls a privileged binary must check, before invoking it, whether
the current process has the necessary access. The check must:

1. Verify the binary exists on the host (`shutil.which()`).
2. Verify the current user is a member of the project group OR the process is already
   running as root.
3. If either check fails, raise a typed `PrivilegeError` (defined in `exceptions.py`)
   with a message explaining what is missing and directing the user to run `host init`.

This check must be implemented once in a shared utility function in `api/host.py` (e.g.
`check_privileges(binary: str) -> None`) and called from every relevant API function —
never duplicated inline.

### `configure` onboarding flow update

`configure` is the recommended onboarding path for new users. It must be updated to
reflect the privilege model clearly. The updated flow is:

```
Step 1: Privilege setup
  — Explains that host init requires sudo once to set up group permissions.
  — Asks for confirmation, then runs: sudo mvm host init
  — Prints the logout/login notice and the newgrp alternative.
  — Tells the user to re-run configure after activating the group if they used newgrp.

Step 2: Binary download      (asset bin fetch <latest>)
Step 3: Kernel download      (asset kernel fetch)
Step 4: Image download       (asset image fetch <type>)
Step 5: SSH key setup        (key create or key add)
Step 6: Summary and first VM command
```

The `README.md` quickstart section must document this flow explicitly — what `configure`
does, why the sudo step exists, and what the user needs to do after it.

### `CONTRIBUTING.md` addition

Add a section titled "Privileged operations" that explains:

- Why the `PRIVILEGED_BINARIES` constant exists and how it is used
- How to add a new privileged binary (add to the list in `constants.py`, document why
  it needs elevated access, update tests)
- How the sudoers file is generated and validated
- How `check_privileges()` works and where to call it

---

## 2. `host` command rename

### Renamed commands

| Old name | New name | Alias | Scope |
|---|---|---|---|
| `host prune` | `host clean` | — | Removes all network config (bridges, tap devices, iptables rules). Does **not** touch sysctl or the sudoers file. |
| `host restore` | `host reset` | — | Full rollback to pre-init state. Removes network config, reverts sysctl changes, removes the sudoers drop-in file, and removes the project group. |

`host init` is unchanged.

### Updated `host` command reference

```
host init       Set up the host for running Firecracker VMs. Creates the project group,
                  adds the current user to it, writes the sudoers drop-in, enables IP
                  forwarding via sysctl, persists the sysctl change, and creates the
                  default network. Requires sudo on first run. Fully idempotent.

host clean      Remove all networking configuration added by this tool: bridge devices,
                  tap devices, and iptables rules. Does not touch sysctl settings, the
                  sudoers file, the project group, or any VM/asset cache files.
                  Refuses to run if any VM is currently running.
                  Requires confirmation unless --force is passed.

host reset      Full rollback to pre-init state. Removes everything host init configured:
                  network config (same as host clean), sysctl changes, the sudoers drop-in
                  file, and the project group. After host reset, the host is in the same
                  state it was before mvm host init was ever run.
                  Refuses to run if any VM is currently running.
                  Requires confirmation unless --force is passed.

host ls         Show current host configuration state: each setting managed by host init,
                  its current value, the original pre-init value, and whether it is in
                  the expected state. Useful for auditing drift.
```

### Backward compatibility

The old names (`host prune`, `host restore`) must be registered as hidden aliases for one
release cycle to avoid breaking users who have scripted against them. They must print a
deprecation warning directing the user to the new name, then execute the new command.
Hidden aliases do not appear in `--help` output.

---

## 3. Top-level `help` subcommand

`mvm help` must produce output identical to `mvm --help`. This extends the help
consistency rule established in phase 4 (section 5) to the top-level entry point.

```
mvm help            → identical to: mvm --help
mvm help <command>  → identical to: mvm <command> --help
```

The second form (`mvm help <command>`) must work for all command groups and subcommands:

```
mvm help vm             → mvm vm --help
mvm help vm create      → mvm vm create --help
mvm help network        → mvm network --help
mvm help key            → mvm key --help
mvm help host           → mvm host --help
mvm help asset          → mvm asset --help
mvm help asset kernel   → mvm asset kernel --help
```

This is a convenience for users who are accustomed to `man`-style help invocation
(`help <topic>`) and is a common pattern in well-regarded CLIs (git, cargo, go).

Implementation note: with Typer, this is cleanest implemented by registering `help` as a
callback command on the root app that calls `ctx.get_help()` and, when an argument is
provided, looks up the named subcommand and calls its help. With Click, the same can be
achieved via a `help` group that delegates to `ctx.invoke`. Do not implement a parallel
help-rendering system — reuse the framework's built-in help generation.

---

## 4. `constants.py` as the single source of truth for operational config

Phase 5 establishes `constants.py` as the home for any value that affects how the CLI
interacts with the host system and that a contributor might need to change. Beyond
`PRIVILEGED_BINARIES`, the following must also live in `constants.py` (consolidating any
that were previously scattered):

```python
# The system group created by host init for privilege management
PROJECT_GROUP: str = PROJECT_NAME  # e.g. "mvm"

# Path where the sudoers drop-in is written
SUDOERS_DROP_IN_PATH: str = f"/etc/sudoers.d/{PROJECT_NAME}"

# Default network created by host init
DEFAULT_NETWORK_NAME: str = "default"
DEFAULT_NETWORK_CIDR: str = "10.10.0.0/24"
DEFAULT_NETWORK_GATEWAY: str = "10.10.0.1"

# Network device name prefixes (all derived from project name)
BRIDGE_PREFIX: str = f"{PROJECT_NAME}-br"
TAP_PREFIX: str = f"{PROJECT_NAME}-tap"

# Firecracker process management
FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S: int = 5
FIRECRACKER_SIGTERM_WAIT_S: int = 1

# Privileged binaries required for host and network operations
PRIVILEGED_BINARIES: list[str] = [
    "/usr/sbin/ip",
    "/usr/sbin/iptables",
    "/usr/sbin/iptables-restore",
    "/usr/sbin/iptables-save",
    "/usr/sbin/sysctl",
]
```

All of these values are derived from `PROJECT_NAME` where applicable. `PROJECT_NAME`
itself is still the single root — everything else in `constants.py` builds on top of it.

Any value currently hardcoded elsewhere in the codebase that belongs in this category
must be migrated to `constants.py` as part of Phase 5 implementation.
