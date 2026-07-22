# CI Boundaries

```text
swift-e2e-tests.yml

  Local: macOS 26     hosted runner + managed WendyAgentMac
  Local: Ubuntu 24    hosted runner + managed Go agent
  Analyze             aggregate → AI review → PR comment

Boundaries
  auth fixtures only — personal ~/.wendy/config.json never leaks
  legacy integration suite: protected, non-fork workflows only
  physical routes (Pi, Jetson, SER9, …): dormant ledger,
  disabled until reliable CI hardware exists
```
