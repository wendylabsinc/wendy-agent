//go:build linux

package localmesh

import (
	"crypto/ecdsa"
	"errors"
	"testing"
	"time"
)

type bundleGateLink struct {
	*teardownTestLink
	entered chan struct{}
	release chan struct{}
}

func (l *bundleGateLink) WriteControl(ControlMessage) error {
	close(l.entered)
	<-l.release
	return nil
}

func TestPendingIdentityRepairDoesNotFloodSlowLink(t *testing.T) {
	self, peer := testCredentials(t)
	link := &bundleGateLink{teardownTestLink: &teardownTestLink{done: make(chan struct{})}, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-link.release:
		default:
			close(link.release)
		}
	})
	cache, err := OpenIdentityCache("", DefaultCacheLimits(), self.Verify, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Put(self.Certificate.Certificate, time.Now()); err != nil {
		t.Fatal(err)
	}
	n := &Node{cache: cache, bundleHints: newBundleHintCache()}
	p := &nodePeer{device: peer.Device, link: link, control: make(chan ControlMessage, 4)}
	bundle := ControlMessage{Kind: "bundle", Bundle: self.Certificate.Certificate}
	n.queue(p, []ControlMessage{bundle})
	writing := <-p.control
	done := make(chan error, 1)
	go func() { done <- n.writePeerControl(p, writing) }()
	select {
	case <-link.entered:
	case err := <-done:
		t.Fatalf("trusted bundle did not reach transport: %v", err)
	case <-time.After(time.Second):
		t.Fatal("trusted bundle writer stalled")
	}
	for i := 0; i < 100; i++ {
		n.queue(p, []ControlMessage{bundle})
	}
	if len(p.control) != 0 {
		t.Fatal("duplicate certificate copies accumulated during the write")
	}
	select {
	case <-link.done:
		t.Fatal("repeated cache repair closed the link")
	default:
	}
	n.queue(p, []ControlMessage{{Kind: "manifest"}})
	if (<-p.control).Kind != "manifest" {
		t.Fatal("cache repair displaced a route proof")
	}
	close(link.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(n.bundleHints.Known(peer.Device, time.Now())) != 1 {
		t.Fatal("completed write was not recorded")
	}
	// A later request can represent a leaf reboot and must repair it again.
	n.queue(p, []ControlMessage{bundle})
	if len(p.control) != 1 {
		t.Fatal("completed bundle suppressed a later cache-repair request")
	}
}

type bundleCaptureLink struct {
	*teardownTestLink
	written []ControlMessage
	err     error
}

func (l *bundleCaptureLink) WriteControl(m ControlMessage) error {
	l.written = append(l.written, m)
	return l.err
}

func TestPendingBundleRenewalClearsOnlyOriginalToken(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "new key", true: "same key"}[sameKey], func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			old := newManifestTestAuthority(t, now).issue(t, now, nil, now.Add(time.Hour))
			var key *ecdsa.PrivateKey
			if sameKey {
				key = old.Signer.(*ecdsa.PrivateKey)
			}
			renewed := newManifestTestAuthority(t, now).issue(t, now, key, now.Add(2*time.Hour))
			verify := func(chain [][]byte, at time.Time) (Identity, error) {
				if id, err := renewed.Verify(chain, at); err == nil {
					return id, nil
				}
				return old.Verify(chain, at)
			}
			cache, err := OpenIdentityCache("", DefaultCacheLimits(), verify, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cache.Put(old.Certificate.Certificate, now); err != nil {
				t.Fatal(err)
			}
			link := &bundleCaptureLink{teardownTestLink: &teardownTestLink{done: make(chan struct{})}}
			n := &Node{cache: cache, bundleHints: newBundleHintCache()}
			p := &nodePeer{device: old.Device, link: link, control: make(chan ControlMessage, 4)}
			n.queue(p, []ControlMessage{{Kind: "bundle", Bundle: old.Certificate.Certificate}})
			if _, err := cache.Put(renewed.Certificate.Certificate, now); err != nil {
				t.Fatal(err)
			}
			n.queue(p, []ControlMessage{{Kind: "bundle", Bundle: renewed.Certificate.Certificate}})
			if err := n.writePeerControl(p, <-p.control); err != nil {
				t.Fatal(err)
			}
			if len(link.written) != 1 || !sameChain(link.written[0].Bundle, renewed.Certificate.Certificate) {
				t.Fatal("queued bundle did not normalize to current trust")
			}
			if _, pending := p.pendingBundles.Load(Fingerprint(old.Certificate.Certificate)); pending {
				t.Fatal("normalization retained the original queued token")
			}
			if _, pending := p.pendingBundles.Load(Fingerprint(renewed.Certificate.Certificate)); !pending {
				t.Fatal("normalization removed another queued bundle's token")
			}
			known := n.bundleHints.Known(p.device, time.Now())
			if len(known) != 1 || known[0] != Fingerprint(renewed.Certificate.Certificate) {
				t.Fatal("hint did not record the actual transmitted current bundle")
			}
			if err := n.writePeerControl(p, <-p.control); err != nil {
				t.Fatal(err)
			}
			if _, pending := p.pendingBundles.Load(Fingerprint(renewed.Certificate.Certificate)); pending {
				t.Fatal("current bundle retained its token after writing")
			}
		})
	}
}

