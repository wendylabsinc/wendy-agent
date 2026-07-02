package grpcclient

import (
	"sync/atomic"
	"testing"
)

func TestObservedServerOrg_UnsetReturnsFalse(t *testing.T) {
	c := &AgentConnection{}
	if org, ok := c.ObservedServerOrg(); ok || org != 0 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (0, false)", org, ok)
	}
}

func TestObservedServerOrg_ReturnsStoredValue(t *testing.T) {
	c := &AgentConnection{observedServerOrg: new(atomic.Int32)}
	c.observedServerOrg.Store(7)
	if org, ok := c.ObservedServerOrg(); !ok || org != 7 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (7, true)", org, ok)
	}
}

func TestObservedServerOrg_ZeroStoredIsUnset(t *testing.T) {
	c := &AgentConnection{observedServerOrg: new(atomic.Int32)} // never stored → 0
	if org, ok := c.ObservedServerOrg(); ok || org != 0 {
		t.Errorf("ObservedServerOrg() = (%d, %v), want (0, false)", org, ok)
	}
}
