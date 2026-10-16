//go:build linux

package bleprovider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"go.uber.org/zap"
)

type runtime struct {
	cfg                  Config
	uuid                 string
	owner                string
	hciIndex             int
	ownerLookup          func(context.Context, *dbus.Conn) (string, error)
	bus                  *dbus.Conn
	freshness            *advertisementFreshness
	mu                   sync.Mutex
	active               map[meshid.Device]struct{}
	nextAttempt          map[meshid.Device]time.Time
	links                sync.WaitGroup
	dialSlot             chan struct{}
	serverTLS            *tls.Config
	advertisementRefresh chan struct{}
	tlsGate              tlsHandshakeAdmission
	discovery            discoveryRecovery
	restartScan          func(context.Context, *dbus.Conn, dbus.ObjectPath) error
}

type cheaperLinkChecker interface {
	HasCheaperLink(device meshid.Device, cost uint16) bool
}

func (r *runtime) hasCheaperLink(device meshid.Device) bool {
	if r.cfg.Selection != nil {
		return !r.cfg.Selection.AllowRadio(device, localmesh.RadioBLE)
	}
	checker, ok := r.cfg.Node.(cheaperLinkChecker)
	return ok && checker.HasCheaperLink(device, LinkCost)
}

func (r *runtime) watchCheaperLink(ctx context.Context, device meshid.Device, conn net.Conn) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if r.hasCheaperLink(device) {
					r.cfg.Logger.Debug("BLE closing CoC on cheaper-link decision", zap.Stringer("peer", device.UUID()))
					_ = conn.Close()
					return
				}
			}
		}
	}()
	return func() { close(stop) }
}

func (r *runtime) claim(device meshid.Device) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.active[device]; exists || len(r.active) >= r.cfg.TargetPeers {
		return false
	}
	r.active[device] = struct{}{}
	return true
}
func (r *runtime) release(device meshid.Device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, device)
	r.nextAttempt[device] = time.Now().Add(5 * time.Second)
}
func (r *runtime) shouldDial(device meshid.Device) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, active := r.active[device]
	return !active && len(r.active) < r.cfg.TargetPeers && !time.Now().Before(r.nextAttempt[device])
}

// Run advertises, scans, listens for CoC channels, and initiates channels to
// lower device UUIDs concurrently. It blocks until ctx ends. BlueZ data is
// only a discovery hint: every link requires a Wendy device certificate pinned
// to the expected tenant and peer device before AttachStream is called.
func Run(ctx context.Context, cfg Config) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if err := cfg.defaults(); err != nil {
		return err
	}
	uuid := ServiceUUID(cfg.Credentials.Device.Tenant())
	if uuid == "" {
		return errors.New("BLE requires a tenant UUID")
	}
	adv, err := NewAdvertisement(cfg.Credentials.Device.UUID(), cfg.MeshName, cfg.PSM)
	if err != nil {
		return err
	}
	payload, _ := adv.MarshalBinary()
	listener, err := listenL2CAP(cfg.PSM)
	if err != nil {
		return err
	}
	defer listener.Close()
	bus, closeTransport, err := connectProviderBus(runCtx)
	if err != nil {
		return fmt.Errorf("BlueZ system bus: %w", err)
	}
	stopBusGuard := guardProviderBusShutdown(runCtx, 3*time.Second, closeTransport)
	defer func() { _ = closeTransport(); _ = bus.Close(); stopBusGuard() }()
	owner, err := bluezOwner(runCtx, bus)
	if err != nil {
		return err
	}
	adapter, err := selectAdapter(runCtx, bus, cfg.AdapterPath)
	if err != nil {
		return err
	}
	freshness := newAdvertisementFreshness(adapter, uuid, cfg.MeshName, cfg.Credentials.Device)
	stopWatching, err := watchAdvertisements(runCtx, bus, owner, freshness)
	if err != nil {
		return err
	}
	defer func() { cancelRun(); stopWatching() }()
	if err = setupBlueZ(runCtx, bus, adapter, uuid, payload); err != nil {
		return err
	}
	defer func() { cancelRun(); cleanupBlueZ(bus, adapter, cfg.Logger) }()
	// A daemon restart during setup loses the registration before the scan
	// begins. The outer carrier supervisor retries with a fresh bus.
	currentOwner, err := bluezOwner(runCtx, bus)
	if err != nil {
		return err
	}
	if err := verifyBlueZOwner(owner, currentOwner); err != nil {
		return err
	}
	hciIndex, parseErr := adapterHCIIndex(adapter)
	if parseErr != nil {
		cfg.Logger.Debug("BLE connection interval tuning unavailable", zap.Error(parseErr))
	}
	r := &runtime{cfg: cfg, uuid: uuid, owner: owner, hciIndex: hciIndex, ownerLookup: bluezOwner, bus: bus, freshness: freshness, active: make(map[meshid.Device]struct{}), nextAttempt: make(map[meshid.Device]time.Time), dialSlot: make(chan struct{}, 1), tlsGate: newTLSHandshakeAdmission()}
	r.serverTLS = r.makeServerTLS()
	r.advertisementRefresh = make(chan struct{}, 1)
	var loops sync.WaitGroup
	loopErrors := make(chan error, 3)
	loops.Add(3)
	go func() { defer loops.Done(); loopErrors <- r.acceptLoop(runCtx, listener) }()
	go func() { defer loops.Done(); loopErrors <- r.scanLoop(runCtx, bus, adapter) }()
	go func() {
		defer loops.Done()
		loopErrors <- runAdvertisementRefresh(runCtx, r.advertisementRefresh, advertisementRefreshInterval, func(ctx context.Context) error {
			return refreshOwnedAdvertisement(ctx, bus, closeTransport, owner, uuid, payload)
		})
	}()
	var loopErr error
	select {
	case <-ctx.Done():
	case loopErr = <-loopErrors:
		if loopErr == nil && ctx.Err() == nil {
			loopErr = errors.New("BLE provider loop exited unexpectedly")
		}
	}
	cancelRun()
	_ = listener.Close()
	loops.Wait()
	r.links.Wait()
	return loopErr
}

