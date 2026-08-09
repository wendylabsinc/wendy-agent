"""Tests for peer parsing, role derivation, worker slicing, and Fleet.from_env."""

import http.server
import threading

import pytest

from wendytrain.mesh import Fleet, derive_role, http_get, http_post, parse_peers, worker_slice


class TestParsePeers:
    def test_bare_id_expands_to_mesh_hostname_with_default_port(self):
        assert parse_peers("215") == ["device-215.cloud.wendy.dev:8080"]

    def test_bare_id_uses_custom_default_port(self):
        assert parse_peers("215", default_port=9000) == ["device-215.cloud.wendy.dev:9000"]

    def test_id_with_port(self):
        assert parse_peers("215:8081") == ["device-215.cloud.wendy.dev:8081"]

    def test_full_hostname_with_port_passes_through(self):
        assert parse_peers("device-215.cloud.wendy.dev:8080") == [
            "device-215.cloud.wendy.dev:8080"
        ]

    def test_bare_hostname_gets_default_port(self):
        assert parse_peers("spark-3011.local") == ["spark-3011.local:8080"]

    def test_mixed_entries_with_whitespace(self):
        raw = " 211 , 334:9000 , spark-edeb.local , host.example:7000 "
        assert parse_peers(raw) == [
            "device-211.cloud.wendy.dev:8080",
            "device-334.cloud.wendy.dev:9000",
            "spark-edeb.local:8080",
            "host.example:7000",
        ]

    def test_dedupe_preserves_order(self):
        assert parse_peers("211,334,211,334:8080") == [
            "device-211.cloud.wendy.dev:8080",
            "device-334.cloud.wendy.dev:8080",
        ]

    def test_self_is_skipped_by_id(self):
        assert parse_peers("211,334,283", self_id="334") == [
            "device-211.cloud.wendy.dev:8080",
            "device-283.cloud.wendy.dev:8080",
        ]

    def test_self_skip_applies_to_ported_ids(self):
        assert parse_peers("334:9000", self_id="334") == []

    def test_empty_and_blank_entries_are_skipped(self):
        assert parse_peers("") == []
        assert parse_peers(" , ,211,") == ["device-211.cloud.wendy.dev:8080"]


class TestDeriveRole:
    def test_lowest_id_is_coordinator(self):
        assert derive_role("211", "211,283,334") == "coordinator"

    def test_higher_id_is_worker(self):
        assert derive_role("334", "211,283,334") == "worker"

    def test_self_not_listed_in_peers_still_participates(self):
        # MESH_PEERS may list only the others; self joins via MESH_SELF.
        assert derive_role("100", "211,283") == "coordinator"
        assert derive_role("400", "211,283") == "worker"

    def test_ids_compare_numerically_not_lexically(self):
        assert derive_role("9", "9,100") == "coordinator"

    def test_alone_is_coordinator(self):
        assert derive_role("211", "") == "coordinator"

    def test_explicit_role_wins(self):
        assert derive_role("334", "211,334", explicit="coordinator") == "coordinator"
        assert derive_role("211", "211,334", explicit="actor") == "actor"

    def test_hostname_peer_is_ambiguous_and_raises(self):
        with pytest.raises(ValueError, match="WT_ROLE"):
            derive_role("211", "spark-3011.local,283")

    def test_missing_self_id_raises(self):
        with pytest.raises(ValueError, match="WT_ROLE"):
            derive_role("", "211,283")

    def test_non_numeric_self_id_raises(self):
        with pytest.raises(ValueError, match="WT_ROLE"):
            derive_role("spark", "211,283")


class TestWorkerSlice:
    def test_disjoint_full_cover_for_all_uneven_splits(self):
        for population in range(1, 51):
            for count in range(1, 8):
                slices = [worker_slice(i, count, population) for i in range(count)]
                combined = [j for s in slices for j in s]
                assert combined == list(range(population)), (population, count)

    def test_remainder_goes_to_the_last_worker(self):
        assert worker_slice(0, 3, 10) == range(0, 3)
        assert worker_slice(1, 3, 10) == range(3, 6)
        assert worker_slice(2, 3, 10) == range(6, 10)

    def test_index_out_of_range_raises(self):
        with pytest.raises(ValueError):
            worker_slice(3, 3, 10)
        with pytest.raises(ValueError):
            worker_slice(-1, 3, 10)


class TestFleetFromEnv:
    def test_reads_documented_variables(self):
        env = {
            "MESH_SELF": "211",
            "MESH_PEERS": "211,283,334",
            "MESH_PORT": "9000",
            "WT_ROLE": "auto",
            "WT_RUN_ID": "demo-1",
            "WT_CKPT_DIR": "/data/ckpt",
        }
        fleet = Fleet.from_env(env)
        assert fleet.role == "coordinator"
        assert fleet.self_id == "211"
        assert fleet.peers == [
            "device-283.cloud.wendy.dev:9000",
            "device-334.cloud.wendy.dev:9000",
        ]
        assert fleet.port == 9000
        assert fleet.run_id == "demo-1"
        assert fleet.ckpt_dir == "/data/ckpt"

    def test_defaults(self):
        fleet = Fleet.from_env({"MESH_SELF": "1", "MESH_PEERS": "1"})
        assert fleet.port == 8080
        assert fleet.run_id == "default"
        assert fleet.ckpt_dir == "/data/checkpoints"
        assert fleet.role == "coordinator"
        assert fleet.peers == []

    def test_explicit_role_avoids_derivation(self):
        fleet = Fleet.from_env(
            {"MESH_SELF": "", "MESH_PEERS": "spark.local", "WT_ROLE": "worker"}
        )
        assert fleet.role == "worker"


class TestHttpHelpers:
    @pytest.fixture()
    def server(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def _reply(self, body: bytes):
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                self._reply(b"pong")

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                self._reply(self.rfile.read(length))

            def log_message(self, *args):
                pass

        srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        yield f"127.0.0.1:{srv.server_address[1]}"
        srv.shutdown()

    def test_http_get(self, server):
        assert http_get(f"http://{server}/", retries=1) == b"pong"

    def test_http_post_echoes_body(self, server):
        assert http_post(f"http://{server}/", b"payload", retries=1) == b"payload"

    def test_http_get_raises_after_retries_exhausted(self):
        with pytest.raises(OSError):
            http_get("http://127.0.0.1:1/", timeout=0.2, retries=2)
