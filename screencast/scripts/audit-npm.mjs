#!/usr/bin/env node
import { spawnSync } from 'node:child_process';

const allowedAdvisoryURLs = new Set([
  // Slidev 52.16.0 currently pulls these low/moderate advisories through
  // local editor/Markdown tooling. CI still runs `npm audit --audit-level=high`
  // before this policy check, so high/critical advisories remain hard failures.
  'https://github.com/advisories/GHSA-v2wj-7wpq-c8vv',
  'https://github.com/advisories/GHSA-cjmm-f4jc-qw8r',
  'https://github.com/advisories/GHSA-cj63-jhhr-wcxv',
  'https://github.com/advisories/GHSA-39q2-94rc-95cp',
  'https://github.com/advisories/GHSA-h7mw-gpvr-xq4m',
  'https://github.com/advisories/GHSA-crv5-9vww-q3g8',
  'https://github.com/advisories/GHSA-v9jr-rg53-9pgp',
  'https://github.com/advisories/GHSA-h8r8-wccr-v5f2',
  'https://github.com/advisories/GHSA-x4vx-rjvf-j5p4',
  'https://github.com/advisories/GHSA-76mc-f452-cxcm',
  'https://github.com/advisories/GHSA-hpcv-96wg-7vj8',
  'https://github.com/advisories/GHSA-r47g-fvhr-h676',
  'https://github.com/advisories/GHSA-vxr8-fq34-vvx9',
  'https://github.com/advisories/GHSA-gvmj-g25r-r7wr',
  'https://github.com/advisories/GHSA-rp9w-3fw7-7cwq',
  'https://github.com/advisories/GHSA-cmwh-pvxp-8882',
  'https://github.com/advisories/GHSA-h67p-54hq-rp68',
]);

const result = spawnSync('npm', ['audit', '--json', '--audit-level=moderate'], {
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'pipe'],
});

let report;
try {
  report = JSON.parse(result.stdout || '{}');
} catch (error) {
  process.stderr.write(result.stderr || result.stdout || String(error));
  process.exit(result.status || 1);
}

const vulnerabilities = report.vulnerabilities ?? {};

function advisoriesFor(packageName, seen = new Set()) {
  if (seen.has(packageName)) return [];
  seen.add(packageName);

  const item = vulnerabilities[packageName];
  if (!item) return [];

  return item.via.flatMap((via) => {
    if (typeof via === 'string') return advisoriesFor(via, seen);
    return [{
      packageName,
      source: via.source,
      url: via.url,
      severity: via.severity,
      title: via.title,
    }];
  });
}

const allFindings = Object.keys(vulnerabilities).flatMap((name) => advisoriesFor(name));
const uniqueFindings = Array.from(
  new Map(allFindings.map((finding) => [finding.url ?? String(finding.source), finding])).values(),
);

const blocking = uniqueFindings.filter((finding) => {
  if (finding.severity === 'critical' || finding.severity === 'high') return true;
  return !allowedAdvisoryURLs.has(finding.url);
});

if (blocking.length > 0) {
  console.error('blocking npm audit findings:');
  for (const finding of blocking) {
    console.error(`- ${finding.severity}: ${finding.url ?? finding.source} ${finding.title ?? ''}`.trim());
  }
  process.exit(1);
}

const untraceable = Object.entries(vulnerabilities)
  .filter(([name]) => advisoriesFor(name).length === 0)
  .map(([name]) => name);
if (untraceable.length > 0) {
  console.error(`blocking npm audit findings without advisory URLs: ${untraceable.join(', ')}`);
  process.exit(1);
}

const summary = report.metadata?.vulnerabilities ?? {};
console.log(`npm audit accepted: ${JSON.stringify(summary)}`);
if (uniqueFindings.length > 0) {
  console.log(`allowed advisory URLs: ${uniqueFindings.map((finding) => finding.url).sort().join(', ')}`);
}
