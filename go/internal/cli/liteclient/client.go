package liteclient

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

const (
	headerMagic        = 0xA5
	headerVersion      = 0x02
	headerSize         = 8
	chunkSize          = 4096
	chunkSizeForSerial = 768
	versionMajor       = 2
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

type DeviceInfo struct {
	OS               string
	OSVersion        string
	CPUArchitecture  string
	Board            string
	WasmAppSupport   bool
	NativeAppSupport bool
}

type AppType int

// ConsoleChunk is one piece of console output streamed by the device.
type ConsoleChunk struct {
	Gap    bool // data was lost before this chunk
	Stderr bool
	Data   []byte
}

const (
	AppTypeWasm AppType = iota
	AppTypeNative

	// consoleLease is the attachment duration requested in rolling mode;
	// consoleRenew re-arms it well before it expires.
	consoleLease = 20 * time.Second
	consoleRenew = 10 * time.Second
)

// subscription is one waiter registered with the read loop. The read loop
// delivers every message matching filter to ch. The channel capacity is a
// throughput smoother, not a correctness mechanism: dispatch blocks on a full
// channel, so a subscriber must keep draining until unsubscribed.
type subscription struct {
	filter func(*wendypb.WendyComMessage) bool
	ch     chan *wendypb.WendyComMessage
}

type WendyLiteClient struct {
	conn                io.ReadWriteCloser
	isSerial            bool
	serialLock          *serialLock
	requestIdGen        atomic.Uint32
	eventIdGen          atomic.Uint32
	peerProtocolVersion protocolVersion

	writeMu sync.Mutex // serializes writeMessage across command goroutines

	mu      sync.Mutex // guards subs and readErr
	subs    []*subscription
	readErr error // set once the read loop dies; refuses new subscriptions
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
	err = c.handshake()
	if err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("handshake: %w", err)
	}
	go c.readLoop()
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
	err = c.handshake()
	if err != nil {
		conn.Close()
		c.conn = nil
		return fmt.Errorf("handshake: %w", err)
	}
	go c.readLoop()
	return nil
}

func (c *WendyLiteClient) ConnectToSerial(device string) error {
	lock, err := acquireSerialLock(device)
	if err != nil {
		return err
	}
	mode := &serial.Mode{
		BaudRate: 115200,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	}
	port, err := serial.Open(device, mode)
	if err != nil {
		lock.release()
		return fmt.Errorf("open serial: %w", err)
	}
	if err := serialHandshake(port); err != nil {
		port.Close()
		lock.release()
		return err
	}
	c.conn = port
	c.isSerial = true
	c.serialLock = lock
	if err := c.handshake(); err != nil {
		port.Close()
		lock.release()
		c.conn = nil
		c.serialLock = nil
		return fmt.Errorf("handshake: %w", err)
	}
	go c.readLoop()
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
	err := c.conn.Close()
	c.serialLock.release()
	c.serialLock = nil
	return err
}

func (c *WendyLiteClient) Ping() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_Ping{
			Ping: &wendypb.WendyComPingParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("device returned error: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) ResetTargetDevice() error {
	// The device reboots on receipt, so no ack is expected.
	return c.writeMessage(&wendypb.WendyComMessage{
		Msg: &wendypb.WendyComMessage_Command{Command: &wendypb.WendyComCommand{
			RequestId: c.requestIdGen.Add(1),
			Params: &wendypb.WendyComCommand_Reboot{
				Reboot: &wendypb.WendyComRebootParams{},
			},
		}},
	})
}

func (c *WendyLiteClient) PushApp(path string, appType AppType, onProgress func(written, total uint32)) error {
	var pbAppType wendypb.WendyComAppType
	switch appType {
	case AppTypeWasm:
		pbAppType = wendypb.WendyComAppType_WENDY_COM_APP_TYPE_WASM
	case AppTypeNative:
		pbAppType = wendypb.WendyComAppType_WENDY_COM_APP_TYPE_NATIVE
	default:
		return fmt.Errorf("unknown app type %d", appType)
	}

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
		return fmt.Errorf("app file too large: %d bytes exceeds 4 GiB limit", info.Size())
	}
	size := uint32(info.Size())

	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_AppPushBegin{
			AppPushBegin: &wendypb.WendyComAppPushBeginParams{Size: size, AppType: pbAppType},
		},
	}, 0)
	if err != nil {
		return fmt.Errorf("push begin: %w", err)
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("push begin: device returned error: %w", err)
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
			resp, serr := c.sendCommand(&wendypb.WendyComCommand{
				RequestId: c.requestIdGen.Add(1),
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
			if err := resultToError(resp.Result); err != nil {
				return fmt.Errorf("push data at offset %d: device returned error: %w", offset, err)
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
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_AppPushEnd{
			AppPushEnd: &wendypb.WendyComAppPushEndParams{},
		},
	}, 0)
	if err != nil {
		return fmt.Errorf("push end: %w", err)
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("push end: device returned error: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) StopApp() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_AppStop{
			AppStop: &wendypb.WendyComAppStopParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("device returned error: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) StartApp() error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_AppStart{
			AppStart: &wendypb.WendyComAppStartParams{},
		},
	}, 0)
	if err != nil {
		return err
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("device returned error: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) GetDeviceIdentity(timeout time.Duration) (*DeviceIdentity, error) {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_GetDeviceIdentity{
			GetDeviceIdentity: &wendypb.WendyComGetDeviceIdentityParams{},
		},
	}, timeout)
	if err != nil {
		return nil, err
	}
	if err := resultToError(resp.Result); err != nil {
		return nil, fmt.Errorf("device returned error: %w", err)
	}
	di := resp.GetDeviceIdentity()
	if di == nil {
		return nil, fmt.Errorf("device returned no identity")
	}
	return &DeviceIdentity{ID: di.GetId(), Name: di.GetName(), DisplayName: di.GetDisplayName()}, nil
}

