# Test Coverage Baseline - Violating Files

Generated: 2026-03-25
Purpose: Document current test coverage for architecture-violating files

## Summary

| File | Lines | Missing | Branch Miss | Coverage |
|------|-------|---------|-------------|----------|
| `src/mvmctl/cli/asset.py` | 493 | 122 | 27 | 74% |
| `src/mvmctl/cli/vm.py` | 278 | 80 | 18 | 67% |
| `src/mvmctl/cli/configure.py` | 243 | 110 | 56 | 53% |
| `src/mvmctl/core/kernel.py` | 481 | 55 | 29 | 87% |

## Detailed Analysis

### src/mvmctl/cli/asset.py (74% coverage)

**Violation:** Imports `mvmctl.core.metadata` directly (bypasses api/)

**Untested Lines (122 statements missing):**
- Lines 68-69, 75-76, 82-83: Early setup/validation paths
- Lines 105, 107: Option parsing paths
- Lines 145-156: Image list/display logic
- Lines 217-219, 231-232: Kernel listing paths
- Lines 252, 258-259, 262-265: Default kernel setting
- Lines 347, 367: Error handling paths
- Lines 400-411: Image detail display
- Lines 423-426, 467-486, 489-490: Various subcommand paths
- Lines 504-505, 540-541: Binary version listing
- Lines 557-568, 571, 592: Binary management paths
- Lines 596-601, 610: Configuration setting paths
- Lines 631-634, 656-666: Import/export paths
- Lines 718-723, 746: Network config paths
- Lines 776-841: Complex validation/export logic
- Lines 862-873: Cleanup paths

**Test Files:** `tests/unit/test_cli_asset.py`

---

### src/mvmctl/cli/vm.py (67% coverage)

**Violation:** May have direct core imports (needs verification)

**Untested Lines (80 statements missing):**
- Lines 45-46, 55-56, 66-67: CLI option parsing
- Line 83, 89-91: Early validation
- Lines 176-210: VM creation paths
- Lines 223-269: VM configuration logic
- Lines 328, 330: State management paths
- Lines 340-349, 360-364: Network configuration
- Lines 373-376, 389-390, 393: Resource limits
- Lines 455-465, 470-473: VM removal paths
- Lines 477-489, 505-513: Batch operations
- Lines 562, 625-635: Various subcommands

**Test Files:** `tests/unit/test_cli_vm.py`

---

### src/mvmctl/cli/configure.py (53% coverage)

**Violation:** Imports `mvmctl.core.config_state` directly (bypasses api/)

**Untested Lines (110 statements missing):**
- Lines 39-40, 48-49, 57-58: Configuration initialization
- Lines 76-79, 93-94: User prompts
- Lines 102-105: Configuration validation
- Lines 132-157: Interactive wizard steps
- Lines 169-180, 195-210: Network configuration paths
- Lines 224-268: Advanced configuration
- Lines 284-312: Host setup paths
- Lines 327-328: Finalization

**Test Files:** `tests/unit/test_cli_configure.py`

---

### src/mvmctl/core/kernel.py (87% coverage)

**Violation:** Calls `console.print` / `print_warning` directly (CLI-layer output in core)

**Untested Lines (55 statements missing):**
- Lines 55-56, 59, 64: Error display paths
- Lines 85-86, 95-98: Validation errors
- Lines 105, 111-117: Download progress display
- Lines 124-125: Archive extraction display
- Lines 149, 203, 205-215, 220-223: Various error/warning paths
- Lines 356, 430: State display paths
- Lines 457-491: Complex validation paths
- Lines 580, 585-586, 620-622: Cleanup paths
- Lines 676-681, 703-704: Installation paths
- Lines 716-723, 718-720: Configuration display
- Lines 760, 793, 903-905, 912: Various subcommand logic
- Lines 939-946, 975-976: Cleanup/finalization

**Test Files:** `tests/unit/test_kernel.py`, `tests/unit/test_kernel_new.py`

---

## Key Findings

1. **Lowest Coverage:** `cli/configure.py` at 53% - Most critical for testing
2. **Missing Tests for Violations:**
   - No tests specifically target the violation paths (direct core imports)
   - The 26% uncovered lines in `cli/asset.py` include paths that call core.metadata directly
   - The 47% uncovered lines in `cli/configure.py` include paths that call core.config_state directly
3. **core/kernel.py** has 13% uncovered lines, many of which are `console.print`/`print_warning` calls

## Recommendations

1. Add targeted tests for violation paths in `cli/asset.py`:
   - Lines 145-156: Direct metadata access for image listing
   - Lines 262-265: Direct kernel config state access
2. Add tests for `cli/configure.py` to cover the wizard paths
3. Add tests for `cli/vm.py` to improve coverage from 67%
4. Consider mocking `console.print` calls in `core/kernel.py` tests to improve isolation
