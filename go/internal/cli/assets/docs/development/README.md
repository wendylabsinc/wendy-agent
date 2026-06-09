# Developer Guide

This guide covers everything you need to contribute to the `wendy-agent` repository, which contains both the Wendy CLI (`wendy`) and the on-device daemon (`wendy-agent`).

## Contents

| Document | Covers |
|---|---|
| [setup.md](setup.md) | Prerequisites, building from source, shell aliases |
| [testing.md](testing.md) | Unit tests, integration tests against real hardware, test coverage |
| [contributing.md](contributing.md) | Branching model, commit style, PR workflow, code owners |
| [debugging.md](debugging.md) | Debugging the agent itself: logs, env vars, gRPC tracing, common failures |
| [docs-site.md](docs-site.md) | Fumadocs local development, CI, hosting, and release docs URLs |

## Repository Layout

```
wendy-agent/
  go.mod                Go module root
  go/                   Go source tree (CLI + agent)
    cmd/wendy/          CLI entry point
    cmd/wendy-agent/    Agent entry point
    internal/
      agent/            Agent subsystems (containerd, bluetooth, network, mTLS, …)
      cli/              CLI commands, gRPC client, TUI
      shared/           Code shared between CLI and agent
    proto/gen/          Generated protobuf bindings (do not edit by hand)
    scripts/            Build helpers, proto generation, CI test runner
    Makefile            Build targets (run from go/)
  proto-defs/           Protobuf definitions (shared source of truth)
  docs/                 Documentation (symlink -> go/internal/cli/assets/docs)
  plugins/              Claude Code plugins
  swift/                macOS companion app (WendyAgentMac)
  packaging/            Linux .deb/.rpm packaging scripts
  .github/workflows/    CI pipelines
```

## Quick Start

```sh
# Clone
git clone https://github.com/wendylabsinc/wendy-agent.git
cd wendy-agent

# Build both binaries (run make from go/)
cd go && make build

# Run unit tests
make test
```

See [setup.md](setup.md) for full prerequisites and [testing.md](testing.md) for how to run integration tests against a device.
