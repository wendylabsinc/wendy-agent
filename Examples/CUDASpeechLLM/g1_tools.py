"""Fail-closed SpeechLLM tools for Walter's guarded G1 action boundary."""

from __future__ import annotations

import json
import os
import time
from typing import Any, Callable
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen


ALLOWED_GESTURES = ("raise_hand", "wave_hand", "shake_hand", "stop")

TOOL_SCHEMAS = [
    {
        "type": "function",
        "function": {
            "name": "g1_status",
            "description": (
                "Read current guarded G1 readiness and action state without moving "
                "the robot. Use for live capability or status questions."
            ),
            "parameters": {
                "type": "object",
                "properties": {},
                "additionalProperties": False,
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "g1_gesture",
            "description": (
                "Request one exact allowlisted G1 gesture only when the visitor "
                "explicitly addresses Walter by name and asks Walter to perform it "
                "in the current turn. An urgent stop may omit Walter's name. Never "
                "use for hypothetical discussion, unrelated speech, or Walter's "
                "own output."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "action": {
                        "type": "string",
                        "enum": list(ALLOWED_GESTURES),
                    },
                    "command_text": {
                        "type": "string",
                        "description": (
                            "Copy the visitor's exact current-turn command words. "
                            "Do not paraphrase, infer missing words, or copy Walter's "
                            "reply or an earlier turn."
                        ),
                    },
                },
                "required": ["action", "command_text"],
                "additionalProperties": False,
            },
        },
    },
]


def _enabled(name: str, default: bool = False) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


class G1ToolError(RuntimeError):
    pass


