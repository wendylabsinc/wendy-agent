from g1fleet.mesh import parse_peers, worker_index

def test_parse_peers_expands_ids_and_skips_self():
    t = parse_peers("284,283,211", self_id="283")
    assert t == ["device-284.cloud.wendy.dev:8080", "device-211.cloud.wendy.dev:8080"]

def test_parse_peers_accepts_hostports_and_dedupes():
    t = parse_peers("h:9,h:9,284:7", self_id="")
    assert t == ["h:9", "device-284.cloud.wendy.dev:7"]

def test_worker_index_is_stable_and_covers_fleet():
    assert worker_index("284", "284,283,211") == (2, 3)   # sorted ids: 211,283,284
    assert worker_index("211", "284,283,211") == (0, 3)