func TestPendingBundleDropsAndWriteErrorsReleaseToken(t *testing.T) {
	self, _ := testCredentials(t)
	for _, kind := range []string{"missing cache", "untrusted principal", "malformed certificate", "empty bundle", "write error"} {
		t.Run(kind, func(t *testing.T) {
			cache, err := OpenIdentityCache("", DefaultCacheLimits(), self.Verify, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			bundle := self.Certificate.Certificate
			link := &bundleCaptureLink{teardownTestLink: &teardownTestLink{done: make(chan struct{})}}
			n := &Node{cache: cache, bundleHints: newBundleHintCache()}
			switch kind {
			case "missing cache":
				n.cache = nil
			case "malformed certificate":
				bundle = [][]byte{[]byte("invalid certificate")}
			case "empty bundle":
				bundle = nil
			case "write error":
				if _, err := cache.Put(bundle, time.Now()); err != nil {
					t.Fatal(err)
				}
				link.err = errors.New("transport closed")
			}
			p := &nodePeer{device: self.Device, link: link, control: make(chan ControlMessage, 1)}
			message := ControlMessage{Kind: "bundle", Bundle: bundle}
			n.queue(p, []ControlMessage{message})
			if err := n.writePeerControl(p, <-p.control); !errors.Is(err, link.err) {
				t.Fatalf("write result = %v, want %v", err, link.err)
			}
			if _, pending := p.pendingBundles.Load(Fingerprint(bundle)); pending {
				t.Fatal("dropped or failed bundle retained its queued token")
			}
			if kind != "write error" && len(link.written) != 0 {
				t.Fatal("ineligible bundle reached the transport")
			}
			if len(n.bundleHints.Known(p.device, time.Now())) != 0 {
				t.Fatal("dropped or failed bundle created a written hint")
			}
			n.queue(p, []ControlMessage{message})
			if len(p.control) != 1 {
				t.Fatal("released bundle cannot be requested again")
			}
		})
	}
}

func TestPendingBundlesAreScopedToLinkAndCertificate(t *testing.T) {
	self, peer := testCredentials(t)
	n := &Node{bundleHints: newBundleHintCache()}
	makePeer := func() *nodePeer {
		return &nodePeer{device: peer.Device, control: make(chan ControlMessage, 4), link: &teardownTestLink{done: make(chan struct{})}}
	}
	first, second := makePeer(), makePeer()
	bundle := ControlMessage{Kind: "bundle", Bundle: self.Certificate.Certificate}
	n.queue(first, []ControlMessage{bundle, bundle})
	n.queue(second, []ControlMessage{bundle})
	n.queue(first, []ControlMessage{{Kind: "bundle", Bundle: peer.Certificate.Certificate}})
	if len(first.control) != 2 || len(second.control) != 1 {
		t.Fatal("deduplication crossed a certificate or link boundary")
	}
}

func TestDroppedBundleCanBeRequestedAgain(t *testing.T) {
	self, peer := testCredentials(t)
	n := &Node{bundleHints: newBundleHintCache()}
	p := &nodePeer{device: peer.Device, control: make(chan ControlMessage, 1), link: &teardownTestLink{done: make(chan struct{})}}
	p.control <- ControlMessage{Kind: "manifest"}
	bundle := ControlMessage{Kind: "bundle", Bundle: self.Certificate.Certificate}
	n.queue(p, []ControlMessage{bundle})
	<-p.control
	n.queue(p, []ControlMessage{bundle})
	if len(p.control) != 1 {
		t.Fatal("a full control queue permanently suppressed identity repair")
	}
}
