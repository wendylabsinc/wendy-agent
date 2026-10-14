package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/vm"
)

// stubQEMUMissing makes the emulator look absent, and records whether an
// install was attempted.
func stubQEMUMissing(t *testing.T, installed *bool) {
	t.Helper()
	savedLook, savedInstall := qemuLookPathFn, installQEMUFn
	qemuLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	installQEMUFn = func(context.Context) error {
		*installed = true
		return nil
	}
	t.Cleanup(func() { qemuLookPathFn, installQEMUFn = savedLook, savedInstall })
}

// stubTerminal forces the interactive check either way; the shared
// stubInteractive only covers the true case.
func stubTerminal(t *testing.T, interactive bool) {
	t.Helper()
	prev := isInteractiveTerminalFn
	isInteractiveTerminalFn = func() bool { return interactive }
	t.Cleanup(func() { isInteractiveTerminalFn = prev })
}

func stubQEMUConfirmAnswer(t *testing.T, confirm func(string) bool) {
	t.Helper()
	saved := qemuConfirmFn
	qemuConfirmFn = func(_ context.Context, question string) (bool, error) { return confirm(question), nil }
	t.Cleanup(func() { qemuConfirmFn = saved })
}

func TestEnsureQEMUIsANoOpWhenAlreadyInstalled(t *testing.T) {
	saved := qemuLookPathFn
	qemuLookPathFn = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { qemuLookPathFn = saved })

	asked := false
	stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return true })
	if err := ensureQEMUForHostOS(context.Background(), "darwin"); err != nil {
		t.Fatalf("ensureQEMUForHostOS() = %v, want nil", err)
	}
	if asked {
		t.Error("prompted even though QEMU is already installed")
	}
}

func TestEnsureQEMUOffersToInstallOnInteractiveMac(t *testing.T) {
	var installed, asked bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubTerminal(t, true)
	stubQEMUConfirmAnswer(t, func(question string) bool {
		asked = true
		if question != "Do you want to install QEMU?" {
			t.Errorf("question = %q", question)
		}
		return true
	})

	// The stub keeps reporting the binary as missing, so the post-install PATH
	// re-check is what this lands on -- which is the point: an install that
	// does not put QEMU on PATH must say so rather than claim success.
	err := ensureQEMUForHostOS(context.Background(), "darwin")
	if !asked {
		t.Error("did not prompt before installing")
	}
	if !installed {
		t.Error("accepted the prompt but never ran the install")
	}
	if err == nil || !strings.Contains(err.Error(), "not yet on PATH") {
		t.Errorf("ensureQEMUForHostOS() = %v, want the PATH re-check error", err)
	}
}

func TestEnsureQEMUDoesNotPromptWithoutHomebrew(t *testing.T) {
	var installed, asked bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(string) (string, error) { return "", errors.New("not found") })
	stubTerminal(t, true)
	stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return true })

	err := ensureQEMUForHostOS(context.Background(), "darwin")
	if asked || installed {
		t.Error("prompted or installed on a host with no Homebrew")
	}
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("ensureQEMUForHostOS() = %v, want the plain not-found error", err)
	}
}

func TestEnsureQEMUDoesNotPromptWhenNotInteractive(t *testing.T) {
	var installed, asked bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubTerminal(t, false)
	stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return true })

	if err := ensureQEMUForHostOS(context.Background(), "darwin"); err == nil {
		t.Fatal("ensureQEMUForHostOS() = nil, want the not-found error")
	}
	if asked || installed {
		t.Error("prompted or installed without a terminal to prompt on")
	}
}

func TestEnsureQEMUDeclinedKeepsTheManualCommand(t *testing.T) {
	var installed, asked bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubTerminal(t, true)
	stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return false })

	err := ensureQEMUForHostOS(context.Background(), "darwin")
	if !asked {
		t.Error("never prompted")
	}
	if installed {
		t.Error("installed despite the prompt being declined")
	}
	if err == nil || !strings.Contains(err.Error(), qemuInstallHint()) {
		t.Errorf("ensureQEMUForHostOS() = %v, want it to name %q", err, qemuInstallHint())
	}
}