func (r *runtime) acceptLoop(ctx context.Context, listener *l2Listener) error {
	sem := make(chan struct{}, 8)
	for ctx.Err() == nil {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("BLE L2CAP accept: %w", err)
		}
		select {
		case sem <- struct{}{}:
			r.links.Add(1)
			go func() { defer r.links.Done(); defer func() { <-sem }(); r.acceptLink(ctx, conn) }()
		default:
			_ = conn.Close()
		}
	}
	return nil
}

func (r *runtime) makeServerTLS() *tls.Config {
	config := &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		// A hybrid ML-KEM key share expands the first ClientHello beyond six
		// 240-byte CoC SDUs on tested controllers. A single lost fragment stalls
		// the whole TLS flight. BLE uses classical X25519 key exchange while the
		// enrolled mTLS certificate and signature verification remain unchanged.
		CurvePreferences: []tls.CurveID{tls.X25519},
		Certificates:     []tls.Certificate{r.cfg.Credentials.Certificate},
		ClientAuth:       tls.RequireAnyClientCert, NextProtos: []string{ALPN},
		VerifyConnection: func(state tls.ConnectionState) error {
			chain := make([][]byte, 0, len(state.PeerCertificates))
			for _, cert := range state.PeerCertificates {
				chain = append(chain, cert.Raw)
			}
			id, err := r.cfg.Credentials.Verify(chain, time.Now())
			if err != nil {
				return err
			}
			if id.Device.Tenant() != r.cfg.Credentials.Device.Tenant() {
				return errors.New("BLE inbound peer is outside the tenant")
			}
			if meshid.Compare(r.cfg.Credentials.Device, id.Device) >= 0 {
				return errors.New("BLE inbound peer must be a higher device in this tenant")
			}
			return nil
		},
	}
	r.cfg.Credentials.ServerTicketStore(ALPN, "ble-tls").Configure(config)
	return config
}