func (c *WendyLiteClient) GetDeviceInfo(timeout time.Duration) (*DeviceInfo, error) {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_GetDeviceInfo{
			GetDeviceInfo: &wendypb.WendyComGetDeviceInfoParams{},
		},
	}, timeout)
	if err != nil {
		return nil, err
	}
	if err := resultToError(resp.Result); err != nil {
		return nil, fmt.Errorf("device returned error: %w", err)
	}
	di := resp.GetDeviceInfo()
	if di == nil {
		return nil, fmt.Errorf("device returned no info")
	}
	return &DeviceInfo{
		OS:               di.GetOs(),
		OSVersion:        di.GetOsVersion(),
		CPUArchitecture:  di.GetCpuArchitecture(),
		Board:            di.GetBoard(),
		WasmAppSupport:   di.GetWasmAppSupport(),
		NativeAppSupport: di.GetNativeAppSupport(),
	}, nil
}

// ConsoleAttach asks the device to stream its console output and returns the
// chunk channel plus an idempotent detach function. The channel is closed on
// detach and on connection loss. detach stops local delivery, then tells the
// device to stop streaming and returns its result.
//
// With rollingMode the attachment is a lease: it is requested for
// consoleLease and silently renewed every consoleRenew, so the device
// detaches by itself within consoleLease if this client dies without
// detaching. Without rollingMode the device stays attached until detach.
//
// With blockingMode the device captures losslessly: writers on the device
// block when the capture buffer is full, until it is drained. Without
// blockingMode the oldest buffered data is dropped instead and the loss is
// reported as a gap.
func (c *WendyLiteClient) ConsoleAttach(rollingMode bool, blockingMode bool) (<-chan ConsoleChunk, func() error, error) {
	eventID := c.eventIdGen.Add(1)

	// Subscribe before attaching so no chunk is lost.
	sub, err := c.subscribe(func(m *wendypb.WendyComMessage) bool {
		e := m.GetEvent()
		return e != nil && e.GetEventId() == eventID && e.GetConsoleData() != nil
	}, 16)
	if err != nil {
		return nil, nil, err
	}

	var lease time.Duration
	if rollingMode {
		lease = consoleLease
	}
	if err := c.sendConsoleAttach(eventID, lease, blockingMode); err != nil {
		c.unsubscribe(sub)
		return nil, nil, err
	}

	out := make(chan ConsoleChunk)
	done := make(chan struct{})  // closed by detach
	ended := make(chan struct{}) // closed once sub.ch is closed (detach or connection loss)
	go func() {
		for msg := range sub.ch {
			cd := msg.GetEvent().GetConsoleData()
			chunk := ConsoleChunk{
				Gap:    cd.GetGap(),
				Stderr: cd.GetIo() == wendypb.WendyComConsoleIo_WENDY_COM_CONSOLE_IO_STANDARD_ERROR,
				Data:   cd.GetData(),
			}
			// Keep draining sub.ch after detach: a dispatch blocked on a full
			// sub.ch holds the client mutex, and detach's unsubscribe needs
			// that mutex — discarding here breaks the cycle.
			select {
			case out <- chunk:
			case <-done:
			}
		}
		close(ended)
		close(out)
	}()

	var renewer sync.WaitGroup
	if rollingMode {
		renewer.Add(1)
		go func() {
			defer renewer.Done()
			ticker := time.NewTicker(consoleRenew)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// A failed renewal is not fatal: the next tick retries,
					// and a lost connection ends the loop via ended.
					_ = c.sendConsoleAttach(eventID, consoleLease, blockingMode)
				case <-ended:
					return
				}
			}
		}()
	}

	var once sync.Once
	var detachErr error
	detach := func() error {
		once.Do(func() {
			close(done)
			c.unsubscribe(sub)
			renewer.Wait() // no renewal may land after the detach command
			detachErr = c.sendConsoleDetach(eventID)
		})
		return detachErr
	}
	return out, detach, nil
}

