"""Tests for the threaded HTTP service helper."""

import json
import urllib.error
import urllib.request

import pytest

from wendytrain.service import serve


@pytest.fixture()
def server():
    def get_status(body: bytes) -> tuple[int, bytes, str]:
        return 200, json.dumps({"ok": True}).encode(), "application/json"

    def post_echo(body: bytes) -> tuple[int, bytes, str]:
        return 200, body, "application/octet-stream"

    def get_boom(body: bytes) -> tuple[int, bytes, str]:
        raise RuntimeError("handler exploded")

    srv = serve(
        {
            ("GET", "/status"): get_status,
            ("POST", "/echo"): post_echo,
            ("GET", "/boom"): get_boom,
        },
        port=0,
        host="127.0.0.1",
    )
    yield srv
    srv.shutdown()


def base(server) -> str:
    return f"http://127.0.0.1:{server.server_address[1]}"


def test_get_dispatch(server):
    with urllib.request.urlopen(f"{base(server)}/status", timeout=5) as resp:
        assert resp.status == 200
        assert resp.headers["Content-Type"] == "application/json"
        assert json.loads(resp.read()) == {"ok": True}


def test_post_dispatch_receives_body(server):
    req = urllib.request.Request(f"{base(server)}/echo", data=b"payload bytes", method="POST")
    with urllib.request.urlopen(req, timeout=5) as resp:
        assert resp.status == 200
        assert resp.read() == b"payload bytes"


def test_unknown_route_is_404(server):
    with pytest.raises(urllib.error.HTTPError) as excinfo:
        urllib.request.urlopen(f"{base(server)}/nope", timeout=5)
    assert excinfo.value.code == 404


def test_wrong_method_on_known_path_is_404(server):
    req = urllib.request.Request(f"{base(server)}/status", data=b"x", method="POST")
    with pytest.raises(urllib.error.HTTPError) as excinfo:
        urllib.request.urlopen(req, timeout=5)
    assert excinfo.value.code == 404


def test_handler_exception_yields_500_and_server_survives(server, capsys):
    with pytest.raises(urllib.error.HTTPError) as excinfo:
        urllib.request.urlopen(f"{base(server)}/boom", timeout=5)
    assert excinfo.value.code == 500
    # The error is logged, not swallowed.
    assert "handler exploded" in capsys.readouterr().err
    # The server keeps answering after a handler failure.
    with urllib.request.urlopen(f"{base(server)}/status", timeout=5) as resp:
        assert resp.status == 200