func (r *runtime) acceptLink(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	stopWatch := watchLinkContext(ctx, raw)
	defer stopWatch()
	measured, meter := meterHandshake(raw, r.cfg.Logger)
	secure := tls.Server(measured, r.serverTLS)
	meter.start()
	wait, elapsed, err := r.tlsGate.run(ctx, meshTLSAdmissionWait, meshTLSHandshakeTimeout, secure.HandshakeContext)
	measurement := meter.finish()
	state := secure.ConnectionState()
	var peer meshid.Device
	if err == nil && state.NegotiatedProtocol == ALPN {
		chain := make([][]byte, 0, len(state.PeerCertificates))
		for _, cert := range state.PeerCertificates {
			chain = append(chain, cert.Raw)
		}
		if id, verifyErr := r.cfg.Credentials.Verify(chain, time.Now()); verifyErr == nil && id.Device.Tenant() == r.cfg.Credentials.Device.Tenant() && meshid.Compare(r.cfg.Credentials.Device, id.Device) < 0 {
			peer = id.Device
		}
	}
	measurement.log(r.cfg.Logger, "inbound", peer, state, err == nil && state.NegotiatedProtocol == ALPN && peer != (meshid.Device{}))
	if err != nil || state.NegotiatedProtocol != ALPN || peer == (meshid.Device{}) {
		r.cfg.Logger.Warn("BLE inbound TLS rejected", zap.Duration("admission_wait", wait), zap.Duration("handshake_time", elapsed), zap.Error(err))
		_ = secure.Close()
		return
	}
	if r.cfg.Selection != nil {
		r.cfg.Selection.Connected(peer, localmesh.RadioBLE)
	}
	r.cfg.Logger.Info("BLE inbound TLS established", zap.Stringer("peer", peer.UUID()), zap.Duration("admission_wait", wait), zap.Duration("handshake_time", elapsed), zap.Bool("resumed", state.DidResume))
	if r.hasCheaperLink(peer) {
		r.cfg.Logger.Debug("BLE closing inbound CoC on cheaper-link decision", zap.Stringer("peer", peer.UUID()))
		_ = secure.Close()
		return
	}
	if !r.claim(peer) {
		r.cfg.Logger.Debug("BLE closing inbound CoC on claim conflict", zap.Stringer("peer", peer.UUID()))
		_ = secure.Close()
		return
	}
	defer r.release(peer)
	defer secure.Close()
	r.requestAdvertisementRefresh()
	stopCheaperWatch := r.watchCheaperLink(ctx, peer, secure)
	defer stopCheaperWatch()
	if err = r.cfg.Node.AttachStream(ctx, peer, secure, LinkCost); err != nil && ctx.Err() == nil {
		r.cfg.Logger.Debug("BLE link ended", zap.Stringer("peer", peer.UUID()), zap.Error(err))
	}
}

func (r *runtime) scanLoop(ctx context.Context, bus *dbus.Conn, adapter dbus.ObjectPath) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	consecutiveErrors := 0
	for {
		if err := r.scanOnce(ctx, bus, adapter); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, errBlueZOwnerChanged) {
				return err
			}
			consecutiveErrors++
			r.cfg.Logger.Warn("BLE discovery unavailable", zap.Error(err), zap.Int("consecutive_errors", consecutiveErrors))
			if consecutiveErrors >= 3 {
				return fmt.Errorf("BlueZ discovery failed three times: %w", err)
			}
		} else {
			consecutiveErrors = 0
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *runtime) scanOnce(ctx context.Context, bus *dbus.Conn, adapter dbus.ObjectPath) error {
	query, cancel := context.WithTimeout(ctx, 3*time.Second)
	lookup := r.ownerLookup
	if lookup == nil {
		lookup = bluezOwner
	}
	owner, err := lookup(query, bus)
	if err != nil {
		cancel()
		return err
	}
	if err := verifyBlueZOwner(r.owner, owner); err != nil {
		cancel()
		return err
	}
	objects, err := getManaged(query, bus)
	cancel()
	if err != nil {
		return err
	}
	now := time.Now()
	peers := r.discoveredCandidates(objects, now)
	for _, peer := range peers {
		if r.hasCheaperLink(peer.device) {
			continue
		}
		if !r.shouldDial(peer.device) {
			continue
		}
		if !r.claim(peer.device) {
			continue
		}
		r.links.Add(1)
		go func(peer candidate) { defer r.links.Done(); defer r.release(peer.device); r.dialLink(ctx, peer) }(peer)
	}
	r.maybeRestartDiscovery(ctx, bus, adapter, now)
	return nil
}

func (r *runtime) discoveredCandidates(objects managedObjects, at time.Time) []candidate {
	// Device1.ServiceData is retained by BlueZ after an advertiser stops.
	// Only live ServiceData discovery signals may refresh radio selection or
	// initiate a new CoC dial.
	peers, newlySeen := r.freshness.candidates(objects, at)
	if len(newlySeen) > 0 {
		r.discovery.observed(at)
	}
	if r.cfg.Selection != nil {
		for _, device := range newlySeen {
			r.cfg.Selection.Seen(device, localmesh.RadioBLE)
		}
	}
	return peers
}

