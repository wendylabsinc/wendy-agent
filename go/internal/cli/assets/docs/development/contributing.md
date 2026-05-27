# Contributing to WendyOS

Thank you for contributing to WendyOS! This document covers the development workflows, CI pipelines, and guidelines for contributing to the project.

## CI / GitHub Actions

### Docs Update Workflow (`.github/workflows/docs-update.yml`)

The docs-update workflow automatically proposes documentation changes whenever a pull request is merged. It is driven by Claude (Anthropic) and reflects the changes in the PR diff.

**Key behaviours:**

- **Relevance-ranked doc selection:** Markdown files under `docs/` are scored by keyword overlap between their path/content and the PR title and diff. Files are ranked by relevance descending before the 50,000-character budget is applied, so the most pertinent documentation fills the context window rather than alphabetically-first files.
- **File-tree context:** A compact file tree (max 3 levels deep) is included in the prompt so the model can reason about the overall documentation structure.
- **Larger context window:** `max_tokens` is set to `16,000`.
- **Diff formatting:** The raw diff is wrapped in a fenced ` ```diff ``` ` code block in the prompt, truncated at 30,000 characters.
- **Docs truncation limit:** The current-docs context limit is 50,000 characters, presented to the model as "most relevant first".
- **Source repo context:** The name of the source repository is included in the prompt.
- **Path safety:** The workflow uses `Path.relative_to()` (raising `ValueError` on traversal) and rejects any output path whose suffix is not `.md`.
- **Protected files:** `THREAT_MODEL.md` and `threat-model.md` are never read as context and never written by this workflow. Threat model files are managed by a dedicated security workflow.
- **Directory creation:** Output directories are created automatically (`mkdir -p`) so the model can introduce new documentation pages in new subdirectories.
- **Created vs. Updated logging:** The workflow prints `Created:` for new files and `Updated:` for existing files.
- **Empty-diff guard:** If the diff is empty, the workflow exits early rather than attempting a model call.
- **Prompt wording:** The system prompt instructs the model to make only minimal, surgical edits to keep docs accurate — updating only content that is directly wrong or missing as a result of the diff, writing in timeless present tense, and never inventing or removing features not present in the diff.

### Security Review Workflow (`.github/workflows/security-review.yml`)

A dedicated **AI Security Review** workflow runs on every pull request targeting `main` and posts a structured security and compliance report as a PR comment.

#### Trigger

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened]
    branches: [main]
```

Concurrent runs for the same PR are cancelled automatically (`cancel-in-progress: true`).

#### What it does

1. **Fetches the PR diff** using the GitHub CLI (`gh pr diff`).
2. **Invokes Claude** (`claude-sonnet-4-6`, `max_tokens=8096`) with a detailed system prompt covering security and compliance analysis.
3. **Writes a Markdown report** (`review.md`) to the workspace.
4. **Parses the report for severity findings** — the findings table is scanned for rows with `CRITICAL` or `HIGH` severity. When any are found, `HAS_HIGH_SEVERITY=true` is written to `GITHUB_ENV`.
5. **Posts or updates a PR comment** headed `## AI Security Review`. The comment uses a `<details>`/`<summary>` block: the first non-heading paragraph of the report appears as the collapsed summary, and the full report is shown when expanded. If a previous security review comment already exists on the PR it is edited in place; otherwise a new comment is created. The comment is always posted, even when the check subsequently fails.
6. **Blocks merging on high-severity findings** — a final step exits non-zero if `HAS_HIGH_SEVERITY` is `true`, causing the check to fail and preventing the PR from merging until the findings are addressed.

#### Security analysis scope

The model analyses the diff for:

- Injection vulnerabilities (SQL, command, LDAP, XPath, template, etc.)
- Authentication and authorisation flaws
- Sensitive data exposure (secrets, PII, tokens hardcoded or logged)
- Cryptographic weaknesses
- Input validation and output encoding issues
- Security misconfiguration (overly broad permissions, debug flags, unsafe defaults)
- Insecure dependencies or version pins
- Race conditions and TOCTOU vulnerabilities
- Path traversal and SSRF
- Denial-of-service vectors (unbounded loops, large allocations, missing rate limits)

#### Compliance frameworks checked

| Framework | Controls evaluated |
|---|---|
| **SOC 2** | CC6, CC7, CC8, CC9, A1, C1, P-series |
| **ISO/IEC 27001:2022** | A.8 (technological controls), A.9 (access control), A.12 (logging & monitoring) |
| **PCI DSS v4.0** | Req 3, 4, 6, 10 — flagged only when the diff touches payment/card data |
| **GDPR / Privacy** | Lawful basis, PII logging, data-subject rights |
| **HIPAA** | PHI encryption, access controls — flagged only when the diff touches health data |
| **NIST SP 800-53 / CSF 2.0** | AC, AU, IA, SC, SI control families |

Each finding is rated **CRITICAL**, **HIGH**, **MEDIUM**, **LOW**, or **INFORMATIONAL** and tagged with the relevant standard(s) (e.g. `[SOC2-CC6] [ISO27001-A.8]`).

#### Report structure

1. One-paragraph executive summary
2. Findings table: `| Severity | Standards | File | Line(s) | Title |`
3. Detailed section per finding with description, quoted snippet, remediation advice, and controls violated
4. Compliance summary listing which frameworks were checked and whether violations were found
5. Explicit "no findings" statement if the diff looks safe

#### Severity gate

After the report is posted, the workflow parses the findings table for `CRITICAL` or `HIGH` severity rows. If any are found, the workflow exits non-zero, blocking the PR from merging. `MEDIUM`, `LOW`, and `INFORMATIONAL` findings are reported in the comment but do not fail the check.

#### Permissions

The job requests only `contents: read` and `pull-requests: write`. It only runs on PRs from within the same repository (`github.event.pull_request.head.repo.full_name == github.repository`), preventing untrusted forks from triggering the workflow.

#### Prompt injection mitigation

PR title, body, and diff are wrapped in `<untrusted_pr_content>` tags, and the system prompt explicitly instructs the model to ignore any instructions embedded within that content.

#### Required secrets

| Secret | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | Authenticates calls to the Anthropic API |

No additional secrets are required; the `GH_TOKEN` is provided automatically by GitHub Actions.