func (c *WendyLiteClient) sendConsoleAttach(eventID uint32, duration time.Duration, blocking bool) error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_ConsoleAttach{
			ConsoleAttach: &wendypb.WendyComConsoleAttachParams{
				EventId:  eventID,
				Duration: uint32(duration / time.Millisecond),
				Blocking: blocking,
			},
		},
	}, 0)
	if err != nil {
		return err
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("device returned error: %w", err)
	}
	return nil
}

func (c *WendyLiteClient) sendConsoleDetach(eventID uint32) error {
	resp, err := c.sendCommand(&wendypb.WendyComCommand{
		RequestId: c.requestIdGen.Add(1),
		Params: &wendypb.WendyComCommand_ConsoleDetach{
			ConsoleDetach: &wendypb.WendyComConsoleDetachParams{EventId: eventID},
		},
	}, 0)
	if err != nil {
		return err
	}
	if err := resultToError(resp.Result); err != nil {
		return fmt.Errorf("device returned error: %w", err)
	}
	return nil
}

func resultToError(result wendypb.WendyComResult) error {
	switch result {
	case wendypb.WendyComResult_WENDY_COM_RESULT_OK:
		return nil
	case wendypb.WendyComResult_WENDY_COM_RESULT_BAD_PROTOCOL_VERSION:
		return errors.New("bad protocol version")
	case wendypb.WendyComResult_WENDY_COM_RESULT_BAD_STATE:
		return errors.New("bad state")
	case wendypb.WendyComResult_WENDY_COM_RESULT_BUSY:
		return errors.New("device busy")
	case wendypb.WendyComResult_WENDY_COM_RESULT_BAD_APP_TYPE:
		return errors.New("bad app type")
	case wendypb.WendyComResult_WENDY_COM_RESULT_BAD_APP_SIZE:
		return errors.New("bad app size")
	default: // includes WENDY_COM_RESULT_UNKNOWN_ERROR
		return fmt.Errorf("error %d", int32(result))
	}
}

func (c *WendyLiteClient) writeMessage(req *wendypb.WendyComMessage) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	msg := make([]byte, headerSize+len(body))
	msg[0] = headerMagic
	msg[1] = headerVersion
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(body)))
	copy(msg[headerSize:], body)
	if c.isSerial {
		msg = bytes.ReplaceAll(msg, []byte{esc}, []byte{esc, '_'})
	}
	// Commands may now be sent from multiple goroutines; keep frames whole.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	for len(msg) > 0 {
		n, err := c.conn.Write(msg)
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
		msg = msg[n:]
	}
	return nil
}

// roundTrip sends a message and synchronously reads the peer's reply message.
// Only valid before readLoop is started (i.e. during the handshake); once the
// read loop owns the connection, use sendCommand/subscribe instead.
func (c *WendyLiteClient) roundTrip(req *wendypb.WendyComMessage, timeout time.Duration) (*wendypb.WendyComMessage, error) {
	if err := c.writeMessage(req); err != nil {
		return nil, err
	}
	raw, err := c.readRawMessage(timeout)
	if err != nil {
		return nil, err
	}
	reply := &wendypb.WendyComMessage{}
	if err := proto.Unmarshal(raw, reply); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return reply, nil
}

// subscribe registers a waiter with the read loop. Messages matching filter
// are delivered to the returned subscription's channel until unsubscribe.
// Subscribers are notified in reverse order of subscription: the most recent
// subscriber receives messages first.
func (c *WendyLiteClient) subscribe(filter func(*wendypb.WendyComMessage) bool, capacity int) (*subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return nil, fmt.Errorf("connection lost: %w", c.readErr)
	}
	s := &subscription{filter: filter, ch: make(chan *wendypb.WendyComMessage, capacity)}
	c.subs = append(c.subs, s)
	return s, nil
}

