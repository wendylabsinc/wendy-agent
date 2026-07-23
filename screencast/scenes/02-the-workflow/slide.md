# The Workflow

```sh
cd swift

# Run only the command area you are changing
bash Scripts/E2ETest.sh \
  --output-dir ../Build/e2e \
  --filter "wendy info"

make e2e-analyze      # aggregate, review, report
make e2e-reference    # browse documented behavior
```