class G1ControlClient:
    def __init__(
        self,
        *,
        enabled: bool,
        g1_url: str,
        g1_token: str,
        orchestrator_url: str,
        orchestrator_token: str,
        timeout: float = 3.0,
        clock: Callable[[], int] = time.monotonic_ns,
        sleep: Callable[[float], None] = time.sleep,
        opener: Callable[..., Any] = urlopen,
    ) -> None:
        self.enabled = enabled
        self.g1_url = g1_url.rstrip("/")
        self.g1_token = g1_token
        self.orchestrator_url = orchestrator_url.rstrip("/")
        self.orchestrator_token = orchestrator_token
        self.timeout = timeout
        self.clock = clock
        self.sleep = sleep
        self.opener = opener

    @classmethod
    def from_env(cls) -> "G1ControlClient":
        return cls(
            enabled=_enabled("G1_ACTION_TOOLS_ENABLED"),
            g1_url=os.environ.get(
                "G1_SYNC_URL", "http://unitree-g1-nx.local:8094"
            ),
            g1_token=os.environ.get("G1_SYNC_TOKEN", ""),
            orchestrator_url=os.environ.get(
                "SPARK_ORCHESTRATOR_URL", "http://127.0.0.1:8093"
            ),
            orchestrator_token=os.environ.get("SPARK_ORCHESTRATOR_TOKEN", ""),
            timeout=float(os.environ.get("G1_TOOL_TIMEOUT_SECONDS", "3")),
        )

    def _request(
        self,
        base_url: str,
        token: str,
        path: str,
        *,
        payload: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        headers = {"Accept": "application/json"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = Request(
            f"{base_url}{path}",
            data=body,
            headers=headers,
            method="POST" if body is not None else "GET",
        )
        try:
            with self.opener(request, timeout=self.timeout) as response:
                value = json.load(response)
        except HTTPError as exc:
            try:
                detail = json.loads(exc.read()).get("error", str(exc))
            except Exception:
                detail = str(exc)
            raise G1ToolError(f"guarded G1 request rejected: {detail}") from exc
        except (OSError, URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise G1ToolError(f"guarded G1 service unavailable: {exc}") from exc
        if not isinstance(value, dict):
            raise G1ToolError("guarded G1 service returned a non-object response")
        return value

    def status(self) -> dict[str, Any]:
        if not self.enabled or not self.g1_token:
            raise G1ToolError("G1 action tools are not configured")
        payload = self._request(self.g1_url, self.g1_token, "/v1/status")
        runtime = payload.get("action_runtime") or {}
        hardware = runtime.get("hardware") or {}
        return {
            "success": bool(payload.get("ok") and payload.get("ready")),
            "ready": bool(payload.get("ready")),
            "pending": int(payload.get("pending") or 0),
            "active_turn": runtime.get("active_turn"),
            "prepared_turn": runtime.get("prepared_turn"),
            "hardware_connected": bool(hardware.get("connected")),
            "lowstate_ready": bool(hardware.get("lowstate_ready")),
            "lowstate_age_ms": hardware.get("lowstate_age_ms"),
            "allowed_actions": list(ALLOWED_GESTURES),
        }

    def admit_tool(self, name: str, arguments: dict[str, Any]) -> tuple[dict, str | None]:
        if name == "g1_status":
            if arguments:
                raise G1ToolError("g1_status does not accept arguments")
            return self.status(), None
        if name != "g1_gesture":
            raise G1ToolError(f"unsupported tool: {name}")
        action = arguments.get("action")
        if action not in ALLOWED_GESTURES:
            raise G1ToolError("unsupported G1 gesture")
        if set(arguments) != {"action"}:
            raise G1ToolError("g1_gesture accepts only the action field")
        status = self.status()
        if not status["ready"] or not status["hardware_connected"] or not status["lowstate_ready"]:
            raise G1ToolError("the G1 action boundary is not ready")
        if action == "stop":
            if status["active_turn"] is None and status["prepared_turn"] is None:
                raise G1ToolError("there is no active or prepared gesture to stop")
        elif status["pending"] or status["active_turn"] or status["prepared_turn"]:
            raise G1ToolError("another G1 gesture is active or prepared")
        return (
            {
                "success": True,
                "accepted": True,
                "action": action,
                "state": "pending_speech_schedule",
                "instruction": (
                    "Acknowledge in future tense only. Do not say the motion already "
                    "happened."
                ),
            },
            action,
        )

    def schedule(self, turn_id: str, action: str) -> dict[str, Any]:
        if not self.orchestrator_token:
            raise G1ToolError("Spark orchestrator control is not configured")
        plan = self._request(
            self.orchestrator_url,
            self.orchestrator_token,
            "/v1/responses/schedule",
            payload={"request_id": turn_id, "action": action},
        )
        if not plan.get("action_scheduled"):
            raise G1ToolError(str(plan.get("action_error") or "G1 action was not scheduled"))
        return plan

    def wait_until_playback(self, plan: dict[str, Any]) -> None:
        target = int(plan["playback_at_ns"])
        remaining = target - self.clock()
        if remaining > 0:
            self.sleep(remaining / 1_000_000_000)

    def wait_until_prepared(self, turn_id: str, plan: dict[str, Any]) -> dict[str, Any]:
        """Require G1 preflight success before Walter begins the spoken claim."""
        deadline = int(plan["playback_at_ns"]) - 75_000_000
        last: dict[str, Any] = {}
        while self.clock() < deadline:
            try:
                payload = self.timing(turn_id)
            except G1ToolError:
                self.sleep(0.04)
                continue
            last = payload.get("timing") or {}
            state = str(last.get("state") or "")
            if state in {"prepared", "dispatching", "dispatched", "accepted", "released"}:
                return last
            if state.endswith("failed") or state.startswith("cancelled"):
                raise G1ToolError(str(last.get("error") or f"G1 preflight {state}"))
            self.sleep(0.04)
        raise G1ToolError(
            str(last.get("error") or "G1 preflight was not confirmed before playback")
        )

    def record_event(
        self, turn_id: str, event: str, *, monotonic_ns: int | None = None
    ) -> dict[str, Any]:
        return self._request(
            self.orchestrator_url,
            self.orchestrator_token,
            "/v1/events",
            payload={
                "turn_id": turn_id,
                "event": event,
                "monotonic_ns": self.clock() if monotonic_ns is None else monotonic_ns,
                "source": "speechllm-powerconf",
            },
        )

    def cancel(self, turn_id: str) -> dict[str, Any]:
        return self._request(
            self.orchestrator_url,
            self.orchestrator_token,
            "/v1/actions/cancel",
            payload={"request_id": turn_id},
        )

    def timing(self, turn_id: str) -> dict[str, Any]:
        return self._request(
            self.g1_url,
            self.g1_token,
            f"/v1/timings/{quote(turn_id, safe='')}",
        )

    def alignment_timing(self, turn_id: str) -> dict[str, Any]:
        """Return the Spark journal plus clock-translated G1 alignment report."""
        return self._request(
            self.orchestrator_url,
            self.orchestrator_token,
            f"/v1/timings/{quote(turn_id, safe='')}",
        )
