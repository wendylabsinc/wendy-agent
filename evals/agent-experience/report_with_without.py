#!/usr/bin/env python3
"""Render a transparent, task-scoped report from preserved paired attempts."""
import argparse
import html
import itertools
import json
from pathlib import Path
import random
import statistics

import with_without as w


def pair_statistics(rows, task, uncached=False):
    pairs = {}
    for r in rows:
        if r["task"] != task or not r["success"] or not r.get("metrics", {}).get("usage_complete"):
            continue
        u = r["metrics"]["tokens"]
        value = u["input_tokens"] + u["output_tokens"]
        if uncached:
            value -= u["cached_input_tokens"]
        pairs.setdefault(r["pair"], {})[r["arm"]] = value
    values = [(p["without"], p["with"]) for p in pairs.values() if len(p) == 2]
    if not values:
        return {"complete_successful_pairs": 0}
    n = len(values)
    effect = lambda v: 100 * (1 - sum(z for a, z in v) / sum(a for a, z in v))
    rng = random.Random(20261016)
    boots = sorted(effect(rng.choices(values, k=n)) for _ in range(20000))
    observed = abs(sum(a-z for a, z in values))
    null = [abs(sum(sign * (a-z) for sign, (a, z) in zip(signs, values)))
            for signs in itertools.product((-1, 1), repeat=n)]
    return {"complete_successful_pairs": n,
            "reduction_pct": effect(values),
            "bootstrap_95_percentile_interval": [boots[499], boots[19499]],
            "paired_sign_flip_two_sided_p": sum(x >= observed for x in null) / len(null),
            "wendy_fewer_token_pairs": sum(z < a for a, z in values),
            "pair_values_without_with": values}


