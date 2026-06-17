#!/usr/bin/env python3
"""Compute render durations for a screencast scene plan."""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

SCENE_ID_RE = re.compile(r"^[A-Za-z0-9_.-]+$")
STRICT_STATUSES = {
    "missing_video",
    "missing_voiceover",
    "invalid_video",
    "invalid_voiceover",
    "final_less_than_minimum",
    "final_less_than_voiceover",
}


@dataclass
class Scene:
    line: int
    scene: str
    video_rel: str
    minimum_seconds: float
    voiceover_rel: str
    final_override_seconds: float | None


def parse_seconds(value: str, field: str, line: int) -> float:
    try:
        seconds = float(value)
    except ValueError as error:
        raise ValueError(f"line {line}: {field} must be a number: {value!r}") from error
    if seconds < 0:
        raise ValueError(f"line {line}: {field} must be non-negative: {value!r}")
    return seconds


def parse_plan(plan: Path) -> list[Scene]:
    scenes: list[Scene] = []
    for line_no, raw in enumerate(plan.read_text(encoding="utf-8").splitlines(), start=1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue

        parts = raw.split("\t")
        if len(parts) < 3:
            raise ValueError(f"line {line_no}: expected at least 3 tab-separated columns")
        if len(parts) > 5 and any(part.strip() for part in parts[5:]):
            raise ValueError(f"line {line_no}: expected at most 5 tab-separated columns")

        parts += [""] * (5 - len(parts))
        scene, video_rel, minimum, voiceover_rel, final_override = [part.strip() for part in parts[:5]]
        if not scene:
            raise ValueError(f"line {line_no}: scene_id is required")
        if not SCENE_ID_RE.match(scene):
            raise ValueError(
                f"line {line_no}: scene_id may only contain letters, numbers, dots, dashes, and underscores"
            )
        if not video_rel:
            raise ValueError(f"line {line_no}: video_path is required")
        if not minimum:
            raise ValueError(f"line {line_no}: min_seconds is required")

        scenes.append(
            Scene(
                line=line_no,
                scene=scene,
                video_rel=video_rel,
                minimum_seconds=parse_seconds(minimum, "min_seconds", line_no),
                voiceover_rel=voiceover_rel,
                final_override_seconds=(
                    parse_seconds(final_override, "final_seconds", line_no) if final_override else None
                ),
            )
        )
    return scenes


def ffprobe(args: list[str]) -> str:
    return subprocess.check_output(["ffprobe", "-v", "error", *args], text=True).strip()


def media_duration(path: Path) -> tuple[float | None, str | None]:
    if not path.exists():
        return None, "missing"
    if not shutil.which("ffprobe"):
        return None, "ffprobe_not_found"
    try:
        value = ffprobe(["-show_entries", "format=duration", "-of", "default=nk=1:nw=1", str(path)])
        return float(value), None
    except (subprocess.CalledProcessError, ValueError) as error:
        return None, str(error)


def video_stream(path: Path) -> str:
    if not path.exists() or not shutil.which("ffprobe"):
        return ""
    try:
        return ffprobe(
            [
                "-select_streams",
                "v:0",
                "-show_entries",
                "stream=width,height,r_frame_rate",
                "-of",
                "csv=p=0",
                str(path),
            ]
        )
    except subprocess.CalledProcessError:
        return ""


def format_seconds(value: float | None) -> str:
    return "" if value is None else f"{value:.3f}"


def analyze(plan: Path) -> list[dict[str, Any]]:
    project_dir = plan.resolve().parent
    rows: list[dict[str, Any]] = []

    for scene in parse_plan(plan):
        video_path = project_dir / scene.video_rel
        voiceover_path = project_dir / scene.voiceover_rel if scene.voiceover_rel else None

        statuses: list[str] = []
        video_seconds, video_error = media_duration(video_path)
        if video_error == "missing":
            statuses.append("missing_video")
        elif video_error:
            statuses.append("invalid_video")

        voiceover_seconds: float | None = None
        if voiceover_path is not None:
            voiceover_seconds, voiceover_error = media_duration(voiceover_path)
            if voiceover_error == "missing":
                statuses.append("missing_voiceover")
            elif voiceover_error:
                statuses.append("invalid_voiceover")

        recommended_seconds = max(scene.minimum_seconds, voiceover_seconds or 0.0)
        render_seconds = (
            scene.final_override_seconds
            if scene.final_override_seconds is not None
            else recommended_seconds
        )

        if scene.final_override_seconds is not None:
            if render_seconds + 0.010 < scene.minimum_seconds:
                statuses.append("final_less_than_minimum")
            if voiceover_seconds is not None and render_seconds + 0.010 < voiceover_seconds:
                statuses.append("final_less_than_voiceover")

        video_padding_seconds: float | None = None
        if video_seconds is not None and video_seconds + 0.010 < render_seconds:
            video_padding_seconds = render_seconds - video_seconds
            statuses.append("video_shorter_than_render")

        rows.append(
            {
                "line": scene.line,
                "scene": scene.scene,
                "video": scene.video_rel,
                "minimum_seconds": scene.minimum_seconds,
                "actual_video_seconds": video_seconds,
                "voiceover": scene.voiceover_rel,
                "voiceover_seconds": voiceover_seconds,
                "final_override_seconds": scene.final_override_seconds,
                "recommended_final_seconds": recommended_seconds,
                "render_seconds": render_seconds,
                "video_padding_seconds": video_padding_seconds,
                "status": ";".join(statuses),
                "video_stream": video_stream(video_path),
            }
        )
    return rows


def write_tsv(rows: list[dict[str, Any]]) -> None:
    headers = [
        "line",
        "scene",
        "video",
        "minimum_seconds",
        "actual_video_seconds",
        "voiceover",
        "voiceover_seconds",
        "final_override_seconds",
        "recommended_final_seconds",
        "render_seconds",
        "video_padding_seconds",
        "status",
        "video_stream",
    ]
    print("\t".join(headers))
    for row in rows:
        print(
            "\t".join(
                [
                    str(row["line"]),
                    row["scene"],
                    row["video"],
                    format_seconds(row["minimum_seconds"]),
                    format_seconds(row["actual_video_seconds"]),
                    row["voiceover"],
                    format_seconds(row["voiceover_seconds"]),
                    format_seconds(row["final_override_seconds"]),
                    format_seconds(row["recommended_final_seconds"]),
                    format_seconds(row["render_seconds"]),
                    format_seconds(row["video_padding_seconds"]),
                    row["status"],
                    row["video_stream"],
                ]
            )
        )


def write_markdown(rows: list[dict[str, Any]]) -> None:
    print("| Scene | Min | Video | VO | Render | Status |")
    print("|---|---:|---:|---:|---:|---|")
    for row in rows:
        print(
            "| {scene} | {minimum} | {video} | {voiceover} | {render} | {status} |".format(
                scene=row["scene"],
                minimum=format_seconds(row["minimum_seconds"]),
                video=format_seconds(row["actual_video_seconds"]),
                voiceover=format_seconds(row["voiceover_seconds"]),
                render=format_seconds(row["render_seconds"]),
                status=row["status"] or "ok",
            )
        )


def write_render_plan(rows: list[dict[str, Any]]) -> None:
    """Emit a shell-friendly internal plan with no empty fields."""
    for row in rows:
        print(
            "\t".join(
                [
                    row["scene"],
                    row["video"],
                    row["voiceover"] or "-",
                    format_seconds(row["render_seconds"]),
                    row["status"] or "ok",
                ]
            )
        )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("plan", nargs="?", default="scene-plan.tsv", type=Path)
    parser.add_argument("--format", choices=["tsv", "markdown", "json", "render"], default="tsv")
    parser.add_argument(
        "--strict",
        action="store_true",
        help="exit non-zero when required media is missing or an explicit final duration is too short",
    )
    args = parser.parse_args()

    rows = analyze(args.plan)
    if args.format == "tsv":
        write_tsv(rows)
    elif args.format == "markdown":
        write_markdown(rows)
    elif args.format == "json":
        print(json.dumps(rows, indent=2))
    else:
        write_render_plan(rows)

    if args.strict:
        failures = [row for row in rows if STRICT_STATUSES & set(filter(None, row["status"].split(";")))]
        if failures:
            print("error: scene plan has render-blocking issues", file=sys.stderr)
            for row in failures:
                print(f"  line {row['line']} scene {row['scene']}: {row['status']}", file=sys.stderr)
            return 1
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(2)
