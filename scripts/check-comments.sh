#!/usr/bin/env bash
# check-comments.sh — Enforces commenting standards from docs/STANDARDS.md §25
#
# Searches .go files under internal/, pkg/, cmd/ for three categories of
# banned comment patterns. Intended for local use and CI.
#
# Usage:
#   ./scripts/check-comments.sh                    # run all checks
#   ./scripts/check-comments.sh --fix              # no-op (not yet implemented)
#   ./scripts/check-comments.sh --help             # show this message
#
# Exit codes:
#   0  — all clean, no violations found
#   1  — one or more violations found, or invalid arguments
#
# Checks:
#   1. Full-width rulers        — // ==== or // ════ (ASCII/Unicode rulers)
#   2. Porting ancestry refs    — Python (whole word, not substring)
#   3. Unicode section headers  — // ── or // ══ (should use // --- instead)

set -uo pipefail

SCRIPT_NAME="$(basename "$0")"
SEARCH_DIRS="internal/ pkg/ cmd/"
GREP_ARGS=(-rnI --include='*.go')

# Colour support when stdout is a terminal
if [[ -t 1 ]]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  BOLD='\033[1m'
  NC='\033[0m' # No Colour
else
  RED=''
  GREEN=''
  YELLOW=''
  BOLD=''
  NC=''
fi

# ─--- Help ---
show_usage() {
  sed -n '2,14p' "$0" | sed 's/^# \?//'
  exit 0
}

# ─--- Run a single check ---
# Arguments: category_name grep_pattern
# Prints violations, returns 0 (clean) or 1 (violations found)
run_check() {
  local category="$1"
  local pattern="$2"
  local count=0
  local matches

  # Collect matches; grep exits 1 when nothing matches, tolerate that.
  matches=$(grep "${GREP_ARGS[@]}" "${pattern}" ${SEARCH_DIRS} 2>/dev/null || true)

  if [[ -n "${matches}" ]]; then
    count=$(echo "${matches}" | wc -l)
    echo -e "${RED}[FAIL]${NC} ${BOLD}${category}${NC} — ${count} violation(s)"
    echo "${matches}"
    echo ""
  else
    echo -e "${GREEN}[PASS]${NC} ${BOLD}${category}${NC} — clean"
  fi

  # Return 1 if violations found, 0 if clean (never return count directly —
  # non-zero values trigger set -e to abort the script).
  [[ "${count}" -gt 0 ]] && return 1
  return 0
}

# ─--- --fix no-op ---
do_fix() {
  echo "${SCRIPT_NAME}: auto-fix not yet implemented"
  echo "${SCRIPT_NAME}: see docs/STANDARDS.md §25.17 for the patterns to clean up manually"
  exit 0
}

# ═══════════════════════════════════════════════════════════════════════════════
# Main
# ═══════════════════════════════════════════════════════════════════════════════
main() {
  local fix_mode=false

  while [[ $# -gt 0 ]]; do
    case "$1" in
    --fix)
      fix_mode=true
      shift
      ;;
    --help | -h)
      show_usage
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      echo "Usage: $0 [--fix] [--help]" >&2
      exit 1
      ;;
    esac
  done

  if [[ "${fix_mode}" == true ]]; then
    do_fix
  fi

  # ─--- Header ---
  echo -e "${BOLD}check-comments.sh${NC} — Checking Go comment standards (§25)"
  echo ""

  local total_violations=0
  local exit_code=0

  # ─--- Check 1: Full-width rulers ---
  echo -e "${BOLD}1. Full-width rulers${NC} — checking for // ==== or // ════"
  run_check "full-width-rulers" '// ====\|// ════'; rc=$?
  total_violations=$((total_violations + rc))
  [[ "${rc}" -gt 0 ]] && exit_code=1

  # ─--- Check 2: Porting ancestry references ---
  # Only checks for "Python" as a whole word. After the big-bang cleanup,
  # .py file extension refs and "ported"/"porting" are no longer useful
  # signals (false positives from test fixtures and common English words).
  echo -e "${BOLD}2. Porting ancestry references${NC} — checking for Python (whole word)"
  run_check "porting-ancestry" '\bPython\b'; rc=$?
  total_violations=$((total_violations + rc))
  [[ "${rc}" -gt 0 ]] && exit_code=1

  # ─--- Check 3: Unicode section headers ---
  echo -e "${BOLD}3. Unicode section headers${NC} — checking for // ── or // ══"
  run_check "unicode-section-headers" '// ──\|// ══'; rc=$?
  total_violations=$((total_violations + rc))
  [[ "${rc}" -gt 0 ]] && exit_code=1

  # ─--- Summary ---
  echo "────────────────────────────────────────"
  if [[ "${exit_code}" -eq 0 ]]; then
    echo -e "${GREEN}All checks passed.${NC} No comment standard violations found."
  else
    echo -e "${RED}${total_violations} violation(s) found across ${total_violations} category(ies).${NC}"
    echo "See docs/STANDARDS.md §25 for the commenting standards."
  fi

  exit "${exit_code}"
}

main "$@"
