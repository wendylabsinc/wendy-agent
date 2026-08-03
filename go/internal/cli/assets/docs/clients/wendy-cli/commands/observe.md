# `wendy observe`

Deploy a demand-driven ROS 2 preprocessing gateway and forward its HTTPS/WSS
endpoint to local loopback:

```bash
wendy observe [flags]
```

The equivalent scoped command is `wendy device observe`. Use
`wendy cloud device observe` for a cloud-connected robot.

The command remains attached to the port-forwarding session until Ctrl-C. The
Observe app stays deployed on the device, but its ROS subscriptions and
processors exist only while a WSS or HTTPS stream requests them.

Common options:

```text
--domain <id>             ROS domain ID (default 0)
--interface <name>        CycloneDDS interface override
--max-bandwidth <Mbps>    Session-wide output budget (default 8)
--max-hz <rate>           Per-stream output ceiling (default 10)
--point-stride <N>        Minimum point-cloud stride (default 4)
--jpeg-quality <1-100>    Maximum JPEG quality (default 65)
--max-image-width <px>    Maximum image width (default 960)
--port <port>             Local HTTPS/WSS port (default 8780)
```

See [Wendy Observe](/docs/integrations/observe) for the endpoint and binary
frame contracts.
