//go:build linux

package localmesh

import (
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
	n := &Node{bundleHints: newBundleHintCache()}
	p := &nodePeer{device: peer.Device, link: link, control: make(chan ControlMessage, 4)}
	bundle := ControlMessage{Kind: "bundle", Bundle: self.Certificate.Certificate}
	n.queue(p, []ControlMessage{bundle})
	writing := <-p.control
	done := make(chan error, 1)
	go func() { done <- n.writePeerControl(p, writing) }()
	<-link.entered
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
