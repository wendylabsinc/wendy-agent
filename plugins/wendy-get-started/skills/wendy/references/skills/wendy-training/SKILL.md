---
name: wendy-training
description: Inspect compute capacity, submit authorized durable host training jobs, follow bounded progress, and retrieve verified reports through Wendy CLI or MCP. Use for existing RL and training workflows on Spark or Linux cloud devices.
---

# Wendy Training

Use the exact authorized device selector. Preserve existing jobs, checkpoints,
optimizer/RNG state and unrelated work. A receipt, live PID or idle GPU does not
establish training success or physical robot behavior.

For a focused agent catalog, configure `wendy mcp serve --device <selector>
--tool-profile training`. This changes discovery only; each tool retains its
permissions. The default catalog remains available for general device work.

1. Call `wendy_status`; connect to the selected target if needed.
2. Use `compute_status` (`wendy device compute-status`) for GPU identity,
   current compute processes and host capacity. Missing tools stay unknown;
   capacity is sampled, not reserved. Do not launch onto a competing workload.
3. For an existing job, use `job_inspect(job_path=...)`. Its directory contains
   `status.json`; direct children can contain `metrics.jsonl`. The probe checks
   OS PID/command/start-time identity separately from recorded state and reads
   the last complete progress record. Use these observations directly; repeat
   raw reads only for missing/contradictory evidence or an independent check.
4. Save its cursor. Use `since` and `wait_seconds` (up to 30) to wait for changed
   progress; unchanged replies are small. Recorded state alone is not liveness.
5. Use `job_artifacts_pull(job_path=..., files=[...], destination=...)` for
   selected relative reports. It returns a verified local manifest and skips
   files whose hashes match. Bytes stay out of agent context. Maximum 64 files,
   16 MiB transferred; request fewer files when needed. Destination is absolute.

For a new job explicitly requested by the user, call
`job_submit(spec_path="/absolute/local/spec.json", wait_seconds=30)`.
The file is local to the MCP server; its working directory and argv refer to
the connected remote host. Prefer this reference over reading and copying the
JSON through model context. Inline `spec=...` is also supported; supply exactly
one. The optional wait returns terminal evidence or a current snapshot after
at most 30 seconds. Expiry never cancels the job. Inspect ongoing jobs afterward;
an observation error preserves the confirmed receipt and leaves completion unknown.
For short jobs, pass `artifact_files=["status.json", "evaluate/result.json"]`
and an absolute local `artifact_destination` in that same call. Reports are
collected only after a terminal observation, with the same limits and digest
checks as `job_artifacts_pull`. A still-running job returns `artifacts_pending`;
an `artifact_error` preserves job identity and does not imply training failed.
An identical retry reuses the job and skips unchanged reports. This avoids
separate model turns for submission, polling and collection. Use explicit
artifact retrieval for ongoing jobs and larger selected report sets.
The spec has `name`, absolute `working_directory`, and 1–32 `steps`. Each step
has a unique `name`, `argv` and `timeout_seconds` (1–86400). Optional
`source_hashes` verify relative source/checkpoint files before launch. Use the
existing project interpreter/venv; no implicit installation is performed.

Job metadata and logs are stored under `<working_directory>/.wendy/jobs/<name>`.
Commands run with host-agent permissions and may write elsewhere: direct outputs
to `{job_dir}` / `{step_dir}`, or use `WENDY_JOB_DIR` / `WENDY_STEP_DIR`.
Steps must run in the foreground; daemonizing workloads belong in a managed app.
Identical name/spec retries return the original job; use a new name to rerun.
Inspect the returned path and verify outcomes. Submission is not completion.
Never submit physical robot actions without specific user authorization.

CLI equivalents: `wendy job inspect <path> --since <cursor> --wait 30`,
`wendy job artifacts pull <path> --file status.json --output <local-dir>`,
and `wendy job submit spec.json --wait 30`. Use the global `--device` selector.
Add `--artifact status.json --artifact evaluate/result.json --output /local/reports`
to collect selected reports in the submission command after terminal evidence.
