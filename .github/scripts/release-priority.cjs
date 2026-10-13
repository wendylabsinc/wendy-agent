const ACTIVE_STATUSES = ['queued', 'in_progress', 'pending', 'waiting', 'requested'];
const RELEASE_TITLE_PREFIX = 'Stable release · ';
const LEGACY_BUILD_TITLE = 'Build products and release';

async function activeStableReleases({ github, context }) {
  const results = await Promise.all(ACTIVE_STATUSES.map(status =>
    github.paginate(github.rest.actions.listWorkflowRuns, {
      ...context.repo,
      workflow_id: 'build.yml',
      event: 'workflow_dispatch',
      status,
      per_page: 100,
    })
  ));

  const releases = new Map();
  for (const run of results.flat()) {
    if (String(run.id) === String(context.runId) ||
        run.event !== 'workflow_dispatch' ||
        !ACTIVE_STATUSES.includes(run.status)) {
      continue;
    }

    // Older manual builds did not expose their publish input in the title.
    // Treat them as releases until they finish rather than delaying a release.
    if (run.display_title?.startsWith(RELEASE_TITLE_PREFIX) ||
        run.display_title === LEGACY_BUILD_TITLE) {
      releases.set(String(run.id), run);
    }
  }

  // A run can change status between requests and appear in two result sets.
  return [...releases.values()];
}

async function hasDeferredNightly({ github, context, releaseId }) {
  const name = `nightly-deferred-for-${releaseId}`;
  const artifacts = await github.paginate(github.rest.actions.listArtifactsForRepo, {
    ...context.repo,
    name,
    per_page: 100,
  });
  return artifacts.some(artifact => artifact.name === name && !artifact.expired);
}

module.exports = { activeStableReleases, hasDeferredNightly };
