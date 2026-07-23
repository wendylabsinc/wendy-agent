Stay on the simplest route that can prove the behavior.

Local, unauthenticated command behavior should use the default isolated
scenario. Authenticated tests use a dedicated E2E config fixture, never your
personal Wendy config. Device or remote-host behavior uses the explicit target
variables and make targets documented in the package README.

If a behavior genuinely needs cloud state, an interactive prompt harness, or
physical hardware that is not reliable in CI, do not fake it into the hosted
path. Keep that dependency explicit and tracked.
