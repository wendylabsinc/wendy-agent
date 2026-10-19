"""Captured canned replies for short tool-choice scenarios: store, placeholders, scrubbing.

Replies are captured from real output (capture.py), never hand-written. The one exception is
FALLBACK_RESULT, returned (and flagged) for a call the scenario has no capture for.
"""
import json
import re
from pathlib import Path

PLACEHOLDER = re.compile(r"\{\{(args\.[A-Za-z0-9_]+|reply\.[a-z0-9_]+\.[A-Za-z0-9_]+|device)\}\}")
_ANY_BRACES = re.compile(r"\{\{.*?\}\}", re.S)
FALLBACK_RESULT = {"content": [{"type": "text", "text": "{}"}], "isError": False}

SCRUB_KEY = re.compile(r"(?i)serial|^mac(_?address)?$|^org(_?id)?$|asset(_?id)?|tenant|uuid|hostname")
_MAC = re.compile(r"\b[0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5}\b")
_UUID = re.compile(r"\b[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\b")
_CLOUD = re.compile(r"cloud://[^\s\"'<>,}\]]+")
_TOOL_NAME = re.compile(r"^[a-z0-9_]+$")


def _validate(value, where):
    if isinstance(value, str):
        for m in _ANY_BRACES.finditer(value):
            if not PLACEHOLDER.fullmatch(m.group(0)):
                raise ValueError(f"unknown placeholder {m.group(0)!r} in {where}")
    elif isinstance(value, dict):
        for v in value.values():
            _validate(v, where)
    elif isinstance(value, list):
        for v in value:
            _validate(v, where)


def _read(path):
    if not path.is_file():
        return None
    data = json.loads(path.read_text())
    _validate(data, str(path))
    return data


def load_capture(canned_dir, set_name, tool):
    if not isinstance(tool, str) or not _TOOL_NAME.match(tool):
        return None
    return _read(Path(canned_dir) / set_name / f"{tool}.json")


def load_cli_capture(canned_dir, set_name, words):
    """Return the CLI capture whose argv_prefix is the longest prefix of words, or None."""
    folder = Path(canned_dir) / set_name / "cli"
    best = None
    for path in sorted(folder.glob("*.json")) if folder.is_dir() else []:
        data = _read(path)
        prefix = data.get("argv_prefix") or []
        if list(words[:len(prefix)]) == prefix and (best is None or len(prefix) > len(best["argv_prefix"])):
            best = data
    return best


def _reply_value(reply, key):
    if not reply:
        return None
    structured = reply.get("structuredContent")
    if isinstance(structured, dict) and key in structured:
        return structured[key]
    for item in reply.get("content") or []:
        if item.get("type") == "text":
            try:
                parsed = json.loads(item.get("text", ""))
            except ValueError:
                return None
            return parsed.get(key) if isinstance(parsed, dict) else None
    return None


def _lookup(name, context):
    kind, _, rest = name.partition(".")
    if kind == "device":
        value = context.get("device")
    elif kind == "args":
        value = (context.get("args") or {}).get(rest)
    else:
        tool, _, key = rest.partition(".")
        value = _reply_value((context.get("replies") or {}).get(tool), key)
    if value is None:
        return ""
    return value if isinstance(value, str) else json.dumps(value)


def render(value, context):
    if isinstance(value, str):
        return PLACEHOLDER.sub(lambda m: _lookup(m.group(1), context), value)
    if isinstance(value, dict):
        if value.get("type") == "text" and isinstance(value.get("text"), str):
            try:
                parsed = json.loads(value["text"])
            except ValueError:
                parsed = None
            if isinstance(parsed, (dict, list)):
                # Render on the parsed value so substituted text gets proper JSON escaping.
                out = {k: render(v, context) for k, v in value.items() if k != "text"}
                out["text"] = json.dumps(render(parsed, context))
                return out
        return {k: render(v, context) for k, v in value.items()}
    if isinstance(value, list):
        return [render(v, context) for v in value]
    return value


class CannedStore:
    def __init__(self, canned_dir, set_name, context):
        self.canned_dir, self.set_name, self.context = canned_dir, set_name, context
        self._captures = {}
        self._steps = {}
        self._latest = {}

    def reply(self, tool, arguments):
        if not isinstance(tool, str) or not _TOOL_NAME.match(tool):
            return json.loads(json.dumps(FALLBACK_RESULT)), False
        if tool not in self._captures:
            self._captures[tool] = load_capture(self.canned_dir, self.set_name, tool)
        capture = self._captures[tool]
        replies = (capture or {}).get("replies") or []
        if not replies:
            return json.loads(json.dumps(FALLBACK_RESULT)), False
        step = self._steps.get(tool, 0)
        self._steps[tool] = step + 1
        raw = replies[min(step, len(replies) - 1)]
        # Placeholders may read this tool's own earlier replies too, so render before storing.
        result = render(raw, {**self.context, "args": arguments or {}, "replies": self._latest})
        self._latest[tool] = result
        return result, True


def scrub(value):
    if isinstance(value, dict):
        out = {}
        for k, v in value.items():
            if SCRUB_KEY.search(str(k)):
                out[k] = "scrubbed"
            elif k == "text" and isinstance(v, str) and value.get("type") == "text":
                out[k] = _scrub_text(v)
            else:
                out[k] = scrub(v)
        return out
    if isinstance(value, list):
        return [scrub(v) for v in value]
    if isinstance(value, str):
        return _scrub_string(value)
    return value


def _scrub_string(s):
    s = _CLOUD.sub("{{device}}", s)
    return _UUID.sub("00000000-0000-0000-0000-000000000000", _MAC.sub("02:00:00:00:00:00", s))


def scrub_output(s):
    """Scrub CLI stdout/stderr: parsed and re-serialized if JSON, else string-level."""
    return _scrub_text(s)


def _scrub_text(s):
    try:
        parsed = json.loads(s)
    except ValueError:
        return _scrub_string(s)
    if isinstance(parsed, (dict, list)):
        return json.dumps(scrub(parsed))
    return _scrub_string(s)
