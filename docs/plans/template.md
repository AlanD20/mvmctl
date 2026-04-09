# Planning Template

## Summary
[One-paragraph description of the feature/change being implemented]

**Units**: [Specify any units used and their definitions for consistency]

---

## Background

### Problem
[Clear description of the current problem or limitation]

### Solution
[High-level description of the proposed solution]

---

## Goals

1. [Specific, measurable goal 1]
2. [Specific, measurable goal 2]
3. [Specific, measurable goal 3]
4. **HARD REQUIREMENT**: [Any non-negotiable requirements]
5. **NO FALLBACKS**: [Any requirements that must fail rather than degrade]

---

## Layer Violation Guards (CRITICAL)

**These are hard constraints. Any implementation violating these will be rejected.**

| Layer | CAN | CANNOT |
|-------|-----|--------|
| **CLI** | Parse args, resolve constants-backed defaults (vcpus, mem), pass `None` for DB-backed values | Query database, resolve image/kernel paths, add default values in function params |
| **API** | Orchestrate core functions, query DB via MVMDatabase, enforce hard requirements | Add default values in function params, bypass DB for defaults, pass `None` to Core for required params |
| **Core** | Process images, calculate values, return explicit results | Import MVMDatabase, query DB, have default parameter values for operational params |
| **DB/Models** | Store data, enforce NOT NULL | Add business logic, resolve defaults |

### Anti-Patterns to Avoid

**WRONG - [Pattern Name]:**
```python
# [file] - NEVER DO THIS
# [Example of wrong implementation with ❌ markers]
```

**CORRECT - [Pattern Name]:**
```python
# [file] - CORRECT
# [Example of correct implementation with ✅ markers]
```

### Verification Checklist for Implementation

- [ ] **CLI layer**: No imports from `mvmctl.core.mvm_db` or any DB-querying modules
- [ ] **CLI layer**: No `typer.Option(DEFAULT_*)` patterns - all use `None` default
- [ ] **API layer**: No default parameter values for DB-backed or operationally significant params
- [ ] **API layer**: All DB queries go through `MVMDatabase()`, never bypassed
- [ ] **Core layer**: No imports from `mvmctl.core.mvm_db` (except `mvm_db.py` itself)
- [ ] **Core layer**: All function params receive explicit values, no `Optional[T]` for required params
- [ ] **Core layer**: Returns calculated values, never stores to DB directly

---

## Architecture Flow

```
CLI: [command example]
  ↓
API: [api/file.py]
  - [Step 1]
  - [Step 2]
  ↓
Core: [core/file.py]
  - [Step 1]
  - [Step 2]
  - [Calculation/Processing]
  - Returns [Result] with calculated value
  ↓
DB: Stores [field] as [REQUIREMENT LEVEL]
```

**Layer Responsibilities**:
- **CLI**: Parse args, pass to API
- **API**: Orchestrate core functions, query/store DB, enforce hard requirements
- **Core**: Process images, calculate values, NO DB access
- **DB**: Store data, enforce constraints

---

## Detailed Design

### 1. Database Schema Change

**File**: `src/mvmctl/db/migrations/[XXX]_migration_name.sql`

Add [description]:
```sql
[SQL changes]
```

**Note**: [Any constraints, requirements, or rationale]

### 2. Configuration

**File**: `src/mvmctl/assets/_defaults.py`

Add [description]:
```python
DEFAULTS = {
    ...
    "[section]": {
        ...
        "[config_key]": [value],  # [Description/comment]
    },
}
```

**File**: `src/mvmctl/constants.py`

Add constant:
```python
CONST_[NAME]: Final[int] = _require_int(("[section]", "[config_key]"))
```

### 3. [Feature] API Function

**File**: `src/mvmctl/api/[file].py`

#### 3.1 Create [function_name]() in API Layer

The API layer orchestrates core functions. Create `[function_name]()` that calls core functions:

