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
	// Parameters (P2).
	ListParams(ctx context.Context, in *agentpbv2.ListROS2ParamsRequest, opts ...grpc.CallOption) (*agentpbv2.ListROS2ParamsResponse, error)
	GetParam(ctx context.Context, in *agentpbv2.GetROS2ParamRequest, opts ...grpc.CallOption) (*agentpbv2.GetROS2ParamResponse, error)
	SetParam(ctx context.Context, in *agentpbv2.SetROS2ParamRequest, opts ...grpc.CallOption) (*agentpbv2.SetROS2ParamResponse, error)
	// Services (P2) + publish (P3).
	ListServices(ctx context.Context, in *agentpbv2.ListROS2ServicesRequest, opts ...grpc.CallOption) (*agentpbv2.ListROS2ServicesResponse, error)
	GetServiceDefinition(ctx context.Context, in *agentpbv2.GetROS2ServiceDefinitionRequest, opts ...grpc.CallOption) (*agentpbv2.GetROS2ServiceDefinitionResponse, error)
	CallService(ctx context.Context, in *agentpbv2.CallROS2ServiceRequest, opts ...grpc.CallOption) (*agentpbv2.CallROS2ServiceResponse, error)
	Publish(ctx context.Context, in *agentpbv2.PublishROS2Request, opts ...grpc.CallOption) (*agentpbv2.PublishROS2Response, error)
}

// Compile-time assertion: the real generated client must satisfy foxgloveSource.
var _ foxgloveSource = (agentpbv2.ROS2ServiceClient)(nil)

type foxgloveServer struct {
	src      foxgloveSource
	domainID *int32
	topics   []string // explicit filter; empty = all
	// allowControl enables the write path — service calls, client publishing, and
	// setParameters. Default false: the bridge is read-only, matching P1. When
	// false these capabilities are neither advertised nor honoured.
	allowControl bool
	// poll is the topic re-discovery interval; 0 disables re-discovery.
	poll time.Duration
	// framePool recycles MESSAGE_DATA frame buffers across the high-rate
	// subscribe path to avoid a per-frame allocation. Holds *[]byte so the value
	// stored is pointer-sized (sync.Pool best practice).
	framePool sync.Pool
}

