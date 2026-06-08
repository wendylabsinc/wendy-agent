package liteclient

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"google.golang.org/protobuf/proto"
)

const chunkSize = 4096

const (
	headerMagic   = 0xA5
	headerVersion = 0x01
	headerSize    = 8
)

type WendyLiteClient struct {
	addr         string
	conn         io.ReadWriteCloser
	requestIdGen uint32
}

func NewWendyLiteClient(addr string) *WendyLiteClient {
	return &WendyLiteClient{addr: addr}
}

func (c *WendyLiteClient) Connect() error {
	conn, err := tls.Dial("tcp", c.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c.conn = conn
	return nil
}

func (c *WendyLiteClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func (c *WendyLiteClient) Ping() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
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

func (c *WendyLiteClient) GetProtocolVersion() (major, minor uint32, err error) {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		Params: &wendypb.WendyComCommand_GetProtocolVersion{
			GetProtocolVersion: &wendypb.WendyComGetProtocolVersionParams{},
		},
	})
	if err != nil {
		return 0, 0, err
	}
	if resp.Result != wendypb.WendyComResult_WENDY_COM_RESULT_OK {
		return 0, 0, fmt.Errorf("device returned error")
	}
	v := resp.GetProtocolVersion()
	return v.GetMajor(), v.GetMinor(), nil
}

func (c *WendyLiteClient) ResetTargetDevice() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
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
	size := uint32(info.Size())

	resp, err := c.sendCommand(&wendypb.WendyComCommand{
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
			resp, serr := c.sendCommand(&wendypb.WendyComCommand{
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

	resp, err = c.sendCommand(&wendypb.WendyComCommand{
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