```python
def [function_name](
    [param]: [type],
    [flag]: bool = False,
) -> [ReturnType]:
    """[Docstring description].
    
    This is an API layer function that orchestrates core/ layer operations.
    It calls core/[file].py functions to perform the actual work.
    
    Args:
        [param]: [Description]
        [flag]: [Description]
        
    Returns:
        [ReturnType] with [description]
    """
    from mvmctl.core.[file] import [core_function_1], [core_function_2]
    
    if [condition]:
        # [Logic for condition]
        return [ResultType](
            [field]=[value],
            ...
        )
    
    # Normal flow - call core function
    [result] = [core_function]([param])
    
    return [ResultType](
        [field]=[value],
        ...
    )
```

**Key Point**: This is an API layer function that orchestrates core layer operations. It does NOT access the database directly - it returns the calculated value, and the caller stores it.

#### 3.2 Update [Result Dataclass]

```python
@dataclass
class [ResultType]:
    [field]: [type]
    ...
    [new_field]: [type] | None = None  # [Description]
```

### 4. Update [Process] to Use [Feature]

**Architecture**: The API layer's `[function_name]()` orchestrates core functions. [Process] in core/[file].py calls core functions, then the calculated `[field]` is passed back through `[ResultType]` to the API layer for storage.

**File**: `src/mvmctl/core/[file].py`

[Process description] returns `[ResultType]` with `[field]` populated:

```python
# In [process] (core/[file].py)
# Call core [operations] directly
[result] = [core_function]([param])

# Calculate [field] based on [conditions]
if [condition]:
    [field] = [calculation_logic]
else:
    [field] = [alternative_logic]

return [ResultType](
    [field]=[value],
    [new_field]=[field],  # NEW: calculated value
    ...
)
```

**Note**: Core layer calculates the value but does NOT store it. The API layer receives `[ResultType]` and stores `[field]` to database via `[storage_function]`.

### 4.5 [ResultType] Return Points Enumeration

All [N] locations in `core/[file].py` where `[ResultType]` is instantiated must be updated to include `[field]`:

**Lines [X, Y, Z]** ([function]):
- Line [X]: [Description of return point]
- Line [Y]: [Description of return point]
- Line [Z]: [Description of return point]

**Update Pattern**: Each return point must calculate `[field]` based on:
- [Condition 1]
- [Condition 2]
- [Logic for calculation]

### 5. Remove/Update [Legacy Feature]

**File**: `src/mvmctl/assets/[file].yaml`

[Action to take on legacy configuration]

**File**: `src/mvmctl/models/[file].py`

Update `[Model]` - [action]:
```python
@dataclass
class [Model]:
    [field]: [type]
    # [REMOVED/CHANGED]: [field] - [reason]
    ...
```

### 6. Database Registration

**File**: `src/mvmctl/api/[file].py:[storage_function]()`

Store calculated `[field]` in database:

```python
def [storage_function](result: [ResultType], spec: [SpecType]) -> str:
    ...
    fields = {
        ...
        "[field]": result.[field],  # NEW
    }
    [update_function](cache_dir, full_id, **fields)
```

### 7. Handle [Special Case]

**File**: `src/mvmctl/core/[file].py`

When `[condition]`:
- [Behavior 1]
- [Behavior 2]
- [Behavior 3]

**Example**: [Concrete example with values]

**Rationale**: [Explanation of why this behavior]

### 8. Remove [Flag/Feature]

**File**: `src/mvmctl/cli/[file].py`

**Decision**: **[ACTION]**

**Investigation Result**: [Why this is being removed/changed]

**Actions**:
1. [Action 1]
2. [Action 2]
3. [Action 3]

### 9. Update DB Model

**File**: `src/mvmctl/db/models.py`

Add field to `[Model]` dataclass:
```python
@dataclass
class [Model]:
    [field]: [type]
    ...
    [new_field]: [type] | None = None  # NEW
```

**File**: `src/mvmctl/models/[file].py:[ItemClass]`

