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

### Docs Review Workflow (`.github/workflows/docs-review.yml`)

The docs-review workflow runs on every pull request targeting `main` and posts a structured docs coverage review as a PR issue comment when Claude finds documentation gaps.

**Key behaviours:**

- **Structured output:** Claude returns either `NO_CHANGES_NEEDED` or JSON with up to 8 findings. Each finding includes severity, title, docs path, one-line overview, and detailed rationale or suggested content.
- **Severity sorting:** Findings are sorted blockers-first, then concerns, then info, with 🛑 Error, ⚠️ Concern, and 💡 Info labels in the comment.
- **Inline path summaries:** Each visible finding paragraph starts with the affected docs path, followed by a colon and the overview prose.
- **Collapsible details:** Longer rationale and suggested diffs are placed behind a workflow-owned `<details>` disclosure block.
- **Output safety:** LLM-provided text is length-limited, angle brackets are rendered as safe Unicode characters, detail text is placed in fenced code blocks, unsafe URI schemes are neutralised, and docs paths are accepted only when they match the strict existing `docs/.../*.md` allowlist.
- **Edit-in-place comment:** Existing docs-review comments are identified by the `<!-- ai-docs-review:v1 -->` marker and edited instead of posting duplicates. Duplicate marker comments are deleted.
- **Stale comment cleanup:** When no findings are produced, any existing docs-review comment is deleted.
- **Body truncation:** The prepared comment is capped at 65,000 characters, with space reserved so the identifying marker remains present after truncation.
- **GitHub API retries:** Comment list, create, update, and delete requests retry transient `429` and `5xx` responses before failing the step.

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
2. **Fetches prior review state** from the existing security-review comment identified by the `<!-- ai-security-review:v1 -->` marker.
3. **Invokes Claude** (`CLAUDE_MODEL`, currently `claude-opus-4-8`, `max_tokens=16000`) with a detailed system prompt covering security, compliance, prior review state, and explicit `SECURITY:` silencing comments.
4. **Renders a structured Markdown comment** from Claude's JSON response: one glanceable line per finding, with full details behind per-finding `<details>` disclosures.
5. **Posts or updates a PR comment** headed `# AI Security Review`. Existing comments are edited in place via the stable marker; duplicate security-review comments are deleted.
6. **Blocks merging only on open high-severity findings** — a final step exits non-zero only when an unsilenced `open` finding is `HIGH` or `CRITICAL`.

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

The comment is rendered from a JSON payload with:

| Field | Description |
|---|---|
| `summary` | One-line overview of the review |
| `findings[]` | Up to 10 security findings |
| `compliance_summary` | Short Markdown compliance summary |

Each finding includes severity, status, standards, title, path, line(s), visible overview, and detailed Markdown remediation. Valid statuses are `open`, `addressed`, `cancelled`, and `silenced`.

#### Stateful review and silencing

The previous AI Security Review comment is included as untrusted prior review state. Claude preserves still-relevant findings, marks fixed findings as `addressed`, marks no-longer-applicable findings as `cancelled`, and appends genuinely new findings as `open`.

A nearby developer comment containing `SECURITY: <reason>` explicitly accepts the risk. The renderer also checks the PR diff for nearby `SECURITY:` comments and marks matching open findings as `silenced`, keeping them visible but non-blocking.

#### Severity gate

The gate counts only findings whose status remains `open` and whose severity is `HIGH` or `CRITICAL`. Silenced, addressed, and cancelled findings do not fail the check. `MEDIUM`, `LOW`, and `INFORMATIONAL` findings are reported in the comment but do not fail the check.

#### Permissions

The job requests only `contents: read` and `pull-requests: write`. It only runs on PRs from within the same repository (`github.event.pull_request.head.repo.full_name == github.repository`), preventing untrusted forks from triggering the workflow.

#### Prompt injection mitigation

PR title, body, and diff are wrapped in `<untrusted_pr_content>` tags. Prior review state is wrapped in `<untrusted_previous_review>` tags, and malformed model output sent through the repair pass is wrapped in `<untrusted_model_response>` tags. The prompts explicitly instruct the model to ignore instructions embedded within those untrusted sections.

#### Required secrets

| Secret | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | Authenticates calls to the Anthropic API |

No additional secrets are required; the `GH_TOKEN` is provided automatically by GitHub Actions.
