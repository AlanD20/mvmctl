# 0015 — Message format standard for user-facing output

**Status:** accepted

Establishes a consistent format for all user-facing messages across the CLI, eliminating six variations of verb tense, three different boolean representations, and inconsistent punctuation.

## Message type formats

| Type | Format | Example |
|------|--------|---------|
| **Progress** | Present progressive, `...` suffix | `"Creating snapshot..."` |
| **Success (entity action)** | `"✓ Action: value"` | `"✓ Started: vm1"` |
| **Success (multiple entities)** | `"✓ Action: val1, val2"` | `"✓ Created: vm1, vm2"` |
| **Success (non-entity)** | Short sentence, no period | `"✓ Host initialized (3 change(s) applied)"` |
| **Error** | `"✗ Error: Message"` (prefixed with `Error:` after the `✗` indicator) | `"✗ Error: Start failed: vm1"` |
| **Error (unexpected)** | `"⚠ Unexpected Error: Message"` (yellow, for bugs or system-level failures) | `"⚠ Unexpected Error: ConnectionError: Failed to connect to socket"` |
| **Warning** | `"! Message"` (no `Warning:` title) | `"! Binary v1.15.1 already exists"` |
| **Info** | `"  Message"` (2-space indent) | `"  No active VMs"` |
| **User cancellation** | `"  Aborted"` | `"  Aborted"` |

## Rules

### Success messages (entity actions)

Format: `"✓ Action: identifier(s)"`

- Action is a past-tense verb: `Created`, `Started`, `Stopped`, `Paused`, `Resumed`, `Removed`, `Pulled`, `Added`, `Exported`, `Imported`, `Pruned`, `Warmed`, `Downloaded`
- Identifier without quotes: `"✓ Started: vm1"` not `"✓ Started: 'vm1'"`
- Multiple identifiers comma-separated: `"✓ Created: vm1, vm2, vm3"`
- No count prefix: not `"✓ Created 3 VM(s): vm1, vm2, vm3"`

### Error messages

Format: `"✗ Error: Message"` — the `✗ Error:` prefix replaces any manual `"Error:"` text in the message. Do NOT include `"Error:"` or `"Failed to"` in the message text itself.

- `"✗ Error: Start failed: vm1"` (not `"✗ Error: Failed to start VM 'vm1'"`)
- `"✗ Error: Host init failed: db locked"` (not `"✗ Error: Host initialization error: db locked"`)

**Unexpected errors** (bugs, system-level failures, unhandled exceptions) use a separate format:
- `"⚠ Unexpected Error: {ExceptionType}: {message}"` — yellow, used for unhandled exceptions caught by the `handle_errors` decorator.

The `MVMCli.error(is_unexpected=True)` method in `src/mvmctl/utils/cli.py` controls which format is used. The CLI command error handler (`handle_errors` decorator) routes known `MVMError` subclasses to the regular error format and unexpected `Exception` instances to the unexpected format.

### Boolean values in inspect output

Use `True` / `False` (capitalized) for all inspect key-value displays. JSON output uses `true` / `false` (standard JSON booleans).

### No exclamation marks

Messages should not end with `!`. Use periods or (for success messages) nothing.

## Logger messages

| Type | Format | Example |
|------|--------|---------|
| **Progress** | `"Creating X..."` (present progressive, `...`) | `"Downloading kernel from https://..."` |
| **Completion** | `"X created"` (past tense, no period) | `"Snapshot created"` |
| **Failure** | `"Failed to X: reason"` (consistent prefix) | `"Failed to create snapshot: disk full"` |
| **Warning** | `"X not available — suggestion"` | `"Active firewall backend: nftables"` |
| **exc_info** | Always `exc_info=True` when inside an `except` block | `logger.warning("Failed to do X: %s", e, exc_info=True)` |

> **Note:** The Warning example was updated to reflect the mutual-exclusion architecture (ADR-0018). Firewall backends are now mutually exclusive — `FirewallTracker` selects exactly one backend at construction time via the `firewall_backend` setting, with no fallback between them. The original example (`"nftables NAT not available — falling back to iptables"`) was removed because fallback no longer exists.

> **Implementation Note:** The `_prettify_key()` helper in `utils/cli.py` handles snake_case→Title Case conversion with acronym normalization (e.g., `nocloud_net_port`→`Nocloud Net Port` is normalized to keep acronyms uppercase). This is used by `print_dict_tree()` and `key_value()` methods.
