"""Select EGL on NVIDIA hosts and OSMesa for portable headless rendering."""

from __future__ import annotations

import os
import platform
import subprocess
import sys


def _backend_candidates() -> list[str]:
    requested = os.environ.get("MUJOCO_GL", "").strip().lower()
    if requested and requested != "auto":
        return [requested]
    if platform.system() == "Darwin":
        return ["glfw"]
    has_nvidia = os.path.exists("/dev/nvidia0") or os.path.exists("/proc/driver/nvidia/version")
    return ["egl", "osmesa"] if has_nvidia else ["osmesa", "egl"]


def main() -> None:
    failures: list[str] = []
    for backend in _backend_candidates():
        environment = os.environ.copy()
        environment["MUJOCO_GL"] = backend
        print(f"[bootstrap] testing MuJoCo renderer backend={backend}", flush=True)
        completed = subprocess.run(
            [sys.executable, "-m", "fruit_ninja.preflight"],
            env=environment,
            timeout=45,
            check=False,
        )
        if completed.returncode == 0:
            print(f"[bootstrap] selected backend={backend}", flush=True)
            os.execve(
                sys.executable,
                [sys.executable, "-m", "fruit_ninja.server"],
                environment,
            )
        failures.append(f"{backend}: exit {completed.returncode}")
    raise SystemExit("no MuJoCo renderer backend passed preflight (" + ", ".join(failures) + ")")


if __name__ == "__main__":
    main()