Add field:
```python
@dataclass
class [ItemClass]:
    ...
    [new_field]: [type] | None = None  # NEW
    ...
```

### 10. [Feature] Validation Update

**File**: `src/mvmctl/api/[file].py`

Update `[function]()` to read `[field]` from database and validate:

```python
def [function](input: [InputType], ...) -> [ReturnType]:
    ...
    # Get [resource] from database
    db = MVMDatabase()
    [entry] = db.[get_function]([id])
    
    # HARD REQUIREMENT: [field] must exist
    if [entry].[field] is None:
        raise [ErrorType](
            f"[Error message]"
        )
    
    [local_var] = [entry].[field]
    
    if [condition]:
        [validation_logic]
        if [failure_condition]:
            raise [ErrorType](
                f"[Error message]"
            )
        [success_action]
```

**Key Points**:
- `[field]` is a **hard requirement** - [action] fails if missing
- No fallbacks, no defaults - the value MUST be [calculated/provided] during [process]
- [Validation description]

### 11. Update [Model] Methods

**File**: `src/mvmctl/models/[file].py`

Update `[from_method]()` and `[to_method]()` to handle new field.

---

## Files to Modify

### Database & Models
- `src/mvmctl/db/migrations/[XXX]_migration_name.sql` - [Change description]
- `src/mvmctl/db/models.py` - [Change description]
- `src/mvmctl/models/[file].py` - [Change description]

### Configuration
- `src/mvmctl/assets/_defaults.py` - [Change description]
- `src/mvmctl/constants.py` - [Change description]
- `src/mvmctl/assets/[file].yaml` - [Change description]

### API Layer
- `src/mvmctl/api/[file].py` - [Change description]
- `src/mvmctl/api/[file].py` - [Change description]

### CLI
- `src/mvmctl/cli/[file].py` - [Change description]

---

## Test Plan

### Test Strategy (Behavioral Testing)

**Focus**: Behavior verification, not implementation details. Tests should verify WHAT the system does, not HOW it does it.

#### 1. [Test Category] (Unit Tests)

**Verify**:
- [Behavior 1]
- [Behavior 2]
- [Behavior 3]

**Test approach**: [How to test - mock, direct call, etc.]

#### 2. [Test Category] (Unit Tests)

**Verify**:
- [Behavior 1]
- [Behavior 2]

**Test approach**: [How to test]

#### 3. [Test Category] (Integration Tests)

**Verify**:
- [Behavior 1]
- [Behavior 2]
- [Behavior 3]

**Test approach**: [How to test - use test DB, call API, etc.]

#### 4. [Test Category] (Integration Tests)

**Verify**:
- [Behavior 1]
- [Behavior 2]
- [Behavior 3]

**Test approach**: [How to test]

#### 5. CLI Behavior (System Tests)

**Verify**:
- `[command]` → [expected result]
- `[command]` → [expected result]
- `[command]` → [expected result]

**Test approach**: Use CLI test runner (e.g., Typer's CliRunner) or bash scripts.

#### 6. [Test Category] (Regression Tests)

**Verify**:
- [Regression check 1]

**Test approach**: [How to test]

### What NOT to Test

- ❌ Exact mock paths or internal function call sequences
- ❌ Exact error message strings (may change)
- ❌ Internal calculation implementation details
- ❌ File system operations (assume core functions work)

### Testing Considerations

1. [Test consideration 1]
2. [Test consideration 2]
3. [Test consideration 3]
4. [Test consideration 4]
5. [Test consideration 5]
6. [Test consideration 6]

---

## Future Extensions (Not in Scope)

1. [Future idea 1]
2. [Future idea 2]
3. [Future idea 3]

---

## Success Criteria

- [ ] [Criterion 1]
- [ ] [Criterion 2]
- [ ] [Criterion 3]
- [ ] [Criterion 4]
- [ ] [Criterion 5]
- [ ] [Criterion 6]
- [ ] [Criterion 7]
- [ ] [Criterion 8]
