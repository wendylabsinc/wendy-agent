package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func topSampleWithBattery(pct float64, state agentpb.BatteryState) *agentpb.GetResourceStatsResponse {
	return &agentpb.GetResourceStatsResponse{
		Host: &agentpb.HostStats{
			CpuCount:          4,
			MemTotalBytes:     8 << 30,
			MemAvailableBytes: 4 << 30,
			Battery:           &agentpb.BatteryStats{Percent: pct, State: state},
		},
	}
}

// A device whose battery runs flat stops answering. The dashboard must say so
// instead of leaving the last sample on screen as if it were live.
func TestTopShowsOfflineAfterUnreachablePoll(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)

	updated, _ := m.Update(topStatsMsg{resp: topSampleWithBattery(3, agentpb.BatteryState_BATTERY_STATE_DISCHARGING)})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("a successful poll must not mark the device offline")
	}

	updated, _ = m.Update(topStatsMsg{err: status.Error(codes.Unavailable, "connection refused")})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("an Unavailable poll must mark the device offline")
	}

	m.width, m.height = 100, 30
	view := m.View()
	if !strings.Contains(view, "DEVICE OFFLINE") {
		t.Fatalf("view does not announce the device is offline:\n%s", view)
	}
	if !strings.Contains(view, "last values received") {
		t.Fatalf("view does not mark the readings as stale:\n%s", view)
	}
	// The raw transport string must never reach the user (formatError truncates
	// at that marker, and it means nothing to them anyway).
	if strings.Contains(view, "rpc error: code = ") {
		t.Fatalf("view leaked the raw gRPC error:\n%s", view)
	}
}

// The reason the device vanished is usually the battery, so say what the last
// reading was — that is the difference between "offline" and "it went flat".
func TestTopOfflineHeadlineMentionsLastBatteryReading(t *testing.T) {
	b := &agentpb.BatteryStats{Percent: 2, State: agentpb.BatteryState_BATTERY_STATE_DISCHARGING}
	got := topOfflineHeadline(34*time.Second, b)
	for _, want := range []string{"DEVICE OFFLINE", "34s", "2%", "discharging"} {
		if !strings.Contains(got, want) {
			t.Fatalf("headline %q missing %q", got, want)
		}
	}

	noBattery := topOfflineHeadline(90*time.Second, nil)
	if strings.Contains(noBattery, "battery") {
		t.Fatalf("mains-powered device must not get a battery clause: %q", noBattery)
	}
	if !strings.Contains(noBattery, "1m30s") {
		t.Fatalf("headline %q missing the age", noBattery)
	}
}

// A charging or full pack is not why the device went away; only a discharging
// one earns the battery clause, otherwise we would be inventing a cause.
func TestTopOfflineHeadlineOmitsBatteryWhenNotDischarging(t *testing.T) {
	b := &agentpb.BatteryStats{Percent: 96, State: agentpb.BatteryState_BATTERY_STATE_CHARGING}
	if got := topOfflineHeadline(5*time.Second, b); strings.Contains(got, "battery") {
		t.Fatalf("charging pack must not be blamed: %q", got)
	}
}

func TestTopClearsOfflineWhenDeviceAnswersAgain(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)
	updated, _ := m.Update(topStatsMsg{err: status.Error(codes.Unavailable, "connection refused")})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("setup: expected offline")
	}

	updated, _ = m.Update(topStatsMsg{resp: topSampleWithBattery(80, agentpb.BatteryState_BATTERY_STATE_CHARGING)})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("a successful poll must clear the offline state")
	}
	m.width, m.height = 100, 30
	if strings.Contains(m.View(), "DEVICE OFFLINE") {
		t.Fatalf("offline banner survived recovery:\n%s", m.View())
	}
}

// An agent that answers with an application-level error is still online; only
// transport failures mean the device is gone.
func TestTopServerErrorIsNotOffline(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)
	updated, _ := m.Update(topStatsMsg{err: status.Error(codes.Internal, "sampler blew up")})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("codes.Internal is not an offline signal")
	}
	if m.flash == "" {
		t.Fatal("a server-side error should still surface as a flash")
	}
}

// An agent that replies with an error has, by replying, proved it is back. The
// banner must not outlive that proof — "no response for 41s" beside a fresh
// reply is simply false.
func TestTopServerErrorClearsOffline(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)
	updated, _ := m.Update(topStatsMsg{resp: topSampleWithBattery(3, agentpb.BatteryState_BATTERY_STATE_DISCHARGING)})
	m = updated.(topModel)
	updated, _ = m.Update(topStatsMsg{err: status.Error(codes.Unavailable, "connection refused")})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("setup: expected offline")
	}

	updated, _ = m.Update(topStatsMsg{err: status.Error(codes.Internal, "sampler blew up")})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("an application-level reply proves the device is reachable; the banner must clear")
	}
	m.width, m.height = 100, 30
	view := m.View()
	if strings.Contains(view, "DEVICE OFFLINE") {
		t.Fatalf("offline banner survived an agent reply:\n%s", view)
	}
	if m.flash == "" {
		t.Fatal("the agent-side error should surface as a flash")
	}
}

