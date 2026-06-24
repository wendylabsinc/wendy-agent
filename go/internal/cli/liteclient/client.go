package liteclient

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"google.golang.org/protobuf/proto"
)

const (
	headerMagic   = 0xA5
	headerVersion = 0x01
	headerSize    = 8
	chunkSize     = 4096
	versionMajor  = 1
	versionMinor  = 0
)

type protocolVersion struct {
	Major uint32
	Minor uint32
}

type WendyLiteClient struct {
	conn                io.ReadWriteCloser
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
	err = c.exchangeProtocolVersions()
	if err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("get protocol version: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *WendyLiteClient) Ping() error {
	c.requestIdGen++
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen,
		Params: &wendypb.WendyComCommand_Ping{
			Ping: &wendypb.WendyComPingParams{},
		},
	})
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
	})
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
	})
	if err != nil {
		return fmt.Errorf("push begin: %w", err)
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("push begin: device returned error %d", resp.Result)
	}

	buf := make([]byte, chunkSize)
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
			})
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
	})
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
	})
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
	})
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	return nil
}

func (c *WendyLiteClient) sendCommand(cmd *wendypb.WendyComCommand) (*wendypb.WendyComResponse, error) {
	body, err := proto.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	msg := make([]byte, headerSize+len(body))
	msg[0] = headerMagic
	msg[1] = headerVersion
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(body)))
	copy(msg[headerSize:], body)
	if _, err := c.conn.Write(msg); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	raw, err := c.readResponse()
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

func (c *WendyLiteClient) readResponse() ([]byte, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(c.conn, header); err != nil {
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
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	return body, nil
}

func (c *WendyLiteClient) exchangeProtocolVersions() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		Params: &wendypb.WendyComCommand_ProtocolVersion{
			ProtocolVersion: &wendypb.WendyComProtocolVersionParams{
				Major: versionMajor,
				Minor: versionMinor,
			},
		},
	})
	if err != nil {
		return err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return fmt.Errorf("device returned error %d", resp.Result)
	}
	c.peerProtocolVersion = protocolVersion{Major: resp.GetProtocolVersion().GetMajor(), Minor: resp.GetProtocolVersion().GetMinor()}
	return nil
}
