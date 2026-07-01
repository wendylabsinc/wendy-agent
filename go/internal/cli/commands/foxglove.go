package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

const fgSubprotocol = "foxglove.websocket.v1"

// foxgloveSource is the slice of the ROS2 gRPC client the server depends on.
// Narrowed to an interface so tests can supply a fake without the full client.
type foxgloveSource interface {
	ListTopics(ctx context.Context, in *agentpbv2.ListROS2TopicsRequest, opts ...grpc.CallOption) (*agentpbv2.ListROS2TopicsResponse, error)
	GetMessageDefinition(ctx context.Context, in *agentpbv2.GetROS2MessageDefinitionRequest, opts ...grpc.CallOption) (*agentpbv2.GetROS2MessageDefinitionResponse, error)
	SubscribeRaw(ctx context.Context, in *agentpbv2.SubscribeRawROS2Request, opts ...grpc.CallOption) (grpc.ServerStreamingClient[agentpbv2.RawROS2Message], error)
}

// Compile-time assertion: the real generated client must satisfy foxgloveSource.
var _ foxgloveSource = (agentpbv2.ROS2ServiceClient)(nil)

type foxgloveServer struct {
	src      foxgloveSource
	domainID *int32
	topics   []string // explicit filter; empty = all
	// framePool recycles MESSAGE_DATA frame buffers across the high-rate
	// subscribe path to avoid a per-frame allocation. Holds *[]byte so the value
	// stored is pointer-sized (sync.Pool best practice).
	framePool sync.Pool
}

// getFrameBuf returns a recycled (or fresh) frame buffer, length reset to 0.
func (s *foxgloveServer) getFrameBuf() *[]byte {
	if v := s.framePool.Get(); v != nil {
		p := v.(*[]byte)
		*p = (*p)[:0]
		return p
	}
	b := make([]byte, 0, 64*1024)
	return &b
}

// putFrameBuf returns a frame buffer to the pool. Buffers larger than the gRPC
// ceiling are dropped rather than retained forever.
func (s *foxgloveServer) putFrameBuf(p *[]byte) {
	if cap(*p) <= 64*1024*1024 {
		s.framePool.Put(p)
	}
}

// discoverChannels lists topics (filtered) and fetches each message schema,
// assigning a stable channel id per topic. Channels whose schema fails to load
// are skipped (logged to stderr) so one bad topic does not block the rest.
func (s *foxgloveServer) discoverChannels(ctx context.Context) ([]fgChannel, map[uint32]string, error) {
	resp, err := s.src.ListTopics(ctx, &agentpbv2.ListROS2TopicsRequest{DomainId: s.domainID})
	if err != nil {
		return nil, nil, ros2RPCError(err)
	}
	allow := map[string]bool{}
	for _, t := range s.topics {
		allow[t] = true
	}
	names := make([]string, 0, len(resp.GetTopics()))
	for _, t := range resp.GetTopics() {
		if len(allow) == 0 || allow[t.GetName()] {
			names = append(names, t.GetName())
		}
	}
	sort.Strings(names)

	var channels []fgChannel
	chTopic := map[uint32]string{}
	var id uint32 = 1
	for _, name := range names {
		def, derr := s.src.GetMessageDefinition(ctx, &agentpbv2.GetROS2MessageDefinitionRequest{DomainId: s.domainID, Topic: name})
		if derr != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", name, ros2RPCError(derr))
			continue
		}
		channels = append(channels, fgChannel{
			ID: id, Topic: name, Encoding: "cdr",
			SchemaName: def.GetMessageType(), Schema: def.GetSchema(), SchemaEncoding: "ros2msg",
		})
		chTopic[id] = name
		id++
	}
	return channels, chTopic, nil
}

// handleConn drives one Foxglove client connection.
func (s *foxgloveServer) handleConn(ctx context.Context, c *websocket.Conn) {
	defer c.Close(websocket.StatusNormalClosure, "")

	// Serialize all writes through one goroutine (coder/websocket allows a
	// single concurrent writer).
	out := make(chan *[]byte, 64)
	outText := make(chan []byte, 64)
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-connCtx.Done():
				return
			case b := <-outText:
				if err := c.Write(connCtx, websocket.MessageText, b); err != nil {
					cancel()
					return
				}
			case p := <-out:
				err := c.Write(connCtx, websocket.MessageBinary, *p)
				s.putFrameBuf(p)
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// serverInfo then advertise.
	info, _ := json.Marshal(fgServerInfo{Op: "serverInfo", Name: "wendy", Capabilities: []string{}, SupportedEncodings: []string{"cdr"}})
	outText <- info
	channels, chTopic, err := s.discoverChannels(connCtx)
	if err != nil {
		c.Close(websocket.StatusInternalError, err.Error())
		cancel()
		wg.Wait()
		return
	}
	adv, _ := json.Marshal(fgAdvertise{Op: "advertise", Channels: channels})
	outText <- adv

	// subscriptionID -> cancel for its SubscribeRaw stream.
	subs := map[uint32]context.CancelFunc{}
	var subsMu sync.Mutex
	// dropped counts MESSAGE_DATA frames shed across all of this connection's
	// pump goroutines when the client can't drain the write queue fast enough.
	var dropped atomic.Uint64

	for {
		typ, data, rerr := c.Read(connCtx)
		if rerr != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		msg, perr := fgParseClientMessage(data)
		if perr != nil {
			continue
		}
		switch msg.Op {
		case "subscribe":
			for _, sub := range msg.Subscriptions {
				topic, ok := chTopic[sub.ChannelID]
				if !ok {
					continue
				}
				streamCtx, streamCancel := context.WithCancel(connCtx)
				subsMu.Lock()
				subs[sub.ID] = streamCancel
				subsMu.Unlock()
				wg.Add(1)
				go func(subID uint32, topic string, sctx context.Context) {
					defer wg.Done()
					s.pump(sctx, subID, topic, out, &dropped)
				}(sub.ID, topic, streamCtx)
			}
		case "unsubscribe":
			subsMu.Lock()
			for _, id := range msg.UnsubscribeIDs {
				if cancelFn, ok := subs[id]; ok {
					cancelFn()
					delete(subs, id)
				}
			}
			subsMu.Unlock()
		}
	}
	// Cancel any still-active subscription stream contexts so their child
	// context nodes are released immediately (mirror the unsubscribe path)
	// rather than lingering until GC.
	subsMu.Lock()
	for id, cancelFn := range subs {
		cancelFn()
		delete(subs, id)
	}
	subsMu.Unlock()
	cancel()
	wg.Wait()
}

