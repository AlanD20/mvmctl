#!/usr/bin/env python3
"""Boot-time benchmark for mvmctl VM images.

Measures wall-clock time from `vm create` to VM readiness for each image,
saves results to benchmarks/results.json, and shows a comparison against the
previous run so you can see if changes improved or regressed boot times.

Usage:
    python benchmarks/boot_time.py                           # run all images
    python benchmarks/boot_time.py --tag "after-fix"        # tag this run
    python benchmarks/boot_time.py --image ubuntu-24.04     # single image
    python benchmarks/boot_time.py --compare                # show history only
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

# Enable internal timing instrumentation (MVM_TIMING_ENABLED env var)
# so every `mvm` subprocess records per-phase timing to ~/.cache/mvmctl/timing.log.
_TIMING_ENV = os.environ | {"MVM_TIMING_ENABLED": "1"}

# ---------------------------------------------------------------------------
# Config — add / remove / adjust images and thresholds here
# ---------------------------------------------------------------------------


@dataclass
class ImageBenchConfig:
    """Configuration for a single image benchmark."""

    name: str  # Image slug as recognised by `mvm image ls -r`
    threshold_s: int  # Maximum acceptable seconds from create → ready
    kernel: str | None = (
        None  # Kernel ID or ``type:version`` (e.g. ``official:6.19.9``).  None = default kernel.
    )


IMAGES: list[ImageBenchConfig] = [
    ImageBenchConfig(name="alpine", threshold_s=6),
    ImageBenchConfig(name="ubuntu:24.04", threshold_s=6),
    ImageBenchConfig(name="ubuntu-minimal:24.04", threshold_s=6),
    ImageBenchConfig(name="archlinux", threshold_s=6),
    ImageBenchConfig(name="debian:12", threshold_s=6),
    ImageBenchConfig(name="firecracker:v1.15", threshold_s=6),
]

# Wall-clock timeout for the probe subprocess (seconds).  Lower = more
# granular threshold checks (more attempts within the kill window).
PROBE_TIMEOUT = 5

# Hard ceiling — shouldn't be reached since threshold_s is the real abort
# point.  This is just a safety net.
POLL_TIMEOUT = 30

# Where results are stored.
HERE = Path(__file__).resolve().parent
RESULTS_FILE = HERE / "results.json"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_MVM_PATH = os.path.expanduser("~/.local/bin/mvm")
MVM: list[str] = [_MVM_PATH]


def _mvm(*args: str, timeout: int = 120) -> subprocess.CompletedProcess:
    """Run an mvmctl CLI command and return the result."""
    return subprocess.run(
        [*MVM, *args],
        capture_output=True,
        text=True,
        timeout=timeout,
        env=_TIMING_ENV,
    )


# ---------------------------------------------------------------------------
# Results persistence
# ---------------------------------------------------------------------------


def load_history() -> list[dict]:
    """Load previous benchmark runs from results.json."""
    if RESULTS_FILE.exists():
        try:
            return json.loads(RESULTS_FILE.read_text())
        except (json.JSONDecodeError, OSError):
            return []
    return []


def save_run(run: dict) -> None:
    """Append a single run record to results.json."""
    history = load_history()
    history.append(run)
    RESULTS_FILE.write_text(json.dumps(history, indent=2) + "\n")


# ---------------------------------------------------------------------------
# Single-image benchmark
# ---------------------------------------------------------------------------


def bench_image(
    cfg: ImageBenchConfig, *, kernel_id: str | None = None, skip_deblob: bool = False
) -> dict:
    """
    Benchmark a single image.  The threshold_s also serves as the max-wait:
    if the VM isn't ready within that time the image is aborted immediately.

    Args:
        cfg: Image configuration (may include per-image kernel override).
        kernel_id: Global kernel fallback — used only when cfg.kernel is None.

    Returns a dict with keys:
        name, threshold_s, create_s, total_s, passed, error, attempt, kernel
    """
    effective_kernel = cfg.kernel or kernel_id
    unique = f"bm-{cfg.name.replace('.', '-').replace('_', '-').replace(':', '-')}-{int(time.time())}"
    key_name = f"{unique}-key"
    vm_name = f"{unique}-vm"

    result: dict = {
        "name": cfg.name,
        "threshold_s": cfg.threshold_s,
        "create_s": None,
        "total_s": None,
        "passed": False,
        "error": None,
        "kernel": effective_kernel or "default",
    }

    # -- key creation -------------------------------------------------------
    r = _mvm("key", "create", key_name, "--algorithm", "ed25519", "--force")
    if r.returncode != 0:
        err_msg = (r.stderr or r.stdout or "").strip()[:300]
        result["error"] = f"Key creation: {err_msg}"
        return result

    try:
        # -- VM creation ----------------------------------------------------
        t0 = time.monotonic()
        cmd = [
            "vm",
            "create",
            vm_name,
            "--image",
            cfg.name,
            "--ssh-key",
            key_name,
        ]
        if effective_kernel:
            cmd += ["--kernel", effective_kernel]
        if skip_deblob:
            cmd.append("--skip-deblob")
        r = _mvm(*cmd)
        t1 = time.monotonic()
        result["create_s"] = round(t1 - t0, 1)

        if r.returncode != 0:
            err_msg = (r.stderr or r.stdout or "").strip()[:300]
            result["error"] = f"VM creation: {err_msg}"
            return result

        # -- poll for VM readiness -----------------------------------------
        ready = False
        deadline = time.monotonic() + POLL_TIMEOUT
        attempt = 0

        while time.monotonic() < deadline:
            attempt += 1

            # Abort if we've exceeded the threshold — the user wants a
            # hard cut-off, not a poll-until-give-up.
            elapsed = time.monotonic() - t0
            if elapsed > cfg.threshold_s:
                result["total_s"] = round(elapsed, 1)
                result["error"] = (
                    f"VM not ready after {result['total_s']}s "
                    f"(threshold {cfg.threshold_s}s)"
                )
                return result

            try:
                r = _mvm(
                    "vm", "exec", vm_name,
                    "--timeout", str(PROBE_TIMEOUT),
                    "--", "echo", "ok",
                    timeout=PROBE_TIMEOUT + 5,
                )
                if r.returncode == 0:
                    t2 = time.monotonic()
                    result["total_s"] = round(t2 - t0, 1)
                    result["attempt"] = attempt
                    ready = True
                    break
            except subprocess.TimeoutExpired:
                pass  # retry
            time.sleep(1)

        if not ready:
            result["error"] = f"VM not ready after {POLL_TIMEOUT}s polling"
            return result

        result["passed"] = result["total_s"] <= cfg.threshold_s

    finally:
        # -- cleanup --------------------------------------------------------
        for _ in range(3):
            _mvm("vm", "rm", vm_name, "--force", timeout=30)
            r = _mvm("key", "rm", key_name, "--force", timeout=15)
            if r.returncode == 0:
                break

    return result


# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------


def _cell(val, width: int) -> str:
    return str(val).rjust(width)


def _delta(prev, curr) -> str:
    """Return a formatted delta string: better / worse / same / N/A."""
    if prev is None or curr is None:
        return "  N/A  "
    diff = round(curr - prev, 1)
    if diff == 0:
        return "  0.0s  "
    if diff < 0:
        return f" \u2193{-diff:.1f}s "  # ↓ = faster (better)
    return f" \u2191{diff:.1f}s "  # ↑ = slower (worse)


def _find_previous(history: list[dict], name: str) -> dict | None:
    """Find the most recent run for a given image name."""
    for run in reversed(history):
        for r in run.get("results", []):
            if r["name"] == name and r.get("total_s") is not None:
                return r
    return None


def print_current_results(results: list[dict], prev_run: dict | None) -> None:
    """Pretty-print benchmark results with comparison to previous run."""
    header = f"  {'Image':<28s} {'Create':>7s} {'→Ready':>7s} {'Result':>8s}"
    if prev_run:
        header += f"  {'Prev':>7s} {'Δ':>8s}"
    print()
    print("  " + "─" * (len(header) - 2))
    print(header)
    print("  " + "─" * (len(header) - 2))

    # Build a lookup of previous totals
    prev_totals: dict[str, float] = {}
    if prev_run:
        for r in prev_run.get("results", []):
            if r.get("total_s") is not None:
                prev_totals[r["name"]] = r["total_s"]

    passed = 0
    failed = 0

    for r in results:
        create = f"{r['create_s']}s" if r["create_s"] is not None else " ERR"
        total = f"{r['total_s']}s" if r["total_s"] is not None else " N/A"

        if r["passed"]:
            status = "  ✅ PASS"
            passed += 1
        else:
            status = "  ❌ FAIL"
            failed += 1

        line = f"  {r['name']:<28s} {create:>7s} {total:>7s}  {status:>8s}"

        if prev_run:
            prev_total = prev_totals.get(r["name"])
            prev_fmt = f"{prev_total}s" if prev_total is not None else "  N/A  "
            d = _delta(prev_total, r.get("total_s"))
            line += f"  {prev_fmt:>7s}  {d:>8s}"

        print(line)

        if r["error"]:
            print(f"  {'':>28s}  └─ {r['error']}")

    print("  " + "─" * (len(header) - 2))
    print(
        f"\n  ✅ {passed} passed"
        + (f", ❌ {failed} failed" if failed else "")
        + f"  |  {len(results)} total"
    )
    if failed:
        print("  TIP: Adjust `threshold_s` in IMAGES if limits are too tight.")
    print()


def show_history() -> None:
    """Display an ASCII comparison table across all benchmark runs.

    Each column is a tagged run (chronologically), each row is an image.
    Cells show the time-to-ready for that image in that run.
    """
    history = load_history()
    if not history:
        print("No previous results found in benchmarks/results.json")
        return

    if len(history) == 1:
        run = history[0]
        tag = run.get("tag", "run-0")
        ts = run.get("timestamp", "?")
        total = len(run.get("results", []))
        passed = sum(1 for r in run["results"] if r["passed"])
        print(f"\n  Single run: {tag:30s}  {ts:20s}  {passed}/{total} passed\n")
        print_current_results(run["results"], None)
        return

    # Collect all unique image names in order of first appearance
    seen: dict[str, int] = {}
    images_in_order: list[str] = []
    for run in history:
        for r in run.get("results", []):
            name = r["name"]
            if name not in seen:
                seen[name] = len(images_in_order)
                images_in_order.append(name)

    # Build tag→results lookup
    tag_names: list[str] = []
    run_lookup: list[dict[str, dict]] = []
    errors_by_image: dict[str, str] = {}
    for run in history:
        tag_names.append(run.get("tag", "?"))
        by_name: dict[str, dict] = {}
        for r in run.get("results", []):
            by_name[r["name"]] = r
            if r.get("error"):
                errors_by_image[r["name"]] = r["error"]
        run_lookup.append(by_name)

    total_cols = len(tag_names) + 1  # runs + one overall Δ

    # Column sizing
    name_w = max(len(n) for n in images_in_order + ["Image"])
    data_w = 15  # enough for "  11.1s (P)  " or "   FAIL (F)  "

    # Build separator line
    def sep_line(left: str, mid: str, right: str) -> str:
        line = left + "─" * (name_w + 2)
        for _ in range(total_cols):
            line += mid + "─" * (data_w + 2)
        return line + right

    print()
    print(sep_line("┌", "┬", "┐"))
    # Header
    hdr = "│ " + f"{'Image':<{name_w}s} "
    for t in tag_names:
        hdr += f"│ {t:>{data_w}s} "
    hdr += f"│ {'Δ total':>{data_w}s} │"
    print(hdr)
    # Header-data separator
    print(sep_line("├", "┼", "┤"))

    # Data rows
    errors_to_show: list[tuple[str, str]] = []
    for img in images_in_order:
        vals: list[str] = []
        totals: list[float] = []

        for col_idx, by_name in enumerate(run_lookup):
            r = by_name.get(img)
            if r and r.get("total_s") is not None:
                if r.get("passed"):
                    val = f"{r['total_s']}s (P)"
                else:
                    val = f"{r['total_s']}s (F)"
                totals.append(r["total_s"])
            elif r and r.get("error"):
                val = "FAIL (E)"
            else:
                val = " N/A   "
            vals.append(val)

        # Overall delta: first run → latest run
        if len(totals) >= 2:
            diff = round(totals[-1] - totals[0], 1)
            if diff < 0:
                delta_str = f"\u2193{-diff:.1f}s"
            elif diff > 0:
                delta_str = f"\u2191{diff:.1f}s"
            else:
                delta_str = " 0.0s  "
        else:
            delta_str = "  N/A  "

        row = f"│ {img:<{name_w}s} "
        for v in vals:
            row += f"│ {v:>{data_w}s} "
        row += f"│ {delta_str:>{data_w}s} │"
        print(row)

        # Collect errors for footer
        last_r = run_lookup[-1].get(img)
        if last_r and last_r.get("error"):
            errors_to_show.append((img, last_r["error"]))

    # Bottom border
    print(sep_line("└", "┴", "┘"))

    # Footer — error details
    if errors_to_show:
        print()
        for img, err in errors_to_show:
            short = err.split("(")[-1].rstrip(")") if "(" in err else err
            print(f"  {img}: {short}")

    print()


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Benchmark mvmctl VM boot times.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Examples:\n"
            "  %(prog)s                          # run all configured images\n"
            "  %(prog)s --image ubuntu-24.04     # single image only\n"
            "  %(prog)s --tag after-fix          # tag this run\n"
            "  %(prog)s --no-parallel             # serial execution\n"
            "  %(prog)s --kernel 62b0a4c  # use a specific kernel\n"
            "  %(prog)s --compare                # show history\n"
        ),
    )
    parser.add_argument(
        "--image",
        action="append",
        dest="images",
        help="Test only this image (can be repeated).",
    )
    parser.add_argument(
        "--threshold",
        type=int,
        default=None,
        help="Override the threshold (seconds) for all tested images.",
    )
    parser.add_argument(
        "--tag",
        type=str,
        default=None,
        help="Label this run (e.g. 'before-fix', 'after-fix').",
    )
    parser.add_argument(
        "--compare",
        action="store_true",
        help="Show previous results without running benchmarks.",
    )
    parser.add_argument(
        "--parallel",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Run benchmarks in parallel (default). Use --no-parallel for serial.",
    )
    parser.add_argument(
        "--kernel",
        type=str,
        default=None,
        help="Kernel ID/prefix to use for all VMs. Omit to use default kernel.",
    )
    parser.add_argument(
        "--bin",
        type=str,
        default=None,
        help="Path to mvm binary (e.g. ./dist/mvm). Default: uv run mvm",
    )
    parser.add_argument(
        "--skip-deblob",
        action="store_true",
        default=False,
        help="Pass --skip-deblob to mvm vm create (skip rootfs debloat).",
    )
    return parser.parse_args(argv)


def main() -> int:
    args = parse_args()

    # --bin flag: override the mvm command (default: ["uv", "run", "mvm"])
    if args.bin:
        MVM.clear()
        MVM.append(args.bin)

    # --history mode --------------------------------------------------------
    if args.compare:
        show_history()
        return 0

    # Select images ---------------------------------------------------------
    configs = list(IMAGES)
    if args.images:
        selected = {img for img in args.images}
        configs = [c for c in configs if c.name in selected]
        missing = selected - {c.name for c in configs}
        if missing:
            known = ", ".join(c.name for c in IMAGES)
            print(
                f"Unknown image(s): {', '.join(sorted(missing))}\n"
                f"Known images: {known}",
                file=sys.stderr,
            )
            return 1

    # Override threshold ----------------------------------------------------
    if args.threshold is not None:
        for c in configs:
            c.threshold_s = args.threshold

    # Load previous run for comparison --------------------------------------
    history = load_history()
    prev_run = history[-1] if history else None
    if prev_run:
        print(
            f"  Comparing against previous run: {prev_run.get('tag', 'untagged')} "
            f"({prev_run.get('timestamp', '?')})"
        )

    # Run benchmarks --------------------------------------------------------
    results_map: dict[str, dict] = {}

    if args.parallel and len(configs) > 1:
        print(f"  Running {len(configs)} benchmarks in parallel...\n")
        with concurrent.futures.ThreadPoolExecutor(max_workers=len(configs)) as pool:
            fut_map = {
                pool.submit(
                    bench_image,
                    cfg,
                    kernel_id=args.kernel,
                    skip_deblob=args.skip_deblob,
                ): cfg
                for cfg in configs
            }
            for future in concurrent.futures.as_completed(fut_map):
                cfg = fut_map[future]
                try:
                    r = future.result()
                except Exception as e:
                    r = {
                        "name": cfg.name,
                        "threshold_s": cfg.threshold_s,
                        "create_s": None,
                        "total_s": None,
                        "passed": False,
                        "error": str(e),
                    }
                create_str = (
                    f"{r['create_s']}s" if r.get("create_s") is not None else "?"
                )
                total_str = f"{r['total_s']}s" if r.get("total_s") is not None else "?"
                mark = "✅" if r["passed"] else "❌"
                print(
                    f"  {mark}  {cfg.name:<28s}  "
                    f"(create {create_str:>5s}  →Ready {total_str:>5s})"
                )
                results_map[cfg.name] = r
    else:
        for cfg in configs:
            print(
                f"  ⏳  {cfg.name:<28s}  (threshold ≤{cfg.threshold_s}s) ...",
                end=" ",
                flush=True,
            )
            r = bench_image(cfg, kernel_id=args.kernel, skip_deblob=args.skip_deblob)
            tag = "✅" if r["passed"] else "❌"
            print(f"{tag}")
            results_map[cfg.name] = r

    # Restore config order for display
    bench_results = [results_map[cfg.name] for cfg in configs]

    # Save to history -------------------------------------------------------
    run_record = {
        "tag": args.tag or f"run-{len(history)}",
        "timestamp": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "kernel": args.kernel or "default",
        "results": bench_results,
    }
    save_run(run_record)

    # Print report ----------------------------------------------------------
    print_current_results(bench_results, prev_run)

    return 0 if all(r["passed"] for r in bench_results) else 1


if __name__ == "__main__":
    sys.exit(main())
