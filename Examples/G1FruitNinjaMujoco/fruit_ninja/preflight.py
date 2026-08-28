"""Verify that a selected MuJoCo rendering backend can render this scene."""

from __future__ import annotations

import sys

from .simulation import FruitNinjaSimulation


def main() -> int:
    simulation: FruitNinjaSimulation | None = None
    try:
        simulation = FruitNinjaSimulation(enable_renderer=True)
        simulation.run_headless_steps(2)
        simulation._render()  # preflight intentionally exercises the renderer
        status = simulation.status()
        if status["frameSeq"] < 1:
            raise RuntimeError("renderer produced no frame")
        print(
            f"[preflight] MuJoCo {status['mujocoVersion']} backend={status['renderer']} ready",
            flush=True,
        )
        return 0
    except Exception as error:
        print(f"[preflight] {type(error).__name__}: {error}", file=sys.stderr, flush=True)
        return 1
    finally:
        if simulation is not None:
            simulation.close()


if __name__ == "__main__":
    raise SystemExit(main())
