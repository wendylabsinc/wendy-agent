package liteclient

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/console/bleconn"
)

// bleDialTimeout covers GAP connect, service discovery and opening the L2CAP
// channel. A cold connect to an advertising device is well under a second;
// this is the give-up point, not the expectation.
const bleDialTimeout = 10 * time.Second

// ConnectViaBLE reaches a device over BLE: it opens the L2CAP channel, runs
// the TLS handshake on it, and then speaks ordinary WendyCom. Everything above
// the byte stream is identical to the TCP path.
//
// psm may be 0, in which case it comes from the device's GATT info service,
// falling back to bleconn.DefaultPSM where GATT is unavailable.
//
// tlsCfg carries the client's side of the trust decision; nil selects the
// insecure configuration an unprovisioned device needs.
func (c *WendyLiteClient) ConnectViaBLE(address string, psm uint16, tlsCfg *tls.Config) error {
	conn, err := bleconn.Dial(address, bleDialTimeout)
	if err != nil {
		return err
	}

	if psm == 0 {
		// Not fatal: where GATT is unavailable the compile-time default is the
		// answer, and a device that really disagrees fails the open below with
		// a clearer error than this would give.
		if info, ierr := conn.ReadInfo(bleDialTimeout); ierr == nil {
			psm = info.PSM
		}
		if psm == 0 {
			psm = bleconn.DefaultPSM
		}
	}

	if err := conn.OpenL2CAP(psm, bleDialTimeout); err != nil {
		conn.Close()
		return err
	}

	if tlsCfg == nil {
		tlsCfg = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec — unprovisioned devices serve a self-signed cert
			MinVersion:         tls.VersionTLS12,
		}
	}
	tlsConn := tls.Client(conn.Stream(), tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return fmt.Errorf("BLE TLS handshake: %w", err)
	}

	c.link = newDirectLink(tlsConn)
	if err := c.handshake(); err != nil {
		tlsConn.Close()
		c.link = nil
		return fmt.Errorf("handshake: %w", err)
	}
	c.startReadLoop()
	return nil
}

// ConnectViaBLEInsecure reaches a device over BLE without verifying its
// certificate, mirroring ConnectInsecure on TCP. An unprovisioned device
// serves the self-signed certificate compiled into the firmware, which
// authenticates nothing — and reaching an unprovisioned device is most of what
// BLE is for.
//
// SECURITY: development use only. Prefer
// ConnectViaBLEWithMutualAuthentication against a provisioned device.
func (c *WendyLiteClient) ConnectViaBLEInsecure(address string, psm uint16) error {
	return c.ConnectViaBLE(address, psm, nil)
}

// ConnectViaBLEWithMutualAuthentication reaches a provisioned device over BLE,
// presenting a client certificate and verifying the device's against rootCAs.
//
// Hostname verification is off because there is no hostname over L2CAP; the
// explicit chain check is what establishes identity. This mirrors
// ConnectWithMutualAuthentication on TCP.
func (c *WendyLiteClient) ConnectViaBLEWithMutualAuthentication(
	address string, psm uint16, cert tls.Certificate, rootCAs x509.CertPool) error {

	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; the chain is checked below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(rawCerts))
			for i, raw := range rawCerts {
				parsed, err := x509.ParseCertificate(raw)
				if err != nil {
					return fmt.Errorf("parsing device certificate: %w", err)
				}
				certs[i] = parsed
			}
			opts := x509.VerifyOptions{
				Roots:         &rootCAs,
				Intermediates: x509.NewCertPool(),
			}
			for _, intermediate := range certs[1:] {
				opts.Intermediates.AddCert(intermediate)
			}
			if _, err := certs[0].Verify(opts); err != nil {
				return fmt.Errorf("device certificate verification failed: %w", err)
			}
			return nil
		},
	}
	return c.ConnectViaBLE(address, psm, tlsCfg)
}
