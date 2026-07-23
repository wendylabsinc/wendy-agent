# Choose the Right Environment

```text
Choose the smallest honest environment

local CLI behavior       → isolated default scenario
authenticated behavior   → dedicated E2E auth fixture
remote/device behavior   → explicit DEVICE / target variables
cloud, PTY, hardware     → explicit fixture or tracked dependency

Never use personal auth or machine state as a test fixture.
```
