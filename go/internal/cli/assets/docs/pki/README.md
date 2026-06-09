# PKI

Wendy uses mutual TLS (mTLS) to authenticate both devices and CLI clients against the agent's gRPC server. Certificates are issued by a CA managed by either Wendy Cloud or a self-hosted **pki-core** instance.

## Certificate roles

| Certificate | Issued to | Used for |
|-------------|-----------|----------|
| Device cert | `wendy-agent` during provisioning | mTLS server identity; stored in `/etc/wendy-agent/provisioning.json` |
| CLI cert | Developer machine via `wendy auth login` | mTLS client auth when connecting to provisioned devices |

The device's mTLS CA pool is built from the `chainPem` field in `provisioning.json`. CLI clients must present a certificate whose chain terminates at that same CA. If `chainPem` is absent or empty, the agent refuses to build a TLS configuration and returns an error indicating that the device may need to be re-provisioned.

## CA key rollover

The trust bundle may contain multiple CA certificates sharing the same subject DN. This is normal during a CA key rollover, where an old CA and a new CA temporarily coexist in the bundle. The agent's ML-DSA client certificate verifier (`verifyMLDSAClientCert`) tries every CA whose subject DN matches the client certificate's issuer DN. Verification succeeds as soon as any matching CA validates the certificate. If all matching CAs fail, the error from the last attempted CA is returned. If no CA in the pool has a matching subject DN, the verifier returns a "client certificate issuer not found in trusted CA pool" error.

## Clock skew and the NotBefore floor

A device that reboots without network connectivity (e.g. a power cycle with no WiFi) may not have synchronised its clock via NTP before the mTLS server starts. With an unsynchronised clock that predates the certificates, every incoming client certificate would be rejected as "not yet valid", silently making the mTLS port unusable.

To handle this, `wendy-agent` reads the `NotBefore` timestamp from the device's own provisioning certificate at startup and passes it to the verifier as a **time floor**. During peer-certificate verification the effective time is:

```
effectiveNow = max(time.Now(), provisioningCert.NotBefore)
```

`effectiveNow` is used **only for the leaf certificate's `NotBefore` check** — on both the standard (RSA/ECDSA) path, via `x509.VerifyOptions.CurrentTime`, and the ML-DSA path. Certificate expiry (`NotAfter`) and CA-certificate validity are always checked against the real system clock, so the floor cannot mask a genuinely expired certificate or make an immature CA appear valid. Pass a zero `time.Time` to disable the floor.

When the device clock is behind the floor at startup, `wendy-agent` logs a `WARN` that includes the device clock, the floor, and how far behind the clock is. If the provisioning certificate cannot be parsed, the floor is zero (disabled) and a `WARN` notes that NTP clock-skew protection is off. Once NTP synchronises the clock the discrepancy disappears on its own.

The systemd unit also orders the agent after `time-sync.target` (`After=`/`Wants=`) so NTP synchronisation is attempted as early as possible; the floor is the primary guard and does not depend on that ordering.

## Local development with pki-core

[pki-core](https://github.com/wendylabsinc/pki-core) is the self-hosted Wendy PKI engine. Run it locally to provision real devices without a cloud deployment.

### Prerequisites

1. **Start the engine and admin API:**
   ```sh
   pkicore serve all --dev
   ```
2. **Create a CA and configure the Wendy frontend** (`frontend.wendy.device_ca_id` in `config.yaml`).

3. **Start the Wendy gRPC frontend:**
   ```sh
   pkicore serve wendy --config config.yaml
   ```
   This exposes `wendycloud.v1.CertificateService` on the configured listen address (default `:50051`).

### Provision a device

Find your machine's LAN IP (the address the device can reach):

```sh
ifconfig | grep "inet " | grep -v 127.0.0.1
```

Then provision the target device:

```sh
wendy device provision \
  --cloud <your-lan-ip>:50051 \
  --api-key <key-from-config.yaml> \
  --name my-device
```

### Authenticate the CLI

Issue a client certificate from the same pki-core so the CLI can connect over mTLS:

```sh
wendy auth login-local \
  --cloud <your-lan-ip>:50051 \
  --api-key <key-from-config.yaml>
```

After this, `wendy device version`, `wendy run`, and other device commands automatically use the mTLS port (plaintext port + 1) when the device's Avahi advertisement includes `tls=true`.

## Avahi advertisement

After provisioning, `wendy-agent` updates `/etc/avahi/services/wendyos-mdns.service` to set the `_wendyos._udp` service block's port to the mTLS port and adds a `tls=true` TXT record. The CLI reads this record during device discovery to select the mTLS connection path.
