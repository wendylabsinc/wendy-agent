const assert = require('node:assert/strict');
const test = require('node:test');
const { activeStableReleases, hasDeferredNightly } = require('./release-priority.cjs');

const context = {
  repo: { owner: 'wendylabsinc', repo: 'WendyOS' },
  runId: 42,
};

function release(id, changes = {}) {
  return {
    id,
    event: 'workflow_dispatch',
    status: 'queued',
    display_title: 'Stable release · main',
    ...changes,
  };
}

function mockGitHub(pagesByStatus) {
  const requests = [];
  const listWorkflowRuns = async params => {
    requests.push(params);
    return { data: {
      workflow_runs: pagesByStatus[params.status]?.[params.page - 1] ?? [],
    } };
  };
  return {
    requests,
    rest: { actions: { listWorkflowRuns } },
    async paginate(route, params) {
      assert.equal(route, listWorkflowRuns);
      assert.deepEqual(params, {
        ...context.repo,
        workflow_id: 'build.yml',
        event: 'workflow_dispatch',
        status: params.status,
        per_page: 100,
      });
      const results = [];
      let page = 1;
      while (true) {
        const response = await route({ ...params, page });
        const runs = response.data.workflow_runs;
        results.push(...runs);
        if (runs.length < params.per_page) return results;
        page += 1;
      }
    },
  };
}

test('checks every active status and excludes other events, titles and completed runs', async () => {
  const statuses = ['queued', 'in_progress', 'pending', 'waiting', 'requested'];
  const github = mockGitHub(Object.fromEntries(statuses.map((status, index) => [
    status,
    [[release(index + 1, { status })]],
  ])));
  const original = github.paginate;
  github.paginate = async (route, params) => [
    ...await original(route, params),
    release(50, { event: 'push' }),
    release(51, { display_title: 'Build · main' }),
    release(52, { status: 'completed' }),
    release(53, { display_title: undefined }),
    release(54, { display_title: 'Stable release candidate' }),
  ];

  const runs = await activeStableReleases({ github, context });

  assert.deepEqual(runs.map(run => run.id), [1, 2, 3, 4, 5]);
  assert.deepEqual(github.requests.map(request => request.status), statuses);
});

test('finds a release beyond the first page', async () => {
  const github = mockGitHub({
    queued: [
      Array.from({ length: 100 }, (_, index) => release(index + 100, {
        display_title: 'Build · main',
      })),
      [release(200)],
    ],
  });

  const runs = await activeStableReleases({ github, context });

  assert.deepEqual(runs.map(run => run.id), [200]);
  assert.deepEqual(github.requests.filter(request => request.status === 'queued')
    .map(request => request.page), [1, 2]);
});

test('treats legacy manual builds as releases, including the existing queued release', async () => {
  const github = mockGitHub({ queued: [[
    release(38387668924, { display_title: 'Build products and release' }),
    release(2, { display_title: 'Build products and release', event: 'push' }),
  ]] });

  const runs = await activeStableReleases({ github, context });

  assert.deepEqual(runs.map(run => run.id), [38387668924]);
});

test('excludes the caller even when its ID is represented as a string', async () => {
  const github = mockGitHub({ queued: [[
    release('42'),
    release(43),
  ]] });

  const runs = await activeStableReleases({ github, context });

  assert.deepEqual(runs.map(run => run.id), [43]);
});

test('deduplicates releases observed during a status change', async () => {
  const github = mockGitHub({
    queued: [[release(1)]],
    in_progress: [[release('1', { status: 'in_progress' })]],
  });

  const runs = await activeStableReleases({ github, context });

  assert.equal(runs.length, 1);
  assert.equal(runs[0].status, 'in_progress');
});

test('returns an empty list when no active release exists', async () => {
  const github = mockGitHub({});
  assert.deepEqual(await activeStableReleases({ github, context }), []);
});

test('propagates API failures so callers cannot admit background work', async () => {
  const github = mockGitHub({});
  const failure = new Error('GitHub API unavailable');
  github.paginate = async (_route, params) => {
    if (params.status === 'waiting') throw failure;
    return [];
  };

  await assert.rejects(activeStableReleases({ github, context }), failure);
});

function mockArtifacts(pages) {
  const requests = [];
  const listArtifactsForRepo = async params => {
    requests.push(params);
    return { data: { artifacts: pages[params.page - 1] ?? [] } };
  };
  return {
    requests,
    rest: { actions: { listArtifactsForRepo } },
    async paginate(route, params) {
      assert.equal(route, listArtifactsForRepo);
      assert.deepEqual(params, {
        ...context.repo,
        name: 'nightly-deferred-for-123',
        per_page: 100,
      });
      const results = [];
      let page = 1;
      while (true) {
        const response = await route({ ...params, page });
        const artifacts = response.data.artifacts;
        results.push(...artifacts);
        if (artifacts.length < params.per_page) return results;
        page += 1;
      }
    },
  };
}

test('allows a nightly retry only for an exact unexpired release marker', async () => {
  const github = mockArtifacts([[
    { name: 'nightly-deferred-for-123', expired: false },
  ]]);

  assert.equal(await hasDeferredNightly({ github, context, releaseId: 123 }), true);
});

test('does not retry without a marker, with expired markers or for another release', async () => {
  for (const artifacts of [
    [],
    [{ name: 'nightly-deferred-for-123', expired: true }],
    [{ name: 'nightly-deferred-for-1234', expired: false }],
  ]) {
    const github = mockArtifacts([artifacts]);
    assert.equal(await hasDeferredNightly({ github, context, releaseId: 123 }), false);
  }
});

test('finds a deferred nightly marker beyond the first artifact page', async () => {
  const github = mockArtifacts([
    Array.from({ length: 100 }, () => ({
      name: 'nightly-deferred-for-123', expired: true,
    })),
    [{ name: 'nightly-deferred-for-123', expired: false }],
  ]);

  assert.equal(await hasDeferredNightly({ github, context, releaseId: 123 }), true);
  assert.deepEqual(github.requests.map(request => request.page), [1, 2]);
});

test('fails the nightly retry check when the artifact API fails', async () => {
  const github = mockArtifacts([]);
  const failure = new Error('Artifact API unavailable');
  github.paginate = async () => { throw failure; };

  await assert.rejects(hasDeferredNightly({ github, context, releaseId: 123 }), failure);
});
