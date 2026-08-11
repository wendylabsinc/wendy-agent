from fruit_ninja import server


def test_server_contract_uses_dedicated_demo_port():
    assert server.PORT == 8878
    assert server.STATIC_ROOT.joinpath("index.html").is_file()


def test_ui_exposes_demo_evidence_and_boundary():
    page = server.STATIC_ROOT.joinpath("index.html").read_text(encoding="utf-8")
    assert "Real MuJoCo contacts" in page
    assert "Simulation only" in page
    assert "/stream.mjpg" in page
    assert "/api/status" in page