func (r *runtime) missingPeerSlots() bool {
	if r.cfg.TargetPeers <= 0 {
		return false
	}
	devices := make(map[meshid.Device]struct{}, r.cfg.TargetPeers)
	r.mu.Lock()
	for device := range r.active {
		devices[device] = struct{}{}
	}
	r.mu.Unlock()
	if node, ok := r.cfg.Node.(interface{ Snapshot() localmesh.NodeSnapshot }); ok {
		for _, link := range node.Snapshot().Links {
			devices[link.Device] = struct{}{}
		}
	}
	return len(devices) < r.cfg.TargetPeers
}

func (r *runtime) maybeRestartDiscovery(ctx context.Context, bus *dbus.Conn, adapter dbus.ObjectPath, now time.Time) {
	if !r.discovery.restartDue(now, r.missingPeerSlots()) {
		return
	}
	restart := r.restartScan
	if restart == nil {
		restart = restartBlueZDiscovery
	}
	restartCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	err := restart(restartCtx, bus, adapter)
	cancel()
	if err != nil {
		r.cfg.Logger.Warn("BLE discovery restart failed", zap.Duration("mesh_advert_silence", now.Sub(r.discovery.lastMeshSignal)), zap.Error(err))
		return
	}
	r.cfg.Logger.Info("BLE discovery restarted after mesh advertisement silence", zap.Duration("mesh_advert_silence", now.Sub(r.discovery.lastMeshSignal)))
}

