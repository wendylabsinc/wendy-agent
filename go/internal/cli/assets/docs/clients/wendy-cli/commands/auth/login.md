Authenticates the CLI with Wendy Cloud. Opens a browser to the cloud dashboard, waits for the OAuth callback, generates a key pair and CSR, then issues and stores an mTLS certificate. Subsequent commands that connect to provisioned devices use this certificate automatically.

After displaying the login URL, the CLI also prints a QR code in the terminal. You can scan this QR code with the **Wendy iOS app** to authenticate on your phone instead of (or in addition to) the browser flow.

Optionally accepts `--cloud` (dashboard URL) and `--cloud-grpc` (gRPC endpoint) to point at a non-default cloud instance.
