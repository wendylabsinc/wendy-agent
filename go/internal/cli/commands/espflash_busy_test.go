package commands

import "testing"

func withStubs(t *testing.T, find func(string) []portHolder, kill func(int), confirm func(string) bool) {
	t.Helper()
	origFind, origKill, origConfirm, origDelay := findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay
	findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay = find, kill, confirm, 0
	t.Cleanup(func() {
		findPortHoldersFn, killProcessFn, confirmFn, killSettleDelay = origFind, origKill, origConfirm, origDelay
	})
}

func TestOfferPortBusyRetry_NoHoldersFound(t *testing.T) {
	var killed []int
	withStubs(t,
		func(string) []portHolder { return nil },
		func(pid int) { killed = append(killed, pid) },
		func(string) bool { t.Fatal("must not prompt when no holder is identified"); return false },
	)

	if offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = true, want false (no holder to kill)")
	}
	if len(killed) != 0 {
		t.Errorf("killed = %v, want none", killed)
	}
}

func TestOfferPortBusyRetry_HolderFound_UserConfirmsKill(t *testing.T) {
	var killed []int
	var askedQuestion string
	withStubs(t,
		func(string) []portHolder { return []portHolder{{pid: 4242, command: "wendy run"}} },
		func(pid int) { killed = append(killed, pid) },
		func(q string) bool { askedQuestion = q; return true },
	)

	if !offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = false, want true (kill confirmed, should retry automatically)")
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Errorf("killed = %v, want [4242]", killed)
	}
	if askedQuestion != "Kill this process and retry?" {
		t.Errorf("question = %q, want singular phrasing", askedQuestion)
	}
}

func TestOfferPortBusyRetry_HolderFound_UserDeclines(t *testing.T) {
	var killed []int
	withStubs(t,
		func(string) []portHolder { return []portHolder{{pid: 4242, command: "wendy run"}} },
		func(pid int) { killed = append(killed, pid) },
		func(string) bool { return false },
	)

	if offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = true, want false (kill declined)")
	}
	if len(killed) != 0 {
		t.Errorf("killed = %v, want none", killed)
	}
}

func TestOfferPortBusyRetry_MultipleHolders_Pluralized(t *testing.T) {
	var killed []int
	var askedQuestion string
	withStubs(t,
		func(string) []portHolder {
			return []portHolder{{pid: 1, command: "a"}, {pid: 2, command: ""}}
		},
		func(pid int) { killed = append(killed, pid) },
		func(q string) bool { askedQuestion = q; return true },
	)

	if !offerPortBusyRetry("/dev/ttyFAKE") {
		t.Error("offerPortBusyRetry() = false, want true")
	}
	if len(killed) != 2 {
		t.Errorf("killed = %v, want 2 pids", killed)
	}
	if askedQuestion != "Kill these processes and retry?" {
		t.Errorf("question = %q, want plural phrasing", askedQuestion)
	}
}