func TestEnsureQEMUNeverPromptsOffMac(t *testing.T) {
	// No non-brew installer is ever driven for the user, so Linux and Windows
	// get the hint and nothing else.
	for _, hostOS := range []string{"linux", "windows"} {
		var installed, asked bool
		stubQEMUMissing(t, &installed)
		stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
		stubTerminal(t, true)
		stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return true })

		err := ensureQEMUForHostOS(context.Background(), hostOS)
		if asked || installed {
			t.Errorf("%s: prompted or installed", hostOS)
		}
		if err == nil {
			t.Errorf("%s: ensureQEMUForHostOS() = nil, want the not-found error", hostOS)
		}
	}
}

func TestEnsureQEMUWithYesSkipsThePromptAndInstalls(t *testing.T) {
	var installed, asked bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubTerminal(t, false)
	stubQEMUConfirmAnswer(t, func(string) bool { asked = true; return false })

	saved := vmAssumeYes
	vmAssumeYes = true
	t.Cleanup(func() { vmAssumeYes = saved })

	_ = ensureQEMUForHostOS(context.Background(), "darwin")
	if asked {
		t.Error("prompted despite --yes")
	}
	if !installed {
		t.Error("--yes did not install QEMU")
	}
}

func TestVMCommandHasAYesFlag(t *testing.T) {
	if newVMCmd().PersistentFlags().Lookup("yes") == nil {
		t.Error("wendy vm has no --yes flag")
	}
}

func TestQEMUInstallHintIsTheOnlySourceInHelp(t *testing.T) {
	// The help text used to carry its own copy of the install command, which
	// then drifted from the error a user actually hits.
	if long := newVMCmd().Long; !strings.Contains(long, qemuInstallHint()) {
		t.Errorf("vm help = %q, want it to name %q", long, qemuInstallHint())
	}
}

func TestEnsureQEMUCancelledDoesNotInstall(t *testing.T) {
	var installed bool
	stubQEMUMissing(t, &installed)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubTerminal(t, true)
	saved := qemuConfirmFn
	qemuConfirmFn = func(context.Context, string) (bool, error) { return false, ErrUserCancelled }
	t.Cleanup(func() { qemuConfirmFn = saved })

	if err := ensureQEMUForHostOS(context.Background(), "darwin"); !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if installed {
		t.Fatal("installed QEMU after cancellation")
	}
}

func TestQEMURecoveryStopsOnContextCancellation(t *testing.T) {
	for _, firmware := range []bool{false, true} {
		for _, beforePrompt := range []bool{false, true} {
			t.Run(fmt.Sprintf("firmware=%t/before_prompt=%t", firmware, beforePrompt), func(t *testing.T) {
				var installed, asked bool
				stubQEMUMissing(t, &installed)
				stubTerminal(t, true)
				stubBrewLookPath(t, func(string) (string, error) { return "/test/brew", nil })
				savedConfirm, savedYes := qemuConfirmFn, vmAssumeYes
				savedFind, savedInstall := findQEMUFirmwareFn, installQEMUFirmwareFn
				t.Cleanup(func() {
					qemuConfirmFn, vmAssumeYes = savedConfirm, savedYes
					findQEMUFirmwareFn, installQEMUFirmwareFn = savedFind, savedInstall
				})
				vmAssumeYes = false
				findQEMUFirmwareFn = func(string, string) (string, error) { return "", vm.ErrFirmwareNotFound }
				installQEMUFirmwareFn = func(context.Context) error { installed = true; return nil }
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				qemuConfirmFn = func(promptCtx context.Context, _ string) (bool, error) {
					asked = true
					if promptCtx != ctx {
						t.Fatal("confirmation did not receive the operation context")
					}
					cancel()
					return true, nil
				}
				if beforePrompt {
					cancel()
				}

				var err error
				if firmware {
					_, err = ensureQEMUFirmwareForHostOS(ctx, "darwin", "/test/homebrew")
				} else {
					err = ensureQEMUForHostOS(ctx, "darwin")
				}
				if !errors.Is(err, context.Canceled) || installed || asked == beforePrompt {
					t.Fatalf("err=%v installed=%t asked=%t, want cancellation with no install and asked=%t", err, installed, asked, !beforePrompt)
				}
			})
		}
	}
}

func TestEnsureQEMUFirmwareRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, hostOS     string
		interactive, yes bool
		brew, accept     bool
		alreadyInstalled bool
		stillMissing     bool
		confirmErr       error
		installErr       error
		wantAsked        bool
		wantInstalled    bool
		wantErr          error
	}{
		{name: "already available", hostOS: "darwin", alreadyInstalled: true},
		{name: "accepted repair", hostOS: "darwin", interactive: true, brew: true, accept: true, wantAsked: true, wantInstalled: true},
		{name: "declined repair", hostOS: "darwin", interactive: true, brew: true, wantAsked: true, wantErr: vm.ErrFirmwareNotFound},
		{name: "cancelled prompt", hostOS: "darwin", interactive: true, brew: true, confirmErr: ErrUserCancelled, wantAsked: true, wantErr: ErrUserCancelled},
		{name: "missing after repair", hostOS: "darwin", interactive: true, brew: true, accept: true, stillMissing: true, wantAsked: true, wantInstalled: true, wantErr: vm.ErrFirmwareNotFound},
		{name: "cancelled install", hostOS: "darwin", interactive: true, brew: true, accept: true, installErr: ErrUserCancelled, wantAsked: true, wantInstalled: true, wantErr: ErrUserCancelled},
		{name: "no terminal", hostOS: "darwin", brew: true, wantErr: vm.ErrFirmwareNotFound},
		{name: "explicit yes", hostOS: "darwin", brew: true, yes: true, wantInstalled: true},
		{name: "no Homebrew", hostOS: "darwin", interactive: true, accept: true, wantErr: vm.ErrFirmwareNotFound},
		{name: "Linux manual install", hostOS: "linux", interactive: true, brew: true, accept: true, wantErr: vm.ErrFirmwareNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedFind, savedInstall := findQEMUFirmwareFn, installQEMUFirmwareFn
			savedConfirm, savedYes := qemuConfirmFn, vmAssumeYes
			t.Cleanup(func() {
				findQEMUFirmwareFn, installQEMUFirmwareFn = savedFind, savedInstall
				qemuConfirmFn, vmAssumeYes = savedConfirm, savedYes
			})
			var asked, installed bool
			findCalls := 0
			findQEMUFirmwareFn = func(hostOS, prefix string) (string, error) {
				findCalls++
				if hostOS != tc.hostOS || prefix != "/test/homebrew" {
					t.Fatalf("firmware discovery used host %q and prefix %q", hostOS, prefix)
				}
				if tc.alreadyInstalled || installed && !tc.stillMissing {
					return "/test/arm64.fd", nil
				}
				return "", vm.ErrFirmwareNotFound
			}
			installQEMUFirmwareFn = func(context.Context) error {
				installed = true
				return tc.installErr
			}
			qemuConfirmFn = func(context.Context, string) (bool, error) {
				asked = true
				return tc.accept, tc.confirmErr
			}
			vmAssumeYes = tc.yes
			stubTerminal(t, tc.interactive)
			stubBrewLookPath(t, func(string) (string, error) {
				if tc.brew {
					return "/test/brew", nil
				}
				return "", errors.New("brew not found")
			})

			firmware, err := ensureQEMUFirmwareForHostOS(context.Background(), tc.hostOS, "/test/homebrew")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if asked != tc.wantAsked || installed != tc.wantInstalled {
				t.Errorf("asked=%t installed=%t, want asked=%t installed=%t", asked, installed, tc.wantAsked, tc.wantInstalled)
			}
			if err == nil && firmware != "/test/arm64.fd" {
				t.Errorf("firmware = %q", firmware)
			}
			if installed && tc.installErr == nil && findCalls != 2 {
				t.Errorf("discovery calls = %d, want another check after repair", findCalls)
			}
		})
	}
}

