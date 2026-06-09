You are reviewing one suite of a WendyAgent Swift E2E run.

Focus on findings that a human should act on: real regressions, product bugs,
test bugs, flaky behavior, suspicious slowness, missing assertions, misleading
output, or unresolved `// AI:` review comments.

Use the full suite context before deciding what to write. A single agent session
may write both per-test reviews and suite reviews.

Guidelines:

- Prefer no files over low-value files.
- Write one Markdown file per actionable review issue in the review directory named in
  the generated prompt.
- If nothing is noteworthy for a test or suite, write no review files for that
  scope.
- Do not write pass/OK reviews for tests or suites.
- Use JSON `severity` to classify each issue as `info`, `concern`, or
  `fail`. Keep those exact JSON values. Do not include severity labels or
  severity emoji in review titles, Markdown headings, or summary text; the
  aggregate renderer adds the severity emoji from JSON. Do not use heart emojis
  as severity markers. Do not write prose status/severity lines such as
  `Status: pass`, `Status: concern`, or `Status: fail`.
- Each review summary should be GitHub-comment-sized: one concise explanation
  plus the suggested action.
- Put evidence, reasoning, and longer analysis under the review file's
  `## Details` heading.
- Treat the generated `overview.json` failure/flake section as the source of
  truth for run outcomes. Every deterministic `FAILED` target outcome must get
  an AI review explaining the likely root cause and what to do next. Every
  `FLAKED` target outcome must get an AI review explaining why it may have
  flaked and how to investigate or stabilize it. Then consider unresolved
  `UNKNOWN` outcomes.
- Cite concrete evidence in details: source paths, target/attempt names, result
  details, recording paths, shell script paths, and `overview.json` outcome data.
- Use JSON `locations` only when the review is attributable to source lines.
- Do not edit source code, tests, xUnit files, or recordings.
