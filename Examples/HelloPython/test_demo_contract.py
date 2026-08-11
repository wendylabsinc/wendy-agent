import json
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parent


class DemoContractTest(unittest.TestCase):
    def test_demo_advertises_html_entrypoint(self) -> None:
        manifest = json.loads(
            (PROJECT_ROOT / "wendy.json").read_text(encoding="utf-8")
        )
        metadata = json.loads(
            (PROJECT_ROOT / "wendy-demo.json").read_text(encoding="utf-8")
        )

        self.assertIn({"type": "http", "port": 8000}, manifest["entitlements"])
        self.assertEqual(metadata["safety"], "view")
        self.assertEqual(metadata["links"][0], {
            "label": "Open demo",
            "port": 8000,
            "path": "/",
            "kind": "ui",
        })


if __name__ == "__main__":
    unittest.main()
