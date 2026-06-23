#!/usr/bin/env bash
# ─── Build script for mvmctl ─────────────────────────────────────────────────
#
# Works locally AND in CI (no dependencies beyond Go and standard tools).
# Version is auto-detected from git tags, CI environment, or explicit flag.
#
# Usage:
#   ./scripts/build.sh                  # dev build → ./mvm
#   ./scripts/build.sh release          # release build → ./dist/mvm
#   ./scripts/build.sh version          # print resolved version and exit
#
# Options:
#   --version X.Y.Z     Explicit version (overrides auto-detection)
#   --output ./path     Output path for the binary
#   --arch ARCH         Target architecture for guest agent (amd64|arm64, default: host arch)
#
# Version detection priority:
#   1. --version X.Y.Z flag
#   2. GITHUB_REF_NAME environment variable (CI — strips leading "v")
#   3. git describe --tags --dirty --always
#   4. Fallback: "0.0.0-dev"
#
# Examples:
#   ./scripts/build.sh                               # dev, debuggable
#   ./scripts/build.sh release --version 0.2.0        # tagged release
#   ./scripts/build.sh release --output ./custom/mvm  # custom output path
#   GITHUB_REF_NAME=v0.2.0 ./scripts/build.sh release # CI simulation
# =============================================================================

set -euo pipefail

# ─── Package path for ldflags ────────────────────────────────────────────────
LDFLAGS_VAR="mvmctl/internal/lib/version.BuildVersion"

# ─── Resolve version ─────────────────────────────────────────────────────────
resolve_version() {
  # Priority 1: explicit flag
  if [[ -n "${EXPLICIT_VERSION:-}" ]]; then
    echo "${EXPLICIT_VERSION}"
    return
  fi

  # Priority 2: CI environment (GITHUB_REF_NAME="v0.2.0" → "0.2.0")
  if [[ -n "${GITHUB_REF_NAME:-}" ]]; then
    echo "${GITHUB_REF_NAME#v}"
    return
  fi

  # Priority 3: local git tag
  if VERSION=$(git describe --tags --dirty --always 2>/dev/null); then
    echo "${VERSION#v}"
    return
  fi

  # Priority 4: fallback
  echo "0.0.0-dev"
}

# ─── Build guest agent (pre-compiled, zstd-compressed for embedding) ─────────
# Cross-compiles the vsock guest agent for the target architecture, then
# compresses it with zstd. The compressed binary is embedded via //go:embed,
# reducing the mvm binary size by ~60% for the agent portion.
build_agent() {
  local arch="$1"
  local agent_dir="internal/service/vsockagent"
  local agent_binary="agent-linux-${arch}"
  local agent_zst="${agent_binary}.zst"

  # Placeholder for //go:embed before building the agent binary.
  touch "${agent_dir}/${agent_zst}"

  echo "  → Building guest agent (linux/${arch})..."
  CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -a \
    -o "${agent_dir}/${agent_binary}" \
    -ldflags="-s -w -X '${LDFLAGS_VAR}=${version}'" \
    ./internal/service/vsockagent/cmd/

  # Compress for embedding — saves ~60% in embedded binary size.
  # Decompressed lazily at runtime on first AgentBinary() call.
  # zstd gives ~10-15% better compression than gzip at level 19.
  echo "  → Compressing guest agent..."
  zstd -19 -f -o "${agent_dir}/${agent_zst}" "${agent_dir}/${agent_binary}"
}

# ─── Clean up agent binaries ─────────────────────────────────────────────────
cleanup_agent() {
  local arch="$1"
  rm -f "internal/service/vsockagent/agent-linux-${arch}" \
        "internal/service/vsockagent/agent-linux-${arch}.zst"
}

# ─── Build for one architecture ──────────────────────────────────────────────
do_build_one() {
  local mode="$1"
  local version="$2"
  local output="$3"
  local goarch="$4"   # Go arch name: amd64 or arm64

  local ldflags="-X '${LDFLAGS_VAR}=${version}'"
  local buildargs=()

  if [[ "$mode" == "release" ]]; then
    # Release: stripped DWARF/symbols, path-free, PIE, pure Go net+os/user
    ldflags="-s -w ${ldflags}"
    buildargs+=("-trimpath" "-buildmode=pie")
    buildtags="netgo,osusergo"
  else
    # Dev: debuggable (keep DWARF), faster build, no PIE
    buildtags=""
  fi

  if [[ -n "${buildtags}" ]]; then
    buildargs+=("-tags" "${buildtags}")
  fi

  buildargs+=("-ldflags=${ldflags}")

  # Step 1: Build guest agent binary for the target arch (needed for //go:embed)
  build_agent "$goarch"
  trap "cleanup_agent $goarch" EXIT

  echo "==> Building mvmctl ${mode} binary (linux/${goarch})"
  echo "    version:  ${version}"
  echo "    output:   ${output}"
  echo "    ldflags:  ${ldflags}"
  if [[ -n "${buildtags}" ]]; then
    echo "    tags:     ${buildtags}"
  fi

  # Ensure output directory exists (e.g. dist/)
  mkdir -p "$(dirname "${output}")"

  GOARCH="${goarch}" CGO_ENABLED=0 go build "${buildargs[@]}" -o "${output}" ./cmd/mvm/

  echo "    done:     $(ls -lh "${output}" | awk '{print $5}')"
}

# ─── Build the binary (single arch or all) ──────────────────────────────────
do_build() {
  local mode="$1"
  local version="$2"
  local output="$3"
  local arch="$4"

  if [[ "$arch" == "all" ]]; then
    # Build for both amd64 and arm64.
    local outdir
    outdir="$(dirname "${output}")"
    local outname
    outname="$(basename "${output}")"

    do_build_one "$mode" "$version" "${outdir}/${outname}" "amd64"
    do_build_one "$mode" "$version" "${outdir}/${outname}-arm64" "arm64"
  else
    do_build_one "$mode" "$version" "$output" "$arch"
  fi
}

# ─── Print resolved version ──────────────────────────────────────────────────
do_version() {
  resolve_version
}

# =============================================================================
# Main
# =============================================================================
main() {
  local mode="release"
  local output=""
  local arch="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
  EXPLICIT_VERSION=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
    release | dev | version)
      mode="$1"
      shift
      ;;
    --version)
      if [[ -z "${2:-}" ]]; then
        echo "ERROR: --version requires a value" >&2
        exit 1
      fi
      EXPLICIT_VERSION="$2"
      shift 2
      ;;
    --output)
      if [[ -z "${2:-}" ]]; then
        echo "ERROR: --output requires a value" >&2
        exit 1
      fi
      output="$2"
      shift 2
      ;;
    --arch)
      if [[ -z "${2:-}" ]]; then
        echo "ERROR: --arch requires a value (amd64|arm64|all)" >&2
        exit 1
      fi
      arch="$2"
      shift 2
      ;;
    --help | -h)
      sed -n '2,26p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *)
      echo "ERROR: unknown argument: $1" >&2
      echo "Usage: $0 [dev|release|version] [--version X.Y.Z] [--output ./path]" >&2
      exit 1
      ;;
    esac
  done

  local version
  version="$(resolve_version)"

  if [[ -z "${output}" ]]; then
    case "$mode" in
    release) output="./dist/mvm" ;;
    dev) output="./mvm" ;;
    version) ;;
    esac
  fi

  case "$mode" in
  version)
    do_version
    ;;
  dev | release)
    do_build "$mode" "$version" "$output" "$arch"
    ;;
  esac
}

main "$@"