// pump opens a SubscribeRaw stream and forwards each message as a binary frame.
// dropped accumulates frames shed when the shared write queue is full (slow
// client); see the send below.
func (s *foxgloveServer) pump(ctx context.Context, subID uint32, topic string, out chan<- *[]byte, dropped *atomic.Uint64) {
	stream, err := s.src.SubscribeRaw(ctx, &agentpbv2.SubscribeRawROS2Request{DomainId: s.domainID, Topic: topic})
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe %s: %v\n", topic, ros2RPCError(err))
		return
	}
	for {
		m, rerr := stream.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "stream %s ended: %v\n", topic, ros2RPCError(rerr))
			}
			return
		}
		ts := uint64(m.GetTimestampNs())
		if ts == 0 {
			ts = uint64(time.Now().UnixNano())
		}
		p := s.getFrameBuf()
		*p = fgAppendMessageData((*p)[:0], subID, ts, m.GetCdr())
		select {
		case <-ctx.Done():
			s.putFrameBuf(p)
			return
		case out <- p:
		default:
			s.putFrameBuf(p)
			// Write queue is full: the Foxglove client can't drain frames as fast
			// as the device produces them. Drop this frame rather than blocking —
			// blocking would pin this goroutine and its frame in memory, let gRPC
			// keep buffering upstream, and delay cancellation when the client goes
			// away. Live telemetry favours the freshest sample over a growing
			// backlog (Finding 6 — slow-client memory growth / DoS). Each frame is a
			// self-contained CDR message, so dropping one only lowers the effective
			// rate; it never corrupts the stream.
			if n := dropped.Add(1); n == 1 || n%1000 == 0 {
				fmt.Fprintf(os.Stderr, "foxglove: client too slow; dropped %d message(s) (most recent on %q)\n", n, topic)
			}
		}
	}
}

func newFoxgloveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "foxglove",
		Short: "Stream device ROS 2 data to Foxglove Studio",
		Long: `Bridge a device's live ROS 2 topics to Foxglove Studio.

'serve' hosts a Foxglove WebSocket Protocol server on your machine; connect
Foxglove Studio to ws://localhost:<port> via "Open connection".`,
	}
	cmd.AddCommand(newFoxgloveServeCmd())
	return cmd
}

func newFoxgloveServeCmd() *cobra.Command {
	var (
		port   int
		host   string
		domain int32
		topics []string
		poll   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a Foxglove WebSocket server bridging the device's ROS 2 topics",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			client, err := newROS2Client(ctx)
			if err != nil {
				return err
			}
			defer client.Close()

			srv := &foxgloveServer{src: client.client, domainID: ros2DomainPtr(domain), topics: topics}
			_ = poll // re-discovery loop is a follow-up; see plan note.

			httpSrv := &http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{fgSubprotocol}})
					if aerr != nil {
						return
					}
					if c.Subprotocol() != fgSubprotocol {
						c.Close(websocket.StatusProtocolError, "client must speak "+fgSubprotocol)
						return
					}
					srv.handleConn(r.Context(), c)
				}),
			}

			ln, lerr := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
			if lerr != nil {
				return lerr
			}
			fmt.Printf("Foxglove server listening on ws://%s — open this in Foxglove Studio\n", ln.Addr())

			go func() { <-ctx.Done(); _ = httpSrv.Close() }()
			if serr := httpSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
				return serr
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 8765, "WebSocket listen port")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Bind address")
	cmd.Flags().Int32Var(&domain, "domain", -1, "ROS_DOMAIN_ID override (default: from the app's ros2 config)")
	cmd.Flags().StringSliceVar(&topics, "topic", nil, "Restrict to these topics (repeatable; default: all)")
	cmd.Flags().DurationVar(&poll, "poll", 5*time.Second, "Topic re-discovery interval (0 disables; reserved for follow-up)")
	return cmd
}
