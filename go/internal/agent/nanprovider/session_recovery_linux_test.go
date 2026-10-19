//go:build linux

package nanprovider

import "testing"

func TestBorrowedNANRecoveryCanReplacePrivateNDIOnly(t *testing.T) {
	r := nanRecovery{}
	if !r.canReset() || r.next(true) != resetNDI {
		t.Fatal("external management ownership prevents recovery of our private NDI")
	}
	if !r.attempted() || r.canReset() || r.next(true) != resetNone {
		t.Fatal("private NDI recovery must be bounded and never restart borrowed nan0")
	}
}

func TestOwnedNANRecoveryRequiresNoRXBeforeManagementReset(t *testing.T) {
	for _, noRX := range []bool{false, true} {
		r := nanRecovery{ownsSession: true}
		if !r.canReset() || r.next(false) != resetNDI || !r.canReset() {
			t.Fatal("owned management must try private NDI recovery first")
		}
		want := resetNone
		if noRX {
			want = resetOwnedSession
		}
		if got := r.next(noRX); got != want {
			t.Fatalf("next(noRX=%v) = %v, want %v", noRX, got, want)
		}
		if noRX && (r.canReset() || r.next(true) != resetNone) {
			t.Fatal("owned management recovery exceeded one reset")
		}
	}
}
