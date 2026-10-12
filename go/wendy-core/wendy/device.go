package wendy

import (
	"context"
	"errors"
	"io"

	sdkv1 "github.com/wendylabsinc/townhall/gen/wendy/sdk/v1"
)

// ErrNotImplemented reports that the architecture scaffold has no product
// backend yet.
var ErrNotImplemented = errors.New("wendy SDK backend is not implemented")

// Device is an owned connection-capable device handle.
//
// Device values will eventually be created by Client discovery and connection
// operations. The draft constructor is intentionally absent so this scaffold
// does not invent connection semantics.
type Device struct{}

// Info returns the current immutable device information snapshot.
func (d *Device) Info(
	ctx context.Context,
	request *sdkv1.GetDeviceInfoRequest,
) (*sdkv1.DeviceInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNotImplemented
}

// WatchState returns a pull-based stream of complete device state snapshots.
func (d *Device) WatchState(
	ctx context.Context,
	request *sdkv1.WatchDeviceStateRequest,
) *Stream[*sdkv1.DeviceState] {
	return &Stream[*sdkv1.DeviceState]{context: ctx}
}

// Stream is a naturally backpressured, pull-based Go projection of a Wendy
// demand-driven stream.
type Stream[T any] struct {
	context context.Context
}

// Next waits for and returns one item. It returns io.EOF after normal
// completion. The scaffold currently returns ErrNotImplemented.
func (s *Stream[T]) Next(ctx context.Context) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if s.context != nil {
		if err := s.context.Err(); err != nil {
			return zero, err
		}
	}
	return zero, ErrNotImplemented
}

// Close ends the stream. The scaffold has no resources to release.
func (s *Stream[T]) Close() error {
	return nil
}

var _ = io.EOF // Documents the terminal contract before a backend exists.
