# Aggregate, AI Review, Report

```text
make e2e-aggregate      attempts → run directory
make e2e-review         AI review → review.<reviewer>/<slug>.md
make e2e-report         index.html · review.md · review.html

<run>/
  attempts/<target>/<n>/…
  observations/<suite>/<test>/
    source.md            # spec + test source
    <target>/<n>/recording.md, recording.sh.txt
  source-index.md
```
