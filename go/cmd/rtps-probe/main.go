// Command rtps-probe joins a DDS domain, prints every writer it discovers, and
// decodes any battery-bearing topic it finds.
//
// It exists because the battery source has to be validated against a real
// robot, and the agent cannot be rebuilt and redeployed for every iteration.
// Unlike `ros2 topic echo`, it needs no message typesupport — it walks bytes,
// which is exactly what the agent will do.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats/rosbattery"
	"github.com/wendylabsinc/wendy/go/internal/rtps"
)

func main() {
	domain := flag.Int("domain", 0, "ROS_DOMAIN_ID / DDS domain")
	iface := flag.String("interface", "", "network interface to bind discovery to")
	settle := flag.Duration("settle", 20*time.Second, "how long to let discovery run before subscribing")
	run := flag.Duration("run", 45*time.Second, "how long to receive samples for")
	topic := flag.String("topic", "", "topic to subscribe to (default: auto-detect a battery topic)")
	flag.Parse()

	fmt.Printf("RTPSPROBE-BEGIN domain=%d interface=%q\n", *domain, *iface)

	p, err := rtps.NewParticipant(rtps.Config{
		DomainID:  *domain,
		Interface: *iface,
		Logf:      func(f string, a ...any) { fmt.Printf("RTPSPROBE [rtps] "+f+"\n", a...) },
	})
	if err != nil {
		fmt.Printf("RTPSPROBE-FATAL %v\n", err)
		os.Exit(1)
	}
	defer p.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runCtx, runCancel := context.WithTimeout(ctx, *settle+*run+10*time.Second)
	defer runCancel()

	go p.Run(runCtx)

	// Phase 1: collect discovery for `settle`.
	found := map[string]rtps.Endpoint{}
	deadline := time.After(*settle)
collect:
	for {
		select {
		case ep := <-p.Discovered():
			found[ep.Topic] = ep
		case <-deadline:
			break collect
		case <-runCtx.Done():
			break collect
		}
	}

	st := p.Stats()
	fmt.Printf("RTPSPROBE === wire: mcast_pkts=%d ucast_pkts=%d rtps_msgs=%d spdp=%d sedp=%d hb=%d acks=%d data=%d data_err=%d peers=%d\n",
		st.MulticastPackets, st.UnicastPackets, st.RTPSMessages,
		st.SPDPAnnounces, st.SEDPAnnounces, st.Heartbeats, st.AcksSent,
		st.DataSubmessages, st.DataParseErrors, st.PeersSeen)

	dataHist, hbHist := p.WriterHistogram()
	fmt.Printf("RTPSPROBE === DATA by writer entity: %s\n", formatHist(dataHist))
	fmt.Printf("RTPSPROBE === HEARTBEAT by writer entity: %s\n", formatHist(hbHist))
	fmt.Printf("RTPSPROBE === discovered %d writers\n", len(found))
	topics := make([]string, 0, len(found))
	for t := range found {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	for _, t := range topics {
		fmt.Printf("RTPSPROBE topic %s [%s] reliable=%v locators=%d\n",
			t, found[t].Type, found[t].Reliable, len(found[t].Locators))
	}

	// Phase 2: pick the battery topic and subscribe.
	target, ok := pickBatteryTopic(found, *topic)
	if !ok {
		fmt.Printf("RTPSPROBE === no battery topic found\n")
		fmt.Printf("RTPSPROBE-COMPLETE\n")
		return
	}
	fmt.Printf("RTPSPROBE === subscribing to %s [%s]\n", target.Topic, target.Type)
	if err := p.Subscribe(target); err != nil {
		fmt.Printf("RTPSPROBE-FATAL subscribe: %v\n", err)
		return
	}

	// Phase 3: decode what arrives.
	stop := time.After(*run)
	n := 0
	for {
		select {
		case s := <-p.Samples():
			n++
			if n <= 3 || n%50 == 0 {
				report(n, target, s)
			}
		case <-stop:
			st := p.Stats()
			fmt.Printf("RTPSPROBE === received %d samples (wire user_data=%d rtps=%d)\n",
				n, st.UserDataMessages, st.RTPSMessages)
			fmt.Printf("RTPSPROBE-COMPLETE\n")
			return
		case <-runCtx.Done():
			fmt.Printf("RTPSPROBE === received %d samples (ctx done)\n", n)
			fmt.Printf("RTPSPROBE-COMPLETE\n")
			return
		}
	}
}

// formatHist renders a writer-entity histogram, naming the builtin entities so
// the output says "SEDP_PUB_WRITER" rather than a bare hex value.
func formatHist(h map[uint32]int) string {
	if len(h) == 0 {
		return "<none>"
	}
	names := map[uint32]string{
		0x000100c2: "SPDP_WRITER",
		0x000003c2: "SEDP_PUB_WRITER",
		0x000004c2: "SEDP_SUB_WRITER",
		0x000200c2: "P2P_MSG_WRITER",
		0x000201c3: "P2P_MSG_WRITER_SECURE",
	}
	ids := make([]uint32, 0, len(h))
	for id := range h {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return h[ids[i]] > h[ids[j]] })

	var sb strings.Builder
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(", ")
		}
		if n, ok := names[id]; ok {
			fmt.Fprintf(&sb, "%s=%d", n, h[id])
		} else {
			fmt.Fprintf(&sb, "%#08x=%d", id, h[id])
		}
	}
	return sb.String()
}

// report decodes one sample and prints both the raw shape and the decoded
// battery, so a layout mismatch is visible rather than merely wrong.
func report(n int, ep rtps.Endpoint, s rtps.Sample) {
	fmt.Printf("RTPSPROBE sample #%d sn=%d payload=%d bytes\n", n, s.SN, len(s.Payload))

	var b *hoststats.Battery
	var err error
	switch {
	case strings.Contains(ep.Type, "BatteryState"):
		b, err = rosbattery.DecodeBatteryState(s.Payload)
	case strings.Contains(ep.Type, "LowState"):
		b, err = rosbattery.DecodeLowState(s.Payload)
	default:
		fmt.Printf("RTPSPROBE   no decoder for %s\n", ep.Type)
		return
	}
	if err != nil {
		fmt.Printf("RTPSPROBE   DECODE-ERROR %v\n", err)
		if len(s.Payload) >= 16 {
			fmt.Printf("RTPSPROBE   first 16 bytes: % x\n", s.Payload[:16])
		}
		return
	}
	fmt.Printf("RTPSPROBE   DECODED percent=%.1f state=%s seconds_remaining=%d\n",
		b.Percent, b.State, b.SecondsRemaining)
}

// pickBatteryTopic chooses what to subscribe to: an explicit --topic when
// given, else a BatteryState writer, else a LowState writer preferring the
// low-frequency variant over the high-rate control topic.
func pickBatteryTopic(found map[string]rtps.Endpoint, want string) (rtps.Endpoint, bool) {
	if want != "" {
		ep, ok := found[want]
		return ep, ok
	}
	var lowState, lfLowState *rtps.Endpoint
	for t, ep := range found {
		switch {
		case strings.Contains(ep.Type, "BatteryState"):
			return ep, true
		case strings.Contains(ep.Type, "LowState"):
			e := ep
			if strings.HasPrefix(t, "rt/lf/") || strings.HasPrefix(t, "/lf/") {
				lfLowState = &e
			} else if lowState == nil {
				lowState = &e
			}
		}
	}
	if lfLowState != nil {
		return *lfLowState, true
	}
	if lowState != nil {
		return *lowState, true
	}
	return rtps.Endpoint{}, false
}