// stubSocketVMNetMissing simulates a missing helper that becomes available
// after starting, regardless of the host's actual installation.
func stubSocketVMNetMissing(t *testing.T, installed, started *bool) {
	t.Helper()
	savedFind := findSocketVMNetFn
	savedInstall, savedStart := installSocketVMNetFn, startSocketVMNetFn
	findSocketVMNetFn = func(hostOS, brewPrefix string) (string, error) {
		if hostOS != "darwin" {
			return savedFind(hostOS, brewPrefix)
		}
		if *started {
			return "/test/socket_vmnet", nil
		}
		return "", fmt.Errorf("%w: brew install socket_vmnet", vm.ErrSocketVMNetNotFound)
	}
	installSocketVMNetFn = func(context.Context) error { *installed = true; return nil }
	startSocketVMNetFn = func(context.Context) error { *started = true; return nil }
	t.Cleanup(func() {
		findSocketVMNetFn = savedFind
		installSocketVMNetFn, startSocketVMNetFn = savedInstall, savedStart
	})
}

func TestEnsureSocketVMNetNeverPromptsOffMac(t *testing.T) {
	// vmnet is an Apple framework: there is nothing to offer to install, and
	// the error must not recommend Homebrew on a host without it.
	for _, hostOS := range []string{"linux", "windows"} {
		var installed, started, asked bool
		stubSocketVMNetMissing(t, &installed, &started)
		stubTerminal(t, true)
		stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
		stubConfirmFn(t, func(string) bool { asked = true; return true })

		_, err := ensureSocketVMNet(context.Background(), hostOS, "")
		if asked || installed || started {
			t.Errorf("%s: prompted or installed for a platform that cannot run vmnet", hostOS)
		}
		if !errors.Is(err, vm.ErrSharedNetUnsupported) {
			t.Errorf("%s: err = %v, want ErrSharedNetUnsupported", hostOS, err)
		}
	}
}

func TestEnsureSocketVMNetInstallsAndStartsOnMac(t *testing.T) {
	// Install alone leaves the socket missing, so both halves must run.
	var installed, started, asked bool
	stubSocketVMNetMissing(t, &installed, &started)
	stubTerminal(t, true)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubConfirmFn(t, func(string) bool { asked = true; return true })

	socket, err := ensureSocketVMNet(context.Background(), "darwin", "/opt/homebrew")
	if err != nil {
		t.Fatalf("ensureSocketVMNet() = %v, want nil", err)
	}
	if socket != "/test/socket_vmnet" {
		t.Errorf("socket = %q, want /test/socket_vmnet", socket)
	}
	if !asked {
		t.Error("did not prompt before installing")
	}
	if !installed || !started {
		t.Errorf("installed=%v started=%v, want both", installed, started)
	}
}

func TestEnsureSocketVMNetDeclinedKeepsTheManualCommands(t *testing.T) {
	var installed, started bool
	stubSocketVMNetMissing(t, &installed, &started)
	stubTerminal(t, true)
	stubBrewLookPath(t, func(n string) (string, error) { return "/opt/homebrew/bin/" + n, nil })
	stubConfirmFn(t, func(string) bool { return false })

	_, err := ensureSocketVMNet(context.Background(), "darwin", "/opt/homebrew")
	if installed || started {
		t.Error("installed despite the prompt being declined")
	}
	if err == nil || !strings.Contains(err.Error(), "brew install socket_vmnet") {
		t.Errorf("err = %v, want it to name the manual command", err)
	}
}

func TestEnsureSocketVMNetDoesNotPromptWithoutHomebrew(t *testing.T) {
	var installed, started, asked bool
	stubSocketVMNetMissing(t, &installed, &started)
	stubTerminal(t, true)
	stubBrewLookPath(t, func(string) (string, error) { return "", errors.New("not found") })
	stubConfirmFn(t, func(string) bool { asked = true; return true })

	if _, err := ensureSocketVMNet(context.Background(), "darwin", ""); err == nil {
		t.Fatal("ensureSocketVMNet() = nil, want the not-found error")
	}
	if asked || installed || started {
		t.Error("prompted or installed on a host with no Homebrew")
	}
}
