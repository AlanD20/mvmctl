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
| **Error** | `"✗ Message"` (no `Error:` title — the prefix IS the indicator) | `"✗ Start failed: vm1"` |
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

Format: `"✗ Message"` — the `✗` prefix IS the error indicator. Do NOT include `"Error:"` or `"Failed to"` in the message text.

- `"✗ Start failed: vm1"` (not `"✗ Error: Failed to start VM 'vm1'"`)
- `"✗ Host init failed: db locked"` (not `"✗ Error: Host initialization error: db locked"`)

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
| **Warning** | `"X not available — suggestion"` | `"nftables NAT not available — falling back to iptables"` |
| **exc_info** | Always `exc_info=True` when inside an `except` block | `logger.warning("Failed to do X: %s", e, exc_info=True)` |
