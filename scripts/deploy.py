#!/usr/bin/env python3
"""
deploy.py — sequential deploy pipeline with a pinned bottom status bar.

Usage:
  deploy.py --step LABEL CMD [--step LABEL CMD ...]

The terminal is split into two regions:
  - rows 1 .. h-1  scrolling output area (subprocess stdout/stderr)
  - row  h         pipeline status bar   (never scrolls away)

A full-width green / red divider is printed at the end of each step.
"""

import argparse
import atexit
import os
import shutil
import signal
import subprocess
import sys
import threading
import time

# ── ANSI helpers ──────────────────────────────────────────────────────────────
BOLD  = "\033[1m"
GRAY  = "\033[90m"
GREEN = "\033[32m"
RED   = "\033[31m"
RESET = "\033[0m"

SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"]

# ── Shared state ──────────────────────────────────────────────────────────────
_steps: list[tuple[str, str]] = []
_current   = [0]
_spin_idx  = [0]
_lock      = threading.Lock()
_scroll_on = [False]


def _size() -> tuple[int, int]:
    ts = shutil.get_terminal_size((80, 24))
    return ts.columns, ts.lines


def _render_bar() -> str:
    parts = []
    for i, (label, _) in enumerate(_steps):
        if i < _current[0]:
            parts.append(f"{GREEN}✓ {label}{RESET}")
        elif i == _current[0]:
            parts.append(f"{BOLD}{SPINNER_FRAMES[_spin_idx[0]]} {label}{RESET}")
        else:
            parts.append(f"{GRAY}{label}{RESET}")
    return "  " + f" {GRAY}→{RESET} ".join(parts)


# ── Terminal scroll-region management ─────────────────────────────────────────

def _enter_scroll_mode() -> None:
    _, h = _size()
    # Save the current cursor position (start of the output area).
    sys.stdout.write("\033[s")
    # Restrict scrolling to rows 1 .. h-1; row h is the bar and never scrolls.
    sys.stdout.write(f"\033[1;{h - 1}r")
    # Write the initial bar at row h.
    sys.stdout.write(f"\033[{h};1H\033[2K{_render_bar()}")
    # Restore cursor to the output area.
    sys.stdout.write("\033[u")
    sys.stdout.flush()
    _scroll_on[0] = True
    atexit.register(_exit_scroll_mode)


def _exit_scroll_mode() -> None:
    if not _scroll_on[0]:
        return
    _, h = _size()
    # Reset scroll region to the full terminal.
    sys.stdout.write("\033[r")
    # Move to the last line and emit a newline so the shell prompt appears below.
    sys.stdout.write(f"\033[{h};1H\n")
    sys.stdout.flush()
    _scroll_on[0] = False


def _handle_resize(sig: int, frame: object) -> None:
    """Update the scroll region and redraw the bar after a terminal resize."""
    with _lock:
        _, h = _size()
        sys.stdout.write(f"\033[1;{h - 1}r")
        _refresh_bar()


# ── Bar / output primitives (call with _lock held) ────────────────────────────

def _refresh_bar() -> None:
    """Redraw the bar at the bottom line; save/restore the cursor."""
    _, h = _size()
    sys.stdout.write(f"\033[s\033[{h};1H\033[2K{_render_bar()}\033[u")
    sys.stdout.flush()


def _write_line(line: str) -> None:
    """Write one output line into the scroll region, then refresh the bar."""
    sys.stdout.write(line + "\n")
    # Save the new cursor position (after the line) before jumping to the bar.
    sys.stdout.write("\033[s")
    _, h = _size()
    sys.stdout.write(f"\033[{h};1H\033[2K{_render_bar()}\033[u")
    sys.stdout.flush()


def _write_divider(color: str) -> None:
    """Print a full-width horizontal rule in the scroll region."""
    w, _ = _size()
    _write_line(f"{color}{'━' * w}{RESET}")


# ── Background spinner ────────────────────────────────────────────────────────

def _spinner_loop(stop: threading.Event) -> None:
    while not stop.is_set():
        time.sleep(0.1)
        with _lock:
            _spin_idx[0] = (_spin_idx[0] + 1) % len(SPINNER_FRAMES)
            _refresh_bar()


# ── Main ──────────────────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(
        description="Run a sequential deploy pipeline with a pinned status bar."
    )
    parser.add_argument(
        "--step",
        nargs=2,
        metavar=("LABEL", "CMD"),
        action="append",
        required=True,
    )
    args = parser.parse_args()
    _steps[:] = args.step

    stop       = threading.Event()
    active_proc: list[subprocess.Popen | None] = [None]

    def on_interrupt(sig: int, frame: object) -> None:
        stop.set()
        p = active_proc[0]
        if p:
            try:
                p.terminate()
            except OSError:
                pass
        _exit_scroll_mode()
        sys.exit(130)

    signal.signal(signal.SIGINT,  on_interrupt)
    signal.signal(signal.SIGWINCH, _handle_resize)

    # Ensure we start on a fresh line, then enter scroll mode.
    sys.stdout.write("\n")
    _enter_scroll_mode()

    spinner = threading.Thread(target=_spinner_loop, args=(stop,), daemon=True)
    spinner.start()

    try:
        for i, (label, cmd) in enumerate(_steps):
            with _lock:
                _current[0] = i
                _refresh_bar()

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

            for raw in proc.stdout:
                with _lock:
                    _write_line(raw.rstrip("\n"))

            proc.wait()
            active_proc[0] = None

            if proc.returncode != 0:
                with _lock:
                    _write_divider(RED)
                    _write_line(
                        f"{RED}✗ Step '{label}' failed "
                        f"(exit {proc.returncode}){RESET}"
                    )
                stop.set()
                _exit_scroll_mode()
                sys.exit(proc.returncode)

            with _lock:
                _write_divider(GREEN)

        # All steps succeeded.
        stop.set()
        with _lock:
            _current[0] = len(_steps)
            _refresh_bar()
        _exit_scroll_mode()

    finally:
        stop.set()


if __name__ == "__main__":
    main()
