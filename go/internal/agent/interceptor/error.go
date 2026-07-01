package interceptor

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isClientCanceled reports whether err is the normal cancellation that occurs
// when a client closes a stream — for example when `wendy run --detach` returns
// and tears down its deploy stream, or when `wendy device logs` is stopped with
// Ctrl-C. These are expected teardown, not handler failures, so they should not
// be logged at ERROR (they otherwise surface in `wendy device logs` and read as
// if the app had crashed).
func isClientCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled
}

func UnaryErrorInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Panic recovered in gRPC handler",
					zap.String("method", info.FullMethod),
					zap.Any("panic", r),
					zap.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()

		resp, err = handler(ctx, req)
		if err != nil {
			logger.Error("gRPC handler error",
				zap.String("method", info.FullMethod),
				zap.Error(err),
			)
		}
		return
	}
}

type wrappedStream struct {
	grpc.ServerStream
	logger *zap.Logger
	method string
}

func (w *wrappedStream) RecvMsg(m interface{}) error {
	return w.ServerStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m interface{}) error {
	return w.ServerStream.SendMsg(m)
}

func StreamErrorInterceptor(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Panic recovered in gRPC stream handler",
					zap.String("method", info.FullMethod),
					zap.String("panic", fmt.Sprintf("%v", r)),
					zap.String("stack", string(debug.Stack())),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()

		wrapped := &wrappedStream{
			ServerStream: ss,
			logger:       logger,
			method:       info.FullMethod,
		}

		err = handler(srv, wrapped)
		if err != nil {
			if isClientCanceled(err) {
				logger.Debug("gRPC stream closed by client",
					zap.String("method", info.FullMethod),
					zap.Error(err),
				)
			} else {
				logger.Error("gRPC stream handler error",
					zap.String("method", info.FullMethod),
					zap.Error(err),
				)
			}
		}
		return
	}
}