def render(root):
    measured = root / "measured"
    rows = [json.loads(s) for s in (measured / "results.jsonl").read_text().splitlines()]
    summary = w.summarize(rows)
    manifest = json.loads((measured / "manifest.json").read_text())
    stats = {s["task"]: {"total": pair_statistics(rows, s["task"]),
                        "uncached_plus_output": pair_statistics(rows, s["task"], True)} for s in summary}
    w.b.dump(root / "comparison.json", {"summary": summary, "paired_statistics": stats})
    esc = html.escape
    parts = ['<!doctype html><meta charset="utf-8"><title>G1 replay deployment: Wendy CLI versus Docker</title>',
        '<style>body{font:16px/1.6 system-ui;max-width:1100px;margin:50px auto;padding:0 24px;color:#182132}table{border-collapse:collapse;width:100%;font-size:14px}td,th{padding:9px 12px;border:1px solid #d6dee8;text-align:left}th{background:#edf2f7}code{background:#edf2f7;padding:2px 5px}h1{font-size:30px}.scope{background:#edf2f7;padding:16px;border-left:4px solid #405bd8}</style>',
        '<h1>G1 replay deployment: Codex + Wendy CLI versus Codex + Docker</h1>',
        '<p>Measured 16 October 2026. Task-specific, repeated matched experiment; no physical hardware or robot motion.</p>',
        '<p class="scope"><b>Scope:</b> Wendy CLI’s local Docker provider versus ordinary Docker tooling. Containerize and deploy a supplied headless G1 MuJoCo recorded-expert replay service, and diagnose/repair the same seeded startup configuration fault. Both approaches target the same already-running ARM64 Linux Docker VM with the same preinstalled dependency image. This does not measure RL training, learned-policy inference, vendor OS provisioning, remote Jetson deployment, Wendy Agent/MCP integration, or physical robot outcomes.</p>',
        '<h2>Measured results</h2><p>Tokens per verified success include usage from failed attempts. Total tokens = input + output; cached input is already included in input. Uncached input + output is shown separately and is not a dollar cost.</p>',
        '<table><tr><th>Task</th><th>Approach</th><th>Verified</th><th>Tokens / success</th><th>Uncached + output / success</th><th>Median successful seconds</th></tr>']
    for s in summary:
        for arm in ("without", "with"):
            a = s["arms"][arm]
            fmt = lambda v: f"{v:,.1f}" if v is not None else "unknown"
            parts.append(f'<tr><td>{s["task"]}</td><td>{arm} Wendy</td><td>{a["successes"]}/{a["attempts"]}</td><td>{fmt(a["tokens_per_verified_success"])}</td><td>{fmt(a["uncached_plus_output_per_verified_success"])}</td><td>{fmt(a["median_success_seconds"])}</td></tr>')
    parts += ['</table><h2>Paired uncertainty</h2>',
              '<p>Positive reduction means Wendy used fewer tokens; negative means more. Intervals resample complete successful pairs, and describe only this small workload cohort. The exact two-sided sign-flip test assesses a zero paired effect under exchangeability. Five pairs have low power and cannot establish broad product performance. Failure rates must be considered alongside paired successful comparisons. These uncertainty calculations are exploratory analyses added during collection, not preregistered confirmatory tests; the primary token endpoint, tasks, pair order and attempt limit were fixed before measured calls.</p>']
    parts.append('<table><tr><th>Task</th><th>Metric</th><th>Pairs</th><th>Wendy token reduction</th><th>Exploratory 95% interval</th><th>Exact paired p</th></tr>')
    for task, groups in stats.items():
        for name, st in groups.items():
            interval = st.get('bootstrap_95_percentile_interval')
            interval_text = f'{interval[0]:+.1f}% to {interval[1]:+.1f}%' if interval else 'unknown'
            effect = f'{st["reduction_pct"]:+.1f}%' if st.get('reduction_pct') is not None else 'unknown'
            parts.append('<tr>' + ''.join('<td>' + esc(str(v)) + '</td>' for v in (
                task, name, st['complete_successful_pairs'], effect, interval_text,
                st.get('paired_sign_flip_two_sided_p', 'unknown'))) + '</tr>')
    parts.append('</table>')
    parts += ['<h2>Controls and independent checks</h2><ul>',
        '<li>Same Codex CLI, requested and transcript-confirmed gpt-6-astra model, medium reasoning, shell permissions, task source, assets, dependency image and acceptance contract.</li>',
        '<li>Fresh workspace, app/image identity, Codex profile, Docker config and Wendy config each attempt; no follow-up messages or approvals. Randomized order inside each pair, serialized execution.</li>',
        '<li>Baseline: standard Docker tooling, no Wendy skills or MCP. Treatment: official released Wendy CLI 2026.10.14-064751 and two pinned Wendy skills; Docker inspection remains available. Neither approach uses MCP.</li>',
        '<li>The raw bootstrap transcript confirms the baseline has no Wendy skills and the treatment has exactly wendy and wendy-app-lifecycle. Both retain the same four built-in Codex system skills. Model, reasoning and approval settings are checked in each transcript.</li>',
        '<li>Independent verifier checks three fresh HTTP samples: correct nonce/version and asset hashes, 43 finite joints, changing joint positions, advancing simulation clock/ticks; also checks the running container and SHA-256 of actual deployed source/config/assets.</li>',
        '<li>Source/assets are immutable; deployment/config files may be authored. Each repair starts with the same reproduced missing-rollout error. The verifier never repairs the app.</li>',
        '<li>Dependency installation, benchmark development, fixture setup, plumbing tests and grading are excluded from measured agent tokens. Reported time is agent completion time. Build caches are shared and warm; provider-side prompt cache is observed, not reset.</li>',
        '<li>All attempts are retained. Missing usage is unknown; it never becomes zero. Cleanup checks remove only each owned app and compare the unrelated container inventory.</li></ul>',
        '<h2>Every measured attempt</h2><table><tr><th>Task</th><th>Pair</th><th>Approach</th><th>Result</th><th>Total tokens</th><th>Cached input</th><th>Output</th><th>Seconds</th><th>Tools</th><th>Isolation</th><th>Cleanup</th></tr>']
    for r in rows:
        u = r.get("metrics", {}).get("tokens") or {}
        total = u.get("input_tokens", 0) + u.get("output_tokens", 0) if u else "unknown"
        parts.append('<tr>' + ''.join('<td>' + esc(str(v)) + '</td>' for v in (
            r['task'], r['pair'], r['arm'], r['status'], total, u.get('cached_input_tokens'),
            u.get('output_tokens'), r.get('agent',{}).get('seconds'), r.get('metrics',{}).get('tool_calls'),
            r.get('context_audit',{}).get('isolation_passed'), r['cleanup_ok'])) + '</tr>')
    parts += ['</table><h2>Evidence and reproducibility</h2>',
        '<p>See the <a href="protocol.json">pre-run protocol</a>, <a href="measured/manifest.json">manifest</a>, <a href="comparison.json">numeric comparison</a>, <a href="measured/results.jsonl">every attempt</a>, <a href="audit.json">independent evidence audit</a>, and <a href="tests.txt">31 automated test results</a>. Every attempt directory contains raw agent events, its bootstrap transcript, prompt, authored manifests, deployed-file hashes, HTTP samples, logs, and cleanup evidence. The experiment source is under source/. Authentication files are excluded.</p>',
        '<p>Additional provenance: <a href="environment.json">runtime environment</a>, <a href="asset-provenance.json">supplied assets</a>, <a href="release-provenance.json">official CLI release and digest</a>, <a href="implementation.patch">implementation patch</a>, and <a href="patch-check.json">clean patch applicability check</a>.</p>',
        '<p>The protocol was fixed before measured calls. Isolation was validated with separate preflight calls after detecting and disabling inherited host skills. Those preflights and the deterministic fixture/CLI checks are retained separately under plumbing*, calibration/, and dependency-image/.</p>',
        '<p>Dependency image ID: <code>' + esc(json.loads(manifest['dependency_image']['stdout'])[0]['Id']) + '</code></p>',
        '<p>Runner SHA-256: <code>' + manifest['runner_sha256'] + '</code>; source commit: <code>' + manifest['source_commit'] + '</code>.</p>']
    (root / "report.html").write_text('\n'.join(parts))
    return {"summary": summary, "paired_statistics": stats}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    print(json.dumps(render(args.directory), indent=2))
