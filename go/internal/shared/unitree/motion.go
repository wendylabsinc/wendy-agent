package unitree

// Wire IDs/shape from unitree_webrtc_connect@e0abc5780761539eff89a382a79319e5cf6ad1f4
// constants.py and msgs/pub_sub.py. Do not copy/run its sportmode example: it
// changes modes and stands/moves the robot. Firmware acceptance is unverified.
import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// Same limits as the Go2 simulator and its example apps. The physical Go2
// ignores forward requests below about 0.5 m/s, so lower caps stop it walking.
const MotionForwardLimit = 0.8
const MotionLateralLimit = 0.5
const MotionYawLimit = 1.0
const sportTopic = "rt/api/sport/request"

func ValidMotion(forward, lateral, yaw float64) bool {
	within := func(v, limit float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= limit }
	return within(forward, MotionForwardLimit) && within(lateral, MotionLateralLimit) && within(yaw, MotionYawLimit)
}

// Move accepts only bounded planar velocity, never posture or mode changes.
// Zero velocity uses StopMove. An acknowledgement is not physical acceptance.
func (c *Connection) Move(ctx context.Context, forward, lateral, yaw float64) error {
	if !ValidMotion(forward, lateral, yaw) {
		return errors.New("Go2 velocity outside limits")
	}
	if forward == 0 && lateral == 0 && yaw == 0 {
		return c.Stop(ctx)
	}
	parameter, _ := json.Marshal(map[string]float64{"x": forward, "y": lateral, "z": yaw})
	return c.sportRequest(ctx, 1008, string(parameter))
}
func (c *Connection) Stop(ctx context.Context) error { return c.sportRequest(ctx, 1003, "") }

// StandUp uses RecoveryStand, which also works from lying down or a fall.
func (c *Connection) StandUp(ctx context.Context) error { return c.sportRequest(ctx, 1006, "") }
func (c *Connection) LieDown(ctx context.Context) error { return c.sportRequest(ctx, 1005, "") }
func (c *Connection) Hello(ctx context.Context) error   { return c.sportRequest(ctx, 1016, "") }

func (c *Connection) sportRequest(ctx context.Context, api uint64, parameter string) error {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	c.motionMu.Lock()
	defer c.motionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.Connected() || c.channel == nil || (api == 1008 && c.channel.BufferedAmount() != 0) {
		return errors.New("Go2 command channel unavailable or backlogged")
	}
	// Keep numeric identifiers exactly representable by browser/robot JSON parsers.
	id := c.requestID.Add(1) % 2147483648
	response := make(chan int, 1)
	c.responseMu.Lock()
	c.pendingID, c.pendingAPI, c.pendingResponse = id, api, response
	c.responseMu.Unlock()
	defer func() { c.responseMu.Lock(); c.pendingResponse = nil; c.responseMu.Unlock() }()
	payload := struct {
		Type  string `json:"type"`
		Topic string `json:"topic"`
		Data  struct {
			Header struct {
				Identity struct {
					ID  uint64 `json:"id"`
					API uint64 `json:"api_id"`
				} `json:"identity"`
			} `json:"header"`
			Parameter string `json:"parameter"`
		} `json:"data"`
	}{Type: "req", Topic: sportTopic}
	payload.Data.Header.Identity.ID, payload.Data.Header.Identity.API = id, api
	payload.Data.Parameter = parameter
	raw, _ := json.Marshal(payload)
	if err := c.channel.SendText(string(raw)); err != nil {
		return errors.New("Go2 command send failed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("Go2 disconnected before command acknowledgement")
	case code := <-response:
		if code != 0 {
			return errors.New("Go2 refused the teleop command")
		}
		return nil
	}
}

func (c *Connection) motionResponse(raw []byte) {
	var reply struct {
		Type  string `json:"type"`
		Topic string `json:"topic"`
		Data  struct {
			Header struct {
				Identity struct {
					ID  uint64 `json:"id"`
					API uint64 `json:"api_id"`
				} `json:"identity"`
				Status struct {
					Code *int `json:"code"`
				} `json:"status"`
			} `json:"header"`
		} `json:"data"`
	}
	// The reference future_resolver routes API replies by identity, not by
	// the request topic. Do not assume firmware echoes that topic on replies.
	if json.Unmarshal(raw, &reply) != nil || reply.Type != "res" || reply.Data.Header.Status.Code == nil {
		return
	}
	c.responseMu.Lock()
	defer c.responseMu.Unlock()
	if c.pendingResponse != nil && reply.Data.Header.Identity.ID == c.pendingID && reply.Data.Header.Identity.API == c.pendingAPI {
		select {
		case c.pendingResponse <- *reply.Data.Header.Status.Code:
		default:
		}
	}
}
