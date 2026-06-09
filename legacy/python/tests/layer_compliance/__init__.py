"""Layer compliance tests — verify CLI→API→Core architecture compliance.

These tests verify that the codebase follows the strict layering rules:
- CLI → API → Core (CLI should only import from api, models, exceptions, constants)
- API layer must call check_privileges() for privileged operations
- No hardcoded values should exist in core/api/cli (use constants.py)

These tests are designed to initially FAIL (detecting violations),
then pass after the violations are fixed.
"""