// capabilities returns the Foxglove serverInfo capability list for this server.
// Read capabilities (getParameters via "parameters"/"parametersSubscribe") are
// always present; the write capabilities are added only with --allow-control.
func (s *foxgloveServer) capabilities() []string {
	caps := []string{"parameters", "parametersSubscribe"}
	if s.allowControl {
		caps = append(caps, "services", "clientPublish")
	}
	return caps
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

// discoverTopicNames lists the device's topics, applies the --topic filter, and
// returns them sorted.
func (s *foxgloveServer) discoverTopicNames(ctx context.Context) ([]string, error) {
	resp, err := s.src.ListTopics(ctx, &agentpbv2.ListROS2TopicsRequest{DomainId: s.domainID})
	if err != nil {
		return nil, ros2RPCError(err)
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
	return names, nil
}

// channelForTopic fetches a topic's message schema and builds its channel with
// the given id. ok is false (and the reason is logged) if the schema can't load,
// so one bad topic never blocks the rest.
func (s *foxgloveServer) channelForTopic(ctx context.Context, name string, id uint32) (fgChannel, bool) {
	def, derr := s.src.GetMessageDefinition(ctx, &agentpbv2.GetROS2MessageDefinitionRequest{DomainId: s.domainID, Topic: name})
	if derr != nil {
		fmt.Fprintf(os.Stderr, "skipping %s: %v\n", name, ros2RPCError(derr))
		return fgChannel{}, false
	}
	return fgChannel{
		ID: id, Topic: name, Encoding: "cdr",
		SchemaName: def.GetMessageType(), Schema: def.GetSchema(), SchemaEncoding: "ros2msg",
	}, true
}

// discoverChannels lists topics (filtered) and builds a channel per topic,
// assigning ids from 1. Returns the channels and the id->topic map.
func (s *foxgloveServer) discoverChannels(ctx context.Context) ([]fgChannel, map[uint32]string, error) {
	names, err := s.discoverTopicNames(ctx)
	if err != nil {
		return nil, nil, err
	}
	var channels []fgChannel
	chTopic := map[uint32]string{}
	var id uint32 = 1
	for _, name := range names {
		ch, ok := s.channelForTopic(ctx, name, id)
		if !ok {
			continue
		}
		channels = append(channels, ch)
		chTopic[id] = name
		id++
	}
	return channels, chTopic, nil
}

// rediscoverLoop periodically re-lists topics and reconciles the advertised set:
// topics that appeared are advertised (with fresh ids), topics that vanished are
// unadvertised. Ids for surviving topics stay stable. The channel maps are
// shared with the read loop and guarded by mu. Runs until ctx is cancelled.
func (s *foxgloveServer) rediscoverLoop(ctx context.Context, outText chan<- []byte, mu *sync.Mutex, chTopic map[uint32]string, topicID map[string]uint32, nextID *uint32) {
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		names, err := s.discoverTopicNames(ctx)
		if err != nil {
			continue // transient; try again next tick
		}
		current := make(map[string]bool, len(names))
		for _, n := range names {
			current[n] = true
		}

		// Additions: topics present now but not advertised.
		var added []fgChannel
		for _, name := range names {
			mu.Lock()
			_, have := topicID[name]
			mu.Unlock()
			if have {
				continue
			}
			mu.Lock()
			id := *nextID
			*nextID++
			mu.Unlock()
			ch, ok := s.channelForTopic(ctx, name, id)
			if !ok {
				continue
			}
			mu.Lock()
			chTopic[id] = name
			topicID[name] = id
			mu.Unlock()
			added = append(added, ch)
		}

		// Removals: advertised topics no longer present.
		var removed []uint32
		mu.Lock()
		for id, name := range chTopic {
			if !current[name] {
				removed = append(removed, id)
				delete(chTopic, id)
				delete(topicID, name)
			}
		}
		mu.Unlock()

		if len(added) > 0 {
			if b, mErr := json.Marshal(fgAdvertise{Op: "advertise", Channels: added}); mErr == nil {
				select {
				case outText <- b:
				case <-ctx.Done():
					return
				}
			}
		}
		if len(removed) > 0 {
			if b, mErr := json.Marshal(fgUnadvertise{Op: "unadvertise", ChannelIDs: removed}); mErr == nil {
				select {
				case outText <- b:
				case <-ctx.Done():
					return
				}
			}
		}
	}
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
	info, _ := json.Marshal(fgServerInfo{Op: "serverInfo", Name: "wendy", Capabilities: s.capabilities(), SupportedEncodings: []string{"cdr"}})
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

	// chMu guards the channel maps + id counter, which the read loop (subscribe)
	// and the poll re-discovery goroutine both touch. topicID is the reverse of
	// chTopic; nextID keeps advertised ids stable and monotonic across polls.
	var chMu sync.Mutex
	topicID := make(map[string]uint32, len(chTopic))
	var nextID uint32
	for id, name := range chTopic {
		topicID[name] = id
		if id >= nextID {
			nextID = id + 1
		}
	}

	// Services (write path): discovered + advertised only with --allow-control.
	var svcInfo map[uint32]*fgServiceInfo
	if s.allowControl {
		var services []fgService
		services, svcInfo = s.discoverServices(connCtx)
		if len(services) > 0 {
			if b, mErr := json.Marshal(fgAdvertiseServices{Op: "advertiseServices", Services: services}); mErr == nil {
				outText <- b
			}
		}
	}

	// subscriptionID -> cancel for its SubscribeRaw stream.
	subs := map[uint32]context.CancelFunc{}
	var subsMu sync.Mutex
	// Client-advertised channels for publishing (write path).
	clientChannels := map[uint32]*fgClientChannel{}
	// dropped counts MESSAGE_DATA frames shed across all of this connection's
	// pump goroutines when the client can't drain the write queue fast enough.
	var dropped atomic.Uint64

	// Periodic topic re-discovery: advertise topics that appear and unadvertise
	// those that vanish, keeping ids stable for survivors. 0 disables.
	if s.poll > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.rediscoverLoop(connCtx, outText, &chMu, chTopic, topicID, &nextID)
		}()
	}

	for {
		typ, data, rerr := c.Read(connCtx)
		if rerr != nil {
			break
		}
		if typ == websocket.MessageBinary {
			// Write-path binary frames: SERVICE_CALL_REQUEST (0x02) and client
			// CLIENT_MESSAGE_DATA (0x01). Ignored unless --allow-control.
			if !s.allowControl || len(data) == 0 {
				continue
			}
			switch data[0] {
			case 0x02:
				frame := append([]byte(nil), data...)
				wg.Add(1)
				go func() { defer wg.Done(); s.handleServiceCall(connCtx, frame, svcInfo, out, outText) }()
			case 0x01:
				frame := append([]byte(nil), data...)
				wg.Add(1)
				go func() { defer wg.Done(); s.handleClientPublish(connCtx, frame, clientChannels) }()
			}
			continue
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
				chMu.Lock()
				topic, ok := chTopic[sub.ChannelID]
				chMu.Unlock()
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
		case "getParameters":
			values := s.getParameters(connCtx, msg.ParameterNames, msg.ID)
			if b, mErr := json.Marshal(values); mErr == nil {
				outText <- b
			}
		case "setParameters":
			// Writing parameters can change device behaviour; gated behind
			// --allow-control (Foxglove should not advertise the capability when
			// disabled, but ignore defensively regardless).
			if !s.allowControl {
				fmt.Fprintln(os.Stderr, "foxglove: setParameters ignored (start with --allow-control to enable writes)")
				continue
			}
			values := s.setParameters(connCtx, msg.Parameters, msg.ID)
			// Only reply when the client supplied an id (Foxglove correlates the
			// echo by id); an id-less setParameters is fire-and-forget.
			if msg.ID != "" {
				if b, mErr := json.Marshal(values); mErr == nil {
					outText <- b
				}
			}
		case "subscribeParameterUpdates":
			// Emit the current values immediately; ongoing updates are delivered by
			// the --poll re-discovery loop.
			values := s.getParameters(connCtx, msg.ParameterNames, "")
			if b, mErr := json.Marshal(values); mErr == nil {
				outText <- b
			}
		case "advertise":
			// Client publish channels. Ignored unless --allow-control.
			if !s.allowControl {
				continue
			}
			for i := range msg.ClientChannels {
				ch := msg.ClientChannels[i]
				clientChannels[ch.ID] = &ch
			}
		case "unadvertise":
			for _, id := range msg.ClientUnadvertiseIDs {
				delete(clientChannels, id)
			}
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
		port         int
		host         string
		domain       int32
		topics       []string
		poll         time.Duration
		allowControl bool
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

			srv := &foxgloveServer{src: client.client, domainID: ros2DomainPtr(domain), topics: topics, allowControl: allowControl, poll: poll}

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
	cmd.Flags().DurationVar(&poll, "poll", 5*time.Second, "Topic re-discovery interval (0 disables)")
	cmd.Flags().BoolVar(&allowControl, "allow-control", false, "Enable Foxglove to command the device: publish to topics, call services, and set parameters (default: read-only)")
	return cmd
}