// unsubscribe removes the subscriber and closes its channel. If failAll has
// already torn the subscription down (connection lost), the channel is already
// closed and must not be closed again — hence the channel is only closed here
// if the subscriber was actually removed.
func (c *WendyLiteClient) unsubscribe(s *subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.subs)
	c.subs = slices.DeleteFunc(c.subs, func(x *subscription) bool { return x == s })
	if len(c.subs) < n {
		close(s.ch)
	}
}

// readLoop receives every message from the device and hands it to the first
// subscriber whose filter matches. It runs from the end of the handshake
// until the connection dies.
func (c *WendyLiteClient) readLoop() {
	for {
		raw, err := c.readRawMessage(0)
		if err != nil {
			c.failAll(err)
			return
		}
		msg := &wendypb.WendyComMessage{}
		if err := proto.Unmarshal(raw, msg); err != nil {
			c.failAll(fmt.Errorf("unmarshal: %w", err))
			return
		}
		c.dispatch(msg)
	}
}

func (c *WendyLiteClient) dispatch(msg *wendypb.WendyComMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.subs) - 1; i >= 0; i-- {
		if s := c.subs[i]; s.filter(msg) {
			s.ch <- msg
			return
		}
	}
	// Events may legitimately trail their subscription (e.g. console chunks
	// after detach); logging them would dump raw payloads to stderr.
	if msg.GetEvent() != nil {
		return
	}
	log.Printf("wendycom: unhandled message from device: %v", msg)
}

// failAll marks the connection dead and wakes every pending subscriber by
// closing its channel.
func (c *WendyLiteClient) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readErr = err
	for _, s := range c.subs {
		close(s.ch)
	}
	c.subs = nil
}

// sendCommand subscribes for the matching response, sends the command, and
// waits for the response or the timeout. A timeout <= 0 means no deadline.
func (c *WendyLiteClient) sendCommand(cmd *wendypb.WendyComCommand, timeout time.Duration) (*wendypb.WendyComResponse, error) {
	// Subscribe before sending so a fast reply can't slip past us.
	sub, err := c.subscribe(func(m *wendypb.WendyComMessage) bool {
		r := m.GetResponse()
		return r != nil && r.RequestId == cmd.RequestId
	}, 1)
	if err != nil {
		return nil, err
	}
	defer c.unsubscribe(sub)

	if err := c.writeMessage(&wendypb.WendyComMessage{
		Msg: &wendypb.WendyComMessage_Command{Command: cmd},
	}); err != nil {
		return nil, err
	}

	var timer <-chan time.Time
	if timeout > 0 {
		timer = time.After(timeout)
	}
	select {
	case msg, ok := <-sub.ch:
		if !ok {
			return nil, fmt.Errorf("connection lost: %w", c.readErr)
		}
		return msg.GetResponse(), nil
	case <-timer:
		return nil, fmt.Errorf("timeout waiting for response to request %d", cmd.RequestId)
	}
}

// readRawMessage reads one framed message of any kind (response, event, handshake)
// and returns its raw body. A timeout <= 0 means no deadline.
func (c *WendyLiteClient) readRawMessage(timeout time.Duration) ([]byte, error) {
	header := make([]byte, headerSize)
	if err := c.readFull(header, timeout); err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if header[0] != headerMagic {
		return nil, fmt.Errorf("unexpected magic byte: 0x%02X", header[0])
	}
	if header[1] != headerVersion {
		return nil, fmt.Errorf("unexpected protocol version: 0x%02X", header[1])
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

func (c *WendyLiteClient) handshake() error {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("handshake id: %w", err)
	}
	id := binary.BigEndian.Uint32(b[:])
	reply, err := c.roundTrip(&wendypb.WendyComMessage{
		Msg: &wendypb.WendyComMessage_Handshake{
			Handshake: &wendypb.WendyComHandshake{
				HandshakeId: id,
				Version: &wendypb.WendyComProtocolVersion{
					Major: versionMajor,
					Minor: versionMinor,
				},
			},
		},
	}, 3*time.Second)
	if err != nil {
		return fmt.Errorf("device may not support protocol v%d: %w", versionMajor, err)
	}
	hs := reply.GetHandshake()
	switch {
	case hs == nil:
		return fmt.Errorf("unexpected reply message type")
	case hs.GetHandshakeId() != id:
		return fmt.Errorf("handshake ID mismatch")
	case hs.GetVersion().GetMajor() != versionMajor:
		return fmt.Errorf("unsupported device protocol version %d.%d",
			hs.GetVersion().GetMajor(), hs.GetVersion().GetMinor())
	}
	c.peerProtocolVersion = protocolVersion{Major: hs.GetVersion().GetMajor(), Minor: hs.GetVersion().GetMinor()}
	return nil
}