func TestIsDeviceUnreachable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", status.Error(codes.Unavailable, "connection refused"), true},
		{"deadline", status.Error(codes.DeadlineExceeded, "context deadline exceeded"), true},
		{"bare context deadline", context.DeadlineExceeded, true},
		{"wrapped unavailable", fmt.Errorf("listing containers: %w", status.Error(codes.Unavailable, "x")), true},
		{"internal", status.Error(codes.Internal, "boom"), false},
		{"unimplemented", errResourceStatsUnimplemented, false},
		{"canceled", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeviceUnreachable(tc.err); got != tc.want {
				t.Fatalf("isDeviceUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A device that loses power mid-RPC never sends a FIN, so the socket
// black-holes. Without a per-poll deadline the goroutine blocks until gRPC
// keepalive notices — 15 minutes by default — and the ticker stalls with it.
func TestTopPollTimeoutIsBounded(t *testing.T) {
	if got := topPollTimeout(2 * time.Second); got != 4*time.Second {
		t.Fatalf("topPollTimeout(2s) = %v, want 4s", got)
	}
	if got := topPollTimeout(0); got != topPollTimeoutFloor {
		t.Fatalf("topPollTimeout(0) = %v, want floor %v", got, topPollTimeoutFloor)
	}
	// A long refresh interval must not push the deadline past the point where
	// the user would notice the device is gone.
	if got := topPollTimeout(10 * time.Minute); got != topPollTimeoutCap {
		t.Fatalf("topPollTimeout(10m) = %v, want cap %v", got, topPollTimeoutCap)
	}
	if got := topPollTimeout(time.Duration(1<<63 - 1)); got != topPollTimeoutCap {
		t.Fatalf("topPollTimeout(max duration) = %v, want cap %v", got, topPollTimeoutCap)
	}
}

// The containers poll shares the connection, so it must also flip the banner.
func TestTopContainersPollMarksOffline(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)
	updated, _ := m.Update(topContainersMsg{err: fmt.Errorf("receiving: %w", status.Error(codes.Unavailable, "x"))})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("an unreachable containers poll must mark the device offline")
	}
}

// A container list arriving is itself proof the connection is alive, so it must
// end the outage even while the stats poll is still failing — otherwise the
// banner claims silence over a device that is plainly answering.
func TestTopContainersSuccessClearsOffline(t *testing.T) {
	m := newTopModel(context.Background(), nil, 2*time.Second)
	updated, _ := m.Update(topStatsMsg{err: status.Error(codes.Unavailable, "connection refused")})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("setup: expected offline")
	}

	updated, _ = m.Update(topContainersMsg{containers: nil})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("a successful containers poll must clear the offline state")
	}
}

// A slow stats timeout can arrive after a newer containers reply. That older
// poll must not put the device back offline merely because Bubble Tea happened
// to process its result last.
func TestTopLateFailureDoesNotOverrideNewerReply(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := newTopModel(context.Background(), nil, 2*time.Second)

	updated, _ := m.Update(topContainersMsg{startedAt: t0, finishedAt: t0.Add(time.Second)})
	m = updated.(topModel)
	updated, _ = m.Update(topStatsMsg{
		err:        status.Error(codes.DeadlineExceeded, "context deadline exceeded"),
		startedAt:  t0,
		finishedAt: t0.Add(4 * time.Second),
	})
	m = updated.(topModel)
	if m.isOffline() {
		t.Fatal("a failed poll that began before the latest reply must not override that proof of reachability")
	}
}

// Conversely, a reply completed before a newer failed poll began is stale and
// must not clear the outage when its message is delivered late.
func TestTopLateReplyDoesNotOverrideNewerFailure(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := newTopModel(context.Background(), nil, 2*time.Second)

	updated, _ := m.Update(topStatsMsg{
		err:        status.Error(codes.Unavailable, "connection refused"),
		startedAt:  t0.Add(2 * time.Second),
		finishedAt: t0.Add(3 * time.Second),
	})
	m = updated.(topModel)
	updated, _ = m.Update(topContainersMsg{startedAt: t0, finishedAt: t0.Add(time.Second)})
	m = updated.(topModel)
	if !m.isOffline() {
		t.Fatal("a reply older than the newest failed poll must not clear the outage")
	}
}

func TestTopSilentAgeUsesLatestReplyFromEitherPoll(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := newTopModel(context.Background(), nil, 2*time.Second)
	m.noteOnline(t0)
	m.noteReachable(t0.Add(30 * time.Second))
	m.markOffline(t0.Add(32 * time.Second))

	if got := m.silentFor(t0.Add(40 * time.Second)); got != 10*time.Second {
		t.Fatalf("silentFor = %v, want 10s since the latest containers reply", got)
	}
}

func TestFormatAgeShort(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "0s",
		999 * time.Millisecond:          "0s",
		34 * time.Second:                "34s",
		90 * time.Second:                "1m30s",
		59*time.Minute + 59*time.Second: "59m59s",
		time.Hour + 2*time.Minute:       "1h02m",
	}
	for d, want := range cases {
		if got := formatAgeShort(d); got != want {
			t.Fatalf("formatAgeShort(%v) = %q, want %q", d, got, want)
		}
	}
}
