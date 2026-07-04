package liteclient

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strings"
	"time"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

const (
	headerMagic        = 0xA5
	headerVersion      = 0x01
	headerSize         = 8
	chunkSize          = 4096
	chunkSizeForSerial = 768
	versionMajor       = 1
	versionMinor       = 0
	esc                = 0x1B
)

type protocolVersion struct {
	Major uint32
	Minor uint32
}

type DeviceIdentity struct {
	ID          string
	Name        string
	DisplayName string
}

type WendyLiteClient struct {
	conn                io.ReadWriteCloser
	isSerial            bool
	requestIdGen        uint32
	peerProtocolVersion protocolVersion
}

func NewWendyLiteClient() *WendyLiteClient {
	return &WendyLiteClient{}
}

func (c *WendyLiteClient) ConnectInsecure(address string) error {
	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec — device uses self-signed certs
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c.conn = conn
	c.isSerial = false
	err = c.exchangeProtocolVersions()
	if err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("get protocol version: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) ConnectWithMutualAuthentication(address string, cert tls.Certificate, rootCAs x509.CertPool) error {
	// Verify the certificate chain against our root CAs but skip hostname
	// checking — devices on a local network don't have SANs.
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(rawCerts))
			for i, raw := range rawCerts {
				c, err := x509.ParseCertificate(raw)
				if err != nil {
					return fmt.Errorf("parsing server certificate: %w", err)
				}
				certs[i] = c
			}
			opts := x509.VerifyOptions{
				Roots:         &rootCAs,
				Intermediates: x509.NewCertPool(),
			}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			if _, err := certs[0].Verify(opts); err != nil {
				return fmt.Errorf("server certificate verification failed: %w", err)
			}
			return nil
		},
	}
	conn, err := tls.Dial("tcp", address, tlsCfg)
	if err != nil {
		return fmt.Errorf("connect (mTLS): %w", err)
	}
	c.conn = conn
	c.isSerial = false
	err = c.exchangeProtocolVersions()
	if err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("get protocol version: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) ConnectToSerial(device string) error {
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	port, err := serial.Open(device, mode)
	if err != nil {
		return fmt.Errorf("open serial: %w", err)
	}
	if err := serialHandshake(port); err != nil {
		port.Close()
		return err
	}
	c.conn = port
	c.isSerial = true
	if err := c.exchangeProtocolVersions(); err != nil {
		port.Close()
		c.conn = nil
		return fmt.Errorf("get protocol version: %w", err)
	}
	return nil
}

