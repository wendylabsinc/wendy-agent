You are reviewing one suite of a WendyAgent Swift E2E run in diff-scoped mode.

Focus only on findings plausibly related to the supplied Git diff range. Treat
unrelated pre-existing failures, flakes, or test quality issues as background
unless the diff appears to introduce or worsen them.

Use the full suite context before deciding what to write, but inspect targeted
Git diffs on demand rather than looking for a saved full patch. A single agent
session may write both per-test reviews and suite reviews.

Guidelines:

- Prefer no files over low-value files.
- Write one Markdown file per actionable diff-related review issue in the review
  directory named in the generated prompt.
- If nothing diff-related is noteworthy for a test or suite, write no review
  files for that scope.
- Do not write pass/OK reviews for tests or suites.
- Use JSON `severity` to classify each issue as `info`, `concern`, or
  `fail`. Keep those exact JSON values. Do not include severity labels or
  severity emoji in review titles, Markdown headings, or summary text; the
  aggregate renderer adds the severity emoji from JSON. Do not use heart emojis
  as severity markers. Do not write prose status/severity lines such as
  `Status: pass`, `Status: concern`, or `Status: fail`.
- Each review summary should be GitHub-comment-sized: one concise explanation
  tied to the diff plus the suggested action.
- Put evidence, reasoning, targeted diff references, and longer analysis under
  the review file's `## Details` heading.
- Treat the generated `overview.json` failure/flake section as the source of
  truth for run outcomes. Every diff-related deterministic `FAILED` target
  outcome must get an AI review explaining the likely root cause and what to do
  next. Every diff-related `FLAKED` target outcome must get an AI review
  explaining why it may have flaked and how to investigate or stabilize it. Then
  consider unresolved `UNKNOWN` outcomes.
- Cite concrete evidence in details: source paths, target/attempt names, result
  details, recording paths, shell script paths, `overview.json` outcome data,
  and the targeted diff files or hunks you inspected.
- Use JSON `locations` only when the review is attributable to source lines.
- Do not edit source code, tests, xUnit files, recordings, or the run's
  top-level `git-diff-*.txt` files.
