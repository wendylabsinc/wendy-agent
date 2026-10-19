The Cloud provides two endpoints; One for Wendy Agent devices, and one API for end-users (Web Dashboards, mobile apps etc.)

The agent communicates over **gRPC**, which enables us to (conditionally) expose relevant services.
When an agent connects to the cloud, it does so using mTLS by providing a client-certificate. The Cloud verifies and authenticates the device based on this certificate.

Refer to the [PKI](../pki/) documents for more info on certificates, auth and verification.

## Network changes

An agent's broker connection keeps the address it was opened with, even after the device's routing changes. When a new connection would leave from another address, such as after Ethernet returns to a device on Wi-Fi, the agent reconnects within about 30 seconds, once nothing is connected to the device through the cloud and only if the broker answers over the new path. Devices that connect through the Cloud v2 relay do not switch yet.
