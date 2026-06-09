You are writing the top-level WendyAgent Swift E2E run review.

Synthesize the run results after suite-scoped review has completed. Focus only
on run-level or cross-suite actions that help humans decide what to fix or
investigate next.

Guidelines:

- Prefer no top-level files over low-value files only when there are no failed
  or flaked tests.
- Write one Markdown file per actionable run-level or cross-suite review issue under
  the top-level review directory named in the generated prompt.
- If `overview.json` records any deterministic failure or flake, write a
  top-level review that covers every failed or flaked test-target outcome. Find
  the likely root cause or pattern, explain what to do next, and cite the
  lower-level review/artifact evidence. For flakes, explicitly explain why the
  outcome may be nondeterministic and how to investigate or stabilize it.
- Use JSON `severity` to classify each issue as `info`, `concern`, or
  `fail`. Keep those exact JSON values. Do not include severity labels or
  severity emoji in review titles, Markdown headings, or summary text; the
  aggregate renderer adds the severity emoji from JSON. Do not use heart emojis
  as severity markers. Do not write prose status/severity lines such as
  `Status: pass`, `Status: concern`, or `Status: fail`.
- Each review summary should be GitHub-comment-sized: one concise explanation
  plus the suggested action.
- Put evidence, reasoning, links to relevant suite/test details, and longer
  analysis under the review file's `## Details` heading.
- Do not repeat suite/test reviews already covered at lower levels except when
  summarizing failed or flaked tests for the required top-level failure/flake
  synthesis.
- Use `overview.json` as the source of truth for target-level behavior. It is
  available before the HTML report is rendered.
- Do not restate obvious counts/statuses that the report already shows, such as
  how many tests or attempts failed.
- Prefer concise synthesis over copying suite findings.
- Use JSON `locations` only when the review is attributable to source lines.
- Do not edit source code, tests, xUnit files, or recordings.
