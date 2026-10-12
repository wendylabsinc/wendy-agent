package unitree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pion/webrtc/v4"
	"math"
	"testing"
	"time"
)

func TestTeleopVelocityLimits(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), .151, -.151} {
		if ValidMotion(value, 0) {
			t.Errorf("accepted forward %v", value)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), .301, -.301} {
		if ValidMotion(0, value) {
			t.Errorf("accepted yaw %v", value)
		}
	}
	if !ValidMotion(-.15, .3) {
		t.Fatal("rejected bounded velocity")
	}
}
func TestTeleopAcknowledgementMatchesIDAPIAndStatus(t *testing.T) {
	c := &Connection{pendingID: 12, pendingAPI: 1008, pendingResponse: make(chan int, 1)}
	for _, raw := range []string{
		`{"type":"res","topic":"rt/api/sport/request","data":{"header":{"identity":{"id":13,"api_id":1008},"status":{"code":0}}}}`,
		`{"type":"res","topic":"rt/api/sport/request","data":{"header":{"identity":{"id":12,"api_id":1003},"status":{"code":0}}}}`,
		`{"type":"res","topic":"rt/api/sport/request","data":{"header":{"identity":{"id":12,"api_id":1008}}}}`,
	} {
		c.motionResponse([]byte(raw))
		select {
		case <-c.pendingResponse:
			t.Fatal("accepted unrelated/missing acknowledgement")
		default:
		}
	}
	c.motionResponse([]byte(`{"type":"res","topic":"rt/api/sport/request","data":{"header":{"identity":{"id":12,"api_id":1008},"status":{"code":0}}}}`))
	select {
	case <-c.pendingResponse:
	default:
		t.Fatal("lost matching acknowledgement")
	}
}
func TestTeleopWireCommandsWithFixturePeer(t *testing.T) {
	robot, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer robot.Close()
	commands := make(chan uint64, 10)
	robot.OnDataChannel(func(ch *webrtc.DataChannel) {
		ch.OnOpen(func() { _ = ch.SendText(`{"type":"validation","data":"fixture"}`) })
		ch.OnMessage(func(m webrtc.DataChannelMessage) {
			var msg struct {
				Type  string          `json:"type"`
				Topic string          `json:"topic"`
				Data  json.RawMessage `json:"data"`
			}
			if json.Unmarshal(m.Data, &msg) != nil {
				return
			}
			if msg.Type == "validation" {
				_ = ch.SendText(`{"type":"validation","data":"Validation Ok."}`)
				return
			}
			if msg.Type != "req" {
				return
			}
			var d struct {
				Header struct {
					Identity struct {
						ID  uint64 `json:"id"`
						API uint64 `json:"api_id"`
					} `json:"identity"`
				} `json:"header"`
				Parameter string `json:"parameter"`
			}
			if json.Unmarshal(msg.Data, &d) != nil {
				return
			}
			if msg.Topic != sportTopic {
				t.Error("wrong topic")
			}
			if d.Header.Identity.API == 1008 && d.Parameter != `{"x":0.1,"y":0,"z":0.2}` {
				t.Errorf("unexpected parameter %s", d.Parameter)
			}
			commands <- d.Header.Identity.API
			_ = ch.SendText(fmt.Sprintf(`{"type":"res","topic":%q,"data":{"header":{"identity":{"id":%d,"api_id":%d},"status":{"code":0}}}}`, sportTopic, d.Header.Identity.ID, d.Header.Identity.API))
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := connectWithSignaling(ctx, func(ctx context.Context, offer []byte) ([]byte, error) { return answerOffer(t, ctx, robot, offer) })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Move(ctx, .1, .2); err != nil {
		t.Fatal(err)
	}
	if err = c.Move(ctx, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint64{1008, 1003} {
		select {
		case got := <-commands:
			if got != want {
				t.Fatalf("api=%d want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal("no wire command")
		}
	}
	if err = c.Move(ctx, math.NaN(), 0); err == nil {
		t.Fatal("accepted nonfinite Move")
	}
	select {
	case <-commands:
		t.Fatal("invalid Move sent command")
	default:
	}
}
