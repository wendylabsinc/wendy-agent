# `wendy device observe`

Device-scoped form of [`wendy observe`](../observe.md):

```bash
wendy device observe --device <hostname> [flags]
```

It deploys `sh.wendy.observe`, binds the device gateway to loopback, and carries
HTTPS and WSS through the authenticated Wendy device tunnel. See
[Wendy Observe](/docs/integrations/observe) for stream profiles and framing.