func serialHandshake(port serial.Port) error {
	if _, err := port.Write([]byte{esc, esc, esc, esc, 'e'}); err != nil {
		return fmt.Errorf("serial handshake: send escape: %w", err)
	}

	var sentinel string

	if err := port.SetReadTimeout(100 * time.Millisecond); err != nil {
		return fmt.Errorf("serial handshake: set timeout: %w", err)
	}

	window := make([]byte, 0, 32)
	oneByte := make([]byte, 1)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := port.Read(oneByte)
		if err != nil {
			return fmt.Errorf("serial handshake: read: %w", err)
		}
		if n == 0 {
			// rx timeout, so rx buffer empty, so send sentinel
			var randBytes [16]byte
			if _, err := rand.Read(randBytes[:]); err != nil {
				return fmt.Errorf("serial handshake: generate sentinel: %w", err)
			}
			sentinel = hex.EncodeToString(randBytes[:])
			window = window[:0]
			if _, err := port.Write([]byte(strings.Repeat(" ", 16) + sentinel)); err != nil {
				return fmt.Errorf("serial handshake: send sentinel: %w", err)
			}
			continue
		}
		if n == 1 {
			if len(window) < 32 {
				window = append(window, oneByte[0])
			} else {
				copy(window, window[1:])
				window[31] = oneByte[0]
			}
			if sentinel != "" && len(window) == 32 && string(window) == sentinel {
				if err := port.SetReadTimeout(0); err != nil {
					return fmt.Errorf("serial handshake: clear timeout: %w", err)
				}
				if _, err := port.Write([]byte{esc, 'm'}); err != nil {
					return fmt.Errorf("serial handshake: send mode switch: %w", err)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("serial handshake: sentinel not received within 3 seconds")
}

func (c *WendyLiteClient) Close() error {
	if c.conn == nil {
		return nil
	}
	if c.isSerial {
		if port, ok := c.conn.(serial.Port); ok {
			_, _ = port.Write([]byte{esc, 'o'})
			_ = port.Drain()
		}
	}
	return c.conn.Close()
}

func (c *WendyLiteClient) Ping() error {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_Ping{
			Ping: &wendypb.WendyComPingParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) ResetTargetDevice() error {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_Reboot{
			Reboot: &wendypb.WendyComRebootParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) PushApp(path string, onProgress func(written, total uint32)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if info.Size() > math.MaxUint32 {
		return fmt.Errorf("WASM file too large: %d bytes exceeds 4 GiB limit", info.Size())
	}
	size := uint32(info.Size())

	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_AppPushBegin{
			AppPushBegin: &wendypb.WendyComAppPushBeginParams{Size: size},
		},
	}, 0)
	if err != nil {
		return fmt.Errorf("push begin: %w", err)
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("push begin: device returned error %d", resp.Result)
	}

	chunk := chunkSize
	if c.isSerial {
		chunk = chunkSizeForSerial
	}
	buf := make([]byte, chunk)
	var offset uint32
	for {
		n, err := f.Read(buf)
		if n > 0 {
			c.requestIdGen++
			resp, serr := c.sendCommand(&wendypb.WendyComCommand{
				RequestId: c.requestIdGen,
				Params: &wendypb.WendyComCommand_AppPushData{
					AppPushData: &wendypb.WendyComAppPushDataParams{
						Offset: offset,
						Data:   buf[:n],
					},
				},
			}, 0)
			if serr != nil {
				return fmt.Errorf("push data at offset %d: %w", offset, serr)
			}
			if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
				return fmt.Errorf("push data at offset %d: device returned error %d", offset, resp.Result)
			}
			offset += uint32(n)
			if onProgress != nil {
				onProgress(offset, size)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}

	c.requestIdGen++
	resp, err = c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_AppPushEnd{
			AppPushEnd: &wendypb.WendyComAppPushEndParams{},
		},
	}, 0)
	if err != nil {
		return fmt.Errorf("push end: %w", err)
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("push end: device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) StopApp() error {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_AppStop{
			AppStop: &wendypb.WendyComAppStopParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) StartApp() error {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_AppStart{
			AppStart: &wendypb.WendyComAppStartParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) GetDeviceIdentity(timeout time.Duration) (*DeviceIdentity, error) {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_GetDeviceIdentity{
			GetDeviceIdentity: &wendypb.WendyComGetDeviceIdentityParams{},
		},
	}, timeout)
	if err != nil {
		return nil, err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return nil, fmt.Errorf("device returned error %d", resp.Result)
	}
	di := resp.GetDeviceIdentity()
	if di == nil {
		return nil, fmt.Errorf("device returned no identity")
	}
	return &DeviceIdentity{ID: di.GetId(), Name: di.GetName(), DisplayName: di.GetDisplayName()}, nil
}

func (c *WendyLiteClient) sendCommand(cmd *wendypb.WendyComCommand, timeout time.Duration) (*wendypb.WendyComResponse, error) {
	body, err := proto.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	msg := make([]byte, headerSize+len(body))
	msg[0] = headerMagic
	msg[1] = headerVersion
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(body)))
	copy(msg[headerSize:], body)
	if c.isSerial {
		msg = bytes.ReplaceAll(msg, []byte{esc}, []byte{esc, '_'})
	}
	for len(msg) > 0 {
		n, err := c.conn.Write(msg)
		if err != nil {
			return nil, fmt.Errorf("send: %w", err)
		}
		msg = msg[n:]
	}
	raw, err := c.readResponse(timeout)
	if err != nil {
		return nil, err
	}
	resp := &wendypb.WendyComResponse{}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if cmd.RequestId != resp.RequestId {
		return nil, fmt.Errorf("unexpected response: request ID mismatch")
	}
	return resp, nil
}

func (c *WendyLiteClient) readResponse(timeout time.Duration) ([]byte, error) {
	header := make([]byte, headerSize)
	if err := c.readFull(header, timeout); err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if header[0] != headerMagic {
		return nil, fmt.Errorf("unexpected magic byte: 0x%02X", header[0])
	}
	bodyLen := binary.BigEndian.Uint16(header[6:8])
	if bodyLen == 0 {
		return nil, nil
	}
	body := make([]byte, bodyLen)
	if err := c.readFull(body, timeout); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	return body, nil
}

// readFull reads exactly len(buf) bytes from the connection within timeout.
// A zero timeout means no deadline.
//
// For net.Conn, it sets SetReadDeadline for the duration of the call.
//
// For serial.Port, SetReadTimeout makes Read return (0, nil) on timeout
// instead of an error, which would cause io.ReadFull to spin indefinitely.
// readFull therefore loops manually, trimming the per-Read call to the
// remaining time until the deadline, and converts (0, nil) to an error.
func (c *WendyLiteClient) readFull(buf []byte, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if !c.isSerial {
		if nc, ok := c.conn.(net.Conn); ok && !deadline.IsZero() {
			_ = nc.SetReadDeadline(deadline)
			defer nc.SetReadDeadline(time.Time{}) //nolint:errcheck
		}
		_, err := io.ReadFull(c.conn, buf)
		return err
	}
	sp := c.conn.(serial.Port)
	defer sp.SetReadTimeout(serial.NoTimeout) //nolint:errcheck
	for len(buf) > 0 {
		perRead := serial.NoTimeout
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("read timeout")
			}
			perRead = remaining
		}
		_ = sp.SetReadTimeout(perRead)
		n, err := sp.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("read timeout")
		}
		buf = buf[n:]
	}
	return nil
}

func (c *WendyLiteClient) exchangeProtocolVersions() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		Params: &wendypb.WendyComCommand_ProtocolVersion{
			ProtocolVersion: &wendypb.WendyComProtocolVersionParams{
				Major: versionMajor,
				Minor: versionMinor,
			},
		},
	}, 3*time.Second)
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	c.peerProtocolVersion = protocolVersion{Major: resp.GetProtocolVersion().GetMajor(), Minor: resp.GetProtocolVersion().GetMinor()}
	return nil
}