func (r *runtime) dialLink(ctx context.Context, peer candidate) {
	if r.hasCheaperLink(peer.device) {
		r.cfg.Logger.Debug("BLE closing outbound CoC on cheaper-link decision", zap.Stringer("peer", peer.device.UUID()))
		return
	}
	// Serializing outgoing LE creation avoids accumulating simultaneous pending
	// ACLs while the adapter scans and advertises for other mesh peers.
	select {
	case r.dialSlot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			<-r.dialSlot
		}
	}()
	if r.hasCheaperLink(peer.device) || ctx.Err() != nil {
		return
	}
	// Multiple BlueZ advertisements share the controller's advertising time.
	// A BLE CoC connect can wait for the peer's next connectable advertisement
	// while the local adapter scans and advertises at the same time.
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := dialL2CAP(dialCtx, peer.address, peer.addressType, peer.psm)
	cancel()
	if err != nil {
		// A timed-out socket connect can leave a controller attempt pending.
		// The management command resolves the LE address on this adapter and
		// aborts that peer's pending kernel hci_conn. Device1.Disconnect does
		// not reliably cancel raw L2CAP socket connects in BT_CONNECT state.
		if shouldCancelPendingACL(err, ctx.Err(), r.hciIndex) {
			abortCtx, stopAbort := context.WithTimeout(ctx, 3*time.Second)
			abortErr := disconnectLEPeer(abortCtx, r.hciIndex, peer.address, peer.addressType)
			stopAbort()
			if abortErr != nil {
				r.cfg.Logger.Warn("BLE timed-out peer ACL cancel failed", zap.Stringer("peer", peer.device.UUID()), zap.Error(abortErr))
			} else {
				r.cfg.Logger.Info("BLE timed-out peer ACL cancel completed", zap.Stringer("peer", peer.device.UUID()))
			}
		} else {
			r.disconnectOwnedPeer(peer)
		}
		r.cfg.Logger.Debug("BLE CoC dial failed", zap.Stringer("peer", peer.device.UUID()), zap.Error(err))
		return
	}
	<-r.dialSlot
	slotHeld = false
	// Closing the CoC socket alone can leave its LE ACL held by BlueZ. The
	// higher device UUID dialer owns this link and releases that specific peer
	// after every failed attempt, route suppression, or provider shutdown.
	defer func() {
		if raw != nil {
			_ = raw.Close()
		}
		r.disconnectOwnedPeer(peer)
	}()
	// Capture the ACL identity before the context watcher can close/reuse the
	// CoC descriptor. The early update is limited to this Wendy CoC's ACL.
	aclHandle, handleErr := meshACLHandle(raw)
	stopWatch := watchLinkContext(ctx, raw)
	defer stopWatch()
	var secure *tls.Conn
	defer func() {
		if secure != nil {
			_ = secure.Close()
		}
	}()
	// Complete the initial controller procedure before TLS firstflight. An
	// accepted HCI update cannot be cancelled by cancelling its caller; any
	// uncertain result closes this attempt without sending TLS bytes.
	tuneStarted := time.Now()
	interval, untuned, tuneErr := tuneMeshBeforeTLS(ctx, func(updateCtx context.Context) (time.Duration, error) {
		if handleErr != nil {
			return 0, &leUpdateUnavailableError{err: handleErr}
		}
		return requestMeshConnectionInterval(updateCtx, r.hciIndex, aclHandle)
	})
	r.cfg.Logger.Info("BLE initial tune barrier completed", zap.Stringer("peer", peer.device.UUID()),
		zap.Duration("wait", time.Since(tuneStarted)), zap.Duration("interval", interval),
		zap.Bool("untuned_fallback", untuned), zap.Error(tuneErr))
	if tuneErr != nil && !untuned {
		return
	}
	cfg, err := r.cfg.Credentials.PeerTLSWithTickets(peer.device, ALPN, "ble-tls")
	if err != nil {
		return
	}
	// PeerTLSWithTickets returns a clone, so this BLE-only choice cannot alter
	// the QUIC curve preferences used over NAN, LAN, or TCP links.
	cfg.CurvePreferences = []tls.CurveID{tls.X25519}
	measured, meter := meterHandshake(raw, r.cfg.Logger)
	secure = tls.Client(measured, cfg)
	meter.start()
	wait, elapsed, err := r.tlsGate.run(ctx, meshTLSAdmissionWait, meshTLSHandshakeTimeout, secure.HandshakeContext)
	measurement := meter.finish()
	measurement.log(r.cfg.Logger, "outbound", peer.device, secure.ConnectionState(), err == nil && secure.ConnectionState().NegotiatedProtocol == ALPN)
	if err != nil || secure.ConnectionState().NegotiatedProtocol != ALPN {
		r.cfg.Logger.Warn("BLE outbound TLS rejected", zap.Stringer("peer", peer.device.UUID()), zap.Duration("admission_wait", wait), zap.Duration("handshake_time", elapsed), zap.Error(err))
		return
	}
	if r.cfg.Selection != nil {
		r.cfg.Selection.Connected(peer.device, localmesh.RadioBLE)
	}
	r.cfg.Logger.Info("BLE outbound TLS established", zap.Stringer("peer", peer.device.UUID()), zap.Duration("admission_wait", wait), zap.Duration("handshake_time", elapsed), zap.Bool("resumed", secure.ConnectionState().DidResume))
	if r.hasCheaperLink(peer.device) {
		r.cfg.Logger.Debug("BLE closing outbound CoC on cheaper-link decision", zap.Stringer("peer", peer.device.UUID()))
		return
	}
	stopCheaperWatch := r.watchCheaperLink(ctx, peer.device, secure)
	defer stopCheaperWatch()
	err = r.cfg.Node.AttachStream(ctx, peer.device, secure, LinkCost)
	if err != nil && ctx.Err() == nil {
		r.cfg.Logger.Debug("BLE link ended", zap.Stringer("peer", peer.device.UUID()), zap.Error(err))
	}
}

func shouldCancelPendingACL(dialErr, providerErr error, hciIndex int) bool {
	return errors.Is(dialErr, context.DeadlineExceeded) && providerErr == nil && hciIndex >= 0
}

func (r *runtime) disconnectOwnedPeer(peer candidate) {
	if r.bus == nil || !peer.path.IsValid() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.bus.Object(bluezName, peer.path).CallWithContext(ctx, deviceInterface+".Disconnect", 0).Err; err != nil {
		r.cfg.Logger.Debug("BLE owned ACL disconnect unavailable", zap.Stringer("peer", peer.device.UUID()), zap.Error(err))
	}
}

// The inbound peer's TLS deadline remains ten seconds from acceptance.
// Initial tuning consumes at most four seconds of that existing budget;
// no post-authentication update can race its stream-hello deadline.
const meshInitialTuneBudget = 4 * time.Second

func tuneMeshBeforeTLS(ctx context.Context, tune func(context.Context) (time.Duration, error)) (time.Duration, bool, error) {
	updateCtx, cancel := context.WithTimeout(ctx, meshInitialTuneBudget)
	defer cancel()
	interval, err := tune(updateCtx)
	if ctx.Err() != nil {
		return 0, false, ctx.Err()
	}
	var unavailable *leUpdateUnavailableError
	return interval, errors.As(err, &unavailable), err
}

func watchLinkContext(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}
