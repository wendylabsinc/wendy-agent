The Cloud provides two endpoints; One for [Wendy Agent](../wendy-agent/) devices, and one API for end-users (Web Dashboards, mobile apps etc.)

The agent communicates over **gRPC**, which enables us to (conditionally) expose relevant services.
When an agent connects to the cloud, it does so using mTLS by providing a client-certificate. The Cloud verifies and authenticates the device based on this certificate.

Refer to the [PKI](../pki/) documents for more info on certificates, auth and verification.