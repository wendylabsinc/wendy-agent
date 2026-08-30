//go:build !linux

package ros2camera

import "errors"

type unsupportedWriter struct{}

func newFrameWriter(string) cameraWriter { return &unsupportedWriter{} }
func (*unsupportedWriter) WriteFrame(Frame) error {
	return errors.New("ROS 2 loopback cameras require Linux")
}
func (*unsupportedWriter) Close() error { return nil }
