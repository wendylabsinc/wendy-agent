//go:build linux

package nanprovider

import "testing"

func TestBorrowedNANRecoveryCanReplacePrivateNDIOnly(t *testing.T) {
	r := nanRecovery{}
	if !r.canReset(true) || r.next(true) != resetNDI {
		t.Fatal("external management ownership prevents recovery of our private NDI")
	}
	if !r.attempted() || r.canReset(true) || r.next(true) != resetNone {
		t.Fatal("private NDI recovery must be bounded and never restart borrowed nan0")
	}
}

func TestOwnedNANRecoveryRequiresNoRXBeforeManagementReset(t *testing.T) {
	for _, noRX := range []bool{false, true} {
		r := nanRecovery{ownsSession: true}
		if !r.canReset(true) || r.next(false) != resetNDI || !r.canReset(true) {
			t.Fatal("owned management must try private NDI recovery first")
		}
		want := resetNone
		if noRX {
			want = resetOwnedSession
		}
		if got := r.next(noRX); got != want {
			t.Fatalf("next(noRX=%v) = %v, want %v", noRX, got, want)
		}
		if noRX && (r.canReset(true) || r.next(true) != resetNone) {
			t.Fatal("owned management recovery exceeded one reset")
		}
	}
}

func TestOwnedNANRecoveryPreservesWarmEpochAfterNDIRetry(t *testing.T) {
	r := nanRecovery{ownsSession: true}
	if r.next(false) != resetNDI {
		t.Fatal("first warm failure must recreate the private NDI")
	}
	if r.canReset(false) {
		t.Fatal("second warm failure would tear down the epoch without a permitted reset")
	}
	if !r.canReset(true) || r.next(true) != resetOwnedSession {
		t.Fatal("warm evidence must not consume the later no-RX management reset")
	}
	if r.canReset(false) || r.canReset(true) {
		t.Fatal("exhausted recovery must preserve the final live epoch")
	}
}
