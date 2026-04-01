#!/usr/bin/env python3
"""
deploy.py — sequential deploy pipeline with a persistent status bar at the bottom
of the terminal.

Usage:
  deploy.py --step LABEL CMD [--step LABEL CMD ...]

Each --step defines one pipeline stage; stages execute in order. Output from
each command scrolls above the bar. The bar shows:
  ✓ label        green   (completed)
  ⠋ label        bold    (current, animated spinner)
    label        gray    (pending)
"""

import argparse
import os
import signal
import subprocess
import sys
import threading
import time

# ── ANSI escape helpers ───────────────────────────────────────────────────────
BOLD  = "\033[1m"
GRAY  = "\033[90m"
GREEN = "\033[32m"
RED   = "\033[31m"
RESET = "\033[0m"
ERASE = "\033[2K"   # erase entire current line

SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]


def render_pipeline(steps, current, frame):
    """Return the pipeline bar string (no trailing newline)."""
    parts = []
    for i, (label, _) in enumerate(steps):
        if i < current:
            parts.append(f"{GREEN}✓ {label}{RESET}")
        elif i == current:
            parts.append(f"{BOLD}{SPINNER_FRAMES[frame]} {label}{RESET}")
        else:
            parts.append(f"{GRAY}{label}{RESET}")
    return "  " + f" {GRAY}→{RESET} ".join(parts)


def main():
    parser = argparse.ArgumentParser(
        description="Run a sequential deploy pipeline with a persistent pipeline bar."
    )
    parser.add_argument(
        "--step",
        nargs=2,
        metavar=("LABEL", "CMD"),
        action="append",
        required=True,
        help="Add a pipeline stage: LABEL is the display name, CMD is the shell command.",
    )
    args = parser.parse_args()
    steps = args.step  # list of [label, cmd]

    lock = threading.Lock()
    spinner_idx = [0]
    current_step = [0]
    stop_event = threading.Event()
    active_proc = [None]

    def _write_bar():
        """Must be called with lock held."""
        bar = render_pipeline(steps, current_step[0], spinner_idx[0])
        sys.stdout.write(f"\r{ERASE}{bar}")
        sys.stdout.flush()

    def spinner_thread():
        while not stop_event.is_set():
            time.sleep(0.1)
            with lock:
                spinner_idx[0] = (spinner_idx[0] + 1) % len(SPINNER_FRAMES)
                _write_bar()

    def handle_interrupt(signum, frame):
        stop_event.set()
        proc = active_proc[0]
        if proc is not None:
            try:
                proc.terminate()
            except OSError:
                pass
        with lock:
            sys.stdout.write(f"\r{ERASE}{RED}✗ Interrupted{RESET}\n")
            sys.stdout.flush()
        sys.exit(130)

    signal.signal(signal.SIGINT, handle_interrupt)

    # Print initial blank line so the first pipeline bar doesn't sit on the
    # prompt line, then render the bar.
    sys.stdout.write("\n")
    with lock:
        _write_bar()

    t = threading.Thread(target=spinner_thread, daemon=True)
    t.start()

    try:
        for i, (label, cmd) in enumerate(steps):
            with lock:
                current_step[0] = i
                _write_bar()

            # Strip recursive-make flags so sub-make calls work correctly.
            env = os.environ.copy()
            env.pop("MAKEFLAGS", None)
            env.pop("MAKELEVEL", None)

            proc = subprocess.Popen(
                cmd,
                shell=True,
                env=env,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                bufsize=1,
            )
            active_proc[0] = proc

            for raw_line in proc.stdout:
                line = raw_line.rstrip("\n")
                with lock:
                    # Erase bar → print output line → reprint bar below it.
                    bar = render_pipeline(steps, current_step[0], spinner_idx[0])
                    sys.stdout.write(f"\r{ERASE}{line}\n{ERASE}{bar}")
                    sys.stdout.flush()

            proc.wait()
            active_proc[0] = None

            if proc.returncode != 0:
                with lock:
                    sys.stdout.write(
                        f"\r{ERASE}"
                        f"{RED}✗ Step '{label}' failed "
                        f"(exit {proc.returncode}){RESET}\n"
                    )
                    sys.stdout.flush()
                sys.exit(proc.returncode)

        # All steps done — render final bar with all green, then newline.
        stop_event.set()
        with lock:
            current_step[0] = len(steps)
            bar = render_pipeline(steps, current_step[0], 0)
            sys.stdout.write(f"\r{ERASE}{bar}\n")
            sys.stdout.flush()

    finally:
        stop_event.set()


if __name__ == "__main__":
    main()
