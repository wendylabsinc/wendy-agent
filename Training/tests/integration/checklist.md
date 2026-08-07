# Hardware verification record, 2026-08-06

Three Sparks, all discoverable on the local network at the time of the run:
spark-3011.local (asset 334), spark-48fd.local (asset 211), spark-edeb.local
(asset 283). Every device was carrying live workloads throughout (a Claude
container and a YOLO run on 3011, a wakeword trainer on 48fd, a YOLO run on
edeb); nothing pre-existing was stopped, removed, or restarted.

## Proof 1: kill and resume (spark-edeb)

The `single` template, deployed with one `fleet.py up`. The run was stopped at
iteration 4580 of 6000 with `wendy` container stop; the restart's first log
line was:

```
[single] resumed iteration=4580 adam_t=4580
```

The Adam step count equals the iteration, so optimizer state survived, not
just weights. The run then continued to completion:

```
[single] finished: {"run_id": "stage-d-resume4", "iterations": 6000,
"final_mean_return": 499.95, "best_mean_return": 499.99}
```

`manifest.json` and `policy.wtw` were pulled off the device volume and
`wendytrain.manifest.verify_manifest` passed locally; the policy blob decodes
with `architecture: [32]` in its wire metadata.

Observation for future work: a run-to-completion container under the app
group restart policy restart-loops after finishing (each restart resumes at
the final iteration, prints `finished`, and exits). Harmless because resume
is cheap and correct, but noisy; `fleet.py down` after completion, or a
run-once policy at the platform level, would be cleaner.

## Proof 2: three-device es-fleet (lan transport)

Final coordinator status, spark-48fd:

```
{"generation": 40, "population": 24, "n_contributed": 24,
 "mean_return": 273.7, "best_return": 499.99, "stale_posts": 0, "done": true}
```

`n_contributed` 24 of 24 arithmetically requires all three devices: the
coordinator's loopback worker owns pairs 0 to 7, the workers own 8 to 15 and
16 to 23. Forty generations completed in well under a minute once all three
contributed; the loopback-only failure mode below ran at one generation per
45 second timeout. A coordinator restart after completion printed
`resumed generation=40 adam_t=40`, so fleet-level resume holds too.

## Proof 3: three-seed sweep across three devices

`collect.py` gathered three distinct converged runs, sorted
(`results/sweep-2026-08-06.json`): seeds 22, 11, 33 finishing at mean returns
499.99, 498.29, 496.85 on 48fd, 3011, and edeb respectively. Zero unreachable.

## Non-interference

`container list` before and after on each device: the only differences are
stopped `sh.wendy.training.*` containers from these tests. Two temporary
helper apps (a HelloMesh canary and a checkpoint file server) were fully
deleted. All long-running workloads stayed running.

## Findings, in the order the hardware surfaced them

1. **Mesh overlay is currently broken on this fleet** (not fixed here; needs
   the agent owners). Containers cannot resolve `device-<id>.cloud.wendy.dev`
   ("Temporary failure in name resolution"); an earlier canary run saw dials
   that connected and closed without a response. The devices run mixed agent
   builds (3011/48fd on release 2026.07.27-003050, edeb on dev) and the
   LAN-registry code changed on main the same week. The `lan` transport
   exists so training is not hostage to overlay state; mesh remains the
   default and should be retested after the fleet's agents converge.
2. **The launcher emitted no topology for hostname fleets.** Caught by
   `render` before any deploy: lan peers are hostnames, and numeric
   derivation rightly refuses to guess. Fixed with the generic
   `WT_COORDINATOR` / `WT_NODE_INDEX` / `WT_NODE_COUNT` trio; regression
   tests on the launcher and both fleet templates.
3. **The host firewall rejects loopback dials to published ports.** The
   coordinator's own worker got "No route to host" for 127.0.0.1:8080 while
   remote machines could connect, so every generation advanced with zero
   contributions. The loopback worker now calls the coordinator's handlers
   in-process; the regression test breaks HTTP entirely and requires a
   fleet of one to contribute in full. Silent empty generations also
   motivated the per-generation stdout line.
4. **Containers cannot resolve `.local` names.** After the loopback fix the
   coordinator's slice arrived but remote workers logged "Name or service
   not known" for the coordinator's multicast hostname. The launcher now
   resolves device hostnames to addresses while it still can and ships
   addresses in lan plans; tests assert no `.local` name survives into a
   plan.

Each finding produced either a code fix with a regression test in this
repository or, for the mesh overlay, a documented upstream handoff. None of
the four were visible to the unit suites, and two were only reachable after
the previous one was fixed. That sequencing is the argument for keeping these
scripts runnable.

## Addendum, security review follow-up (same day)

After the AI security review, fleet authentication was added and re-verified
on the same three devices: a 10 generation es-fleet run over the lan
transport with a launcher-generated `WT_FLEET_TOKEN` completed with the full
population contributed (`n_contributed` 24 of 24), while an unauthenticated
`GET /status` from the operator's machine received 401 throughout the run.
