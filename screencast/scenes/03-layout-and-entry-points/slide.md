# Layout and Entry Points

```text
swift/
  WendyE2ETests/          # the test package: 110 files, one suite each
  Scripts/E2ETest.sh      # preferred runner: sandboxes + artifacts
  Makefile

make e2e-test         run locally, write attempt artifacts
make e2e-analyze      aggregate + AI review + HTML report
make e2e-reference    behavioral reference docs from test source

Remote targets:
make e2e-test-wendy DEVICE=wendyos-raspberry-pi-5.local
```
