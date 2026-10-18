#!/usr/bin/env python3
import copy
import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location("results", Path(__file__).with_name("integration-results.py"))
results = importlib.util.module_from_spec(spec)
spec.loader.exec_module(results)
SHA, OS_SHA, DIGEST = "a" * 40, "b" * 40, "c" * 64


def result(host="fixture.local", platform="Linux", family="generic-linux-x86", major=1, version=""):
    return {"hostname": host, "host_platform": platform, "family": family,
            "containerd_major": major, "source_sha": SHA, "candidate_version": version,
            "device_info": {"binarySha256": DIGEST},
            "candidate_kind": "agent", "passed": True,
            "candidate": {"device": host, "sourceCommit": SHA, "verified": True,
                          "postSuiteVerified": True, "candidateSha256": DIGEST,
                          "runningSha256": DIGEST, "version": version or f"ci-{SHA}"}}


def os_result(host="jetson.local", platform="Linux", family="jetson", major=2):
    value = result(host, platform, family, major)
    value.update({"candidate_kind": "os", "os_candidate_version": "os-candidate",
                  "os_candidate_sha": OS_SHA, "candidate": {
                      "device": host, "status": "verified", "mode": "candidate-os",
                      "postSuiteVerified": True, "builder_commit": OS_SHA,
                      "build_version": "os-candidate", "agent_sha256": DIGEST,
                      "ota_sha256": "d" * 64}})
    return value


class ResultTests(unittest.TestCase):
    def test_requires_every_expected_host_and_runner(self):
        rows = [result(platform=platform) for platform in ("Linux", "macOS")]
        self.assertEqual(results.verify(rows, ["fixture.local"], ["Linux", "macOS"], SHA, False), 2)
        for broken in ([], rows[:1], rows + rows[:1], [result(host="unexpected.local")]):
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                results.verify(broken, ["fixture.local"], ["Linux", "macOS"], SHA, False)
        with self.assertRaises(ValueError):
            results.verify([], [], ["Linux"], SHA, False)

    def test_rejects_failed_stale_and_unverified_agent_results(self):
        mutations = [lambda row: row.update(passed=False),
                     lambda row: row.update(source_sha="e" * 40),
                     lambda row: row.update(candidate={}),
                     lambda row: row["candidate"].update(device="another.local"),
                     lambda row: row["candidate"].update(sourceCommit="e" * 40),
                     lambda row: row["candidate"].update(verified=False),
                     lambda row: row["candidate"].update(postSuiteVerified=False),
                     lambda row: row["candidate"].update(runningSha256="d" * 64),
                     lambda row: row["candidate"].update(candidateSha256="invalid", runningSha256="invalid"),
                     lambda row: row["candidate"].update(version="stale"),
                     lambda row: row["device_info"].update(binarySha256="e" * 64),
                     lambda row: row.update(device_info={}),
                     lambda row: row.update(containerd_major=True)]
        for mutate in mutations:
            row = result()
            mutate(row)
            with self.subTest(row=row), self.assertRaises(ValueError):
                results.verify([row], ["fixture.local"], ["Linux"], SHA, False)

    def test_release_version_is_bound_in_outer_and_nested_evidence(self):
        row = result(version="release-candidate")
        self.assertEqual(results.verify([row], ["fixture.local"], ["Linux"], SHA, False,
                                        candidate_version="release-candidate"), 1)
        for field in ("outer", "nested"):
            broken = copy.deepcopy(row)
            if field == "outer":
                broken["candidate_version"] = "wrong"
            else:
                broken["candidate"]["version"] = "wrong"
            with self.subTest(field=field), self.assertRaises(ValueError):
                results.verify([broken], ["fixture.local"], ["Linux"], SHA, False,
                               candidate_version="release-candidate")

    def test_required_platforms_and_actual_server_majors(self):
        rows = [result(f"{family}.local", family=family, major=1 if index == 0 else 2)
                for index, family in enumerate(sorted(results.REQUIRED))]
        hosts = [row["hostname"] for row in rows]
        self.assertEqual(results.verify(rows, hosts, ["Linux"], SHA, True), 4)
        for broken in ([row for row in rows if row["family"] != "generic-linux-x86"],
                       [dict(row, containerd_major=2) for row in rows],
                       [dict(row, containerd_major="1") for row in rows]):
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                results.verify(broken, [row["hostname"] for row in broken], ["Linux"], SHA, True)

    def test_os_provenance_requires_exact_image_and_running_bundled_agent(self):
        row = os_result()
        self.assertEqual(results.verify([row], ["jetson.local"], ["Linux"], SHA, False,
                                        "os-candidate", OS_SHA), 1)
        for key, value in (("builder_commit", "e" * 40), ("build_version", "old"),
                           ("mode", "linux-runtime-baseline"), ("status", "failed"),
                           ("agent_sha256", ""), ("ota_sha256", "bad"),
                           ("postSuiteVerified", False)):
            broken = copy.deepcopy(row)
            broken["candidate"][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                results.verify([broken], ["jetson.local"], ["Linux"], SHA, False,
                               "os-candidate", OS_SHA)
        for key in ("os_candidate_version", "os_candidate_sha"):
            broken = copy.deepcopy(row)
            broken[key] = "wrong"
            with self.subTest(key=key), self.assertRaises(ValueError):
                results.verify([broken], ["jetson.local"], ["Linux"], SHA, False,
                               "os-candidate", OS_SHA)

    def test_os_gate_requires_real_os_evidence_for_all_physical_families(self):
        rows = [os_result(f"{family}.local", family=family)
                for family in ("raspberry-pi", "jetson", "qualcomm")]
        baseline = result("ubuntu.local", major=1)
        baseline.update(os_candidate_version="os-candidate", os_candidate_sha=OS_SHA)
        rows.append(baseline)
        hosts = [row["hostname"] for row in rows]
        self.assertEqual(results.verify(rows, hosts, ["Linux"], SHA, True, "os-candidate", OS_SHA), 4)
        wrong_kind = result(rows[0]["hostname"], family="raspberry-pi", major=2)
        wrong_kind.update(os_candidate_version="os-candidate", os_candidate_sha=OS_SHA)
        with self.assertRaisesRegex(ValueError, "all required physical families"):
            results.verify([wrong_kind, *rows[1:]], hosts, ["Linux"], SHA, True, "os-candidate", OS_SHA)


if __name__ == "__main__":
    unittest.main()
