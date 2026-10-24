package platform_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform/platformtest"
)

func TestSignerPreservesExactMessageAndPublicIdentity(t *testing.T) {
	// Opaque test bytes; these tests never implement cryptography.
	message := []byte{'a', '.', 0, 255, '.', 'z'}
	chain := [][]byte{{0x30, 1}, {0x30, 2}}
	publicKey := []byte{0x30, 3}
	signature := []byte{0, 255, 4}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "host request")
	checkContext := func(got context.Context) {
		t.Helper()
		if got.Value(contextKey{}) != "host request" {
			t.Fatal("signer lost the call context")
		}
	}
	var signer platform.Signer = platformtest.Signer{
		AlgorithmFunc: func() platform.SignatureAlgorithm { return platform.MLDSA65 },
		CertificateChainFunc: func(ctx context.Context) ([][]byte, error) {
			checkContext(ctx)
			return chain, nil
		},
		PublicKeyFunc: func(ctx context.Context) ([]byte, error) {
			checkContext(ctx)
			return publicKey, nil
		},
		SignFunc: func(ctx context.Context, got []byte) ([]byte, error) {
			checkContext(ctx)
			if !bytes.Equal(got, message) {
				t.Fatalf("host received modified signing input: %x", got)
			}
			return signature, nil
		},
	}
	if signer.Algorithm() != platform.MLDSA65 {
		t.Fatal("signer lost the algorithm")
	}
	if got, err := signer.CertificateChain(ctx); err != nil || !reflect.DeepEqual(got, chain) {
		t.Fatalf("certificate chain = %x, %v", got, err)
	}
	if got, err := signer.PublicKey(ctx); err != nil || !bytes.Equal(got, publicKey) {
		t.Fatalf("public key = %x, %v", got, err)
	}
	if got, err := signer.Sign(ctx, message); err != nil || !bytes.Equal(got, signature) {
		t.Fatalf("signature = %x, %v", got, err)
	}
}

func TestUnaryPreservesPayloadMetadataAndDeadline(t *testing.T) {
	metadata := platform.Metadata{
		{Name: "x-repeat", Value: []byte("one")},
		{Name: "x-repeat", Value: []byte("two")},
		{Name: "test-bin", Value: []byte{0, 255, 128}},
	}
	request := platform.RPCRequest{
		Method: "/test.Service/Unary", Message: []byte{0, 255}, Metadata: metadata,
	}
	response := platform.RPCResponse{
		Message: []byte{0, 128}, InitialMetadata: metadata,
		TrailingMetadata: platform.Metadata{{Name: "result-bin", Value: []byte{255, 0}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var transport platform.Transport = platformtest.Transport{
		UnaryFunc: func(ctx context.Context, got platform.RPCRequest) (platform.RPCResponse, error) {
			if gotDeadline, ok := ctx.Deadline(); !ok || !gotDeadline.Equal(deadline) {
				t.Fatal("host lost the deadline")
			}
			if !reflect.DeepEqual(got, request) {
				t.Fatalf("host received modified request: %#v", got)
			}
			return response, nil
		},
	}
	got, err := transport.Unary(ctx, request)
	if err != nil || !reflect.DeepEqual(got, response) {
		t.Fatalf("response = %#v, %v", got, err)
	}
}

func TestRPCStatusSurvivesWrappingWithoutDisplayingDiagnostics(t *testing.T) {
	cause := errors.New("host diagnostic")
	status := &platform.RPCStatusError{
		Code: platform.RPCAlreadyExists, Message: "remote diagnostic",
		InitialMetadata:  platform.Metadata{{Name: "initial", Value: []byte("header")}},
		TrailingMetadata: platform.Metadata{{Name: "details-bin", Value: []byte{0, 255}}},
		Cause:            cause,
	}
	var transport platform.Transport = platformtest.Transport{
		UnaryFunc: func(context.Context, platform.RPCRequest) (platform.RPCResponse, error) {
			return platform.RPCResponse{}, fmt.Errorf("host RPC: %w", status)
		},
	}
	_, err := transport.Unary(context.Background(), platform.RPCRequest{})
	var got *platform.RPCStatusError
	if !errors.As(err, &got) || got.Code != platform.RPCAlreadyExists ||
		!reflect.DeepEqual(got.InitialMetadata, status.InitialMetadata) ||
		!reflect.DeepEqual(got.TrailingMetadata, status.TrailingMetadata) ||
		got.Message != status.Message || !errors.Is(err, cause) {
		t.Fatalf("status lost across host boundary: %#v, %v", got, err)
	}
	if err.Error() != "host RPC: RPC failed with status 6" {
		t.Fatalf("error displayed host diagnostics: %s", err)
	}
	unknown := &platform.RPCStatusError{Code: 999}
	if !strings.Contains(unknown.Error(), "999") || unknown.Code != 999 {
		t.Fatal("unknown wire status was lost")
	}
}

func TestRPCStatusMatchesContextErrorsWithoutLosingHostCause(t *testing.T) {
	hostCause := errors.New("native host status")
	for _, test := range []struct {
		code platform.RPCStatusCode
		want error
	}{
		{platform.RPCCanceled, context.Canceled},
		{platform.RPCDeadlineExceeded, context.DeadlineExceeded},
		{platform.RPCUnavailable, nil},
	} {
		err := fmt.Errorf("host call: %w", &platform.RPCStatusError{Code: test.code, Cause: hostCause})
		if !errors.Is(err, hostCause) {
			t.Fatalf("code %d lost the native host cause", test.code)
		}
		for _, target := range []error{context.Canceled, context.DeadlineExceeded} {
			if got := errors.Is(err, target); got != (test.want == target) {
				t.Fatalf("code %d matches %v = %t", test.code, target, got)
			}
		}
	}
}

func TestHostCallsHonorCancellationAndExpiredDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			signer := platformtest.Signer{
				SignFunc: func(ctx context.Context, _ []byte) ([]byte, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
			_, err := signer.Sign(ctx, []byte("exact input"))
			if !errors.Is(err, want) {
				t.Fatalf("signing error = %v, want %v", err, want)
			}
			statusCode := platform.RPCCanceled
			if deadline {
				statusCode = platform.RPCDeadlineExceeded
			}
			hostError := func(ctx context.Context) error {
				<-ctx.Done()
				return &platform.RPCStatusError{Code: statusCode, Cause: ctx.Err()}
			}
			transport := platformtest.Transport{
				UnaryFunc: func(ctx context.Context, _ platform.RPCRequest) (platform.RPCResponse, error) {
					return platform.RPCResponse{}, hostError(ctx)
				},
				ServerStreamFunc: func(ctx context.Context, _ platform.RPCRequest) (platform.ServerStream, error) {
					return nil, hostError(ctx)
				},
			}
			_, unaryErr := transport.Unary(ctx, platform.RPCRequest{})
			_, openErr := transport.ServerStream(ctx, platform.RPCRequest{})
			for _, err := range []error{unaryErr, openErr} {
				var status *platform.RPCStatusError
				if !errors.Is(err, want) || !errors.As(err, &status) || status.Code != statusCode {
					t.Fatalf("transport lost context/status error: %v", err)
				}
			}
		})
	}
}

func TestServerStreamMessagesAndTerminalStatus(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "EOF"
		if failed {
			name = "status"
		}
		t.Run(name, func(t *testing.T) {
			initial := platform.Metadata{{Name: "header", Value: []byte("value")}}
			trailing := platform.Metadata{{Name: "details-bin", Value: []byte{0, 255}}}
			var terminal error = io.EOF
			if failed {
				terminal = &platform.RPCStatusError{
					Code: platform.RPCUnavailable, InitialMetadata: initial, TrailingMetadata: trailing,
				}
			}
			messages := [][]byte{{0, 255}, {}}
			next := 0
			finished := false
			closed := false
			transport := platformtest.Transport{
				ServerStreamFunc: func(context.Context, platform.RPCRequest) (platform.ServerStream, error) {
					return platformtest.ServerStream{
						HeaderFunc: func() (platform.Metadata, error) { return initial, nil },
						RecvFunc: func() ([]byte, error) {
							if next == len(messages) {
								finished = true
								return nil, terminal
							}
							message := messages[next]
							next++
							return message, nil
						},
						TrailerFunc: func() platform.Metadata {
							if !finished {
								return nil
							}
							return trailing
						},
						CloseFunc: func() error { closed = true; return nil },
					}, nil
				},
			}
			stream, err := transport.ServerStream(context.Background(), platform.RPCRequest{Method: "/test.Service/Watch"})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if stream.Trailer() != nil {
				t.Fatal("trailers available before completion")
			}
			if got, err := stream.Header(); err != nil || !reflect.DeepEqual(got, initial) {
				t.Fatalf("initial metadata = %#v, %v", got, err)
			}
			for _, want := range messages {
				if got, err := stream.Recv(); err != nil || !bytes.Equal(got, want) {
					t.Fatalf("stream message = %x, %v; want %x", got, err, want)
				}
			}
			for range 2 {
				if _, err := stream.Recv(); !errors.Is(err, terminal) {
					t.Fatalf("terminal result = %v, want %v", err, terminal)
				}
			}
			if got := stream.Trailer(); !reflect.DeepEqual(got, trailing) {
				t.Fatalf("trailing metadata = %#v", got)
			}
			if err := stream.Close(); err != nil || !closed {
				t.Fatalf("stream not released: %v", err)
			}
		})
	}
}

func TestServerStreamCancellationUnblocksHost(t *testing.T) {
	for _, operation := range []string{"Header", "Recv"} {
		for _, closeStream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/close=%t", operation, closeStream), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := make(chan struct{})
				transport := platformtest.Transport{
					ServerStreamFunc: func(ctx context.Context, _ platform.RPCRequest) (platform.ServerStream, error) {
						ctx, stop := context.WithCancel(ctx)
						var once sync.Once
						wait := func() error {
							once.Do(func() { close(started) })
							<-ctx.Done()
							return ctx.Err()
						}
						return platformtest.ServerStream{
							HeaderFunc:  func() (platform.Metadata, error) { return nil, wait() },
							RecvFunc:    func() ([]byte, error) { return nil, wait() },
							TrailerFunc: func() platform.Metadata { return nil },
							CloseFunc:   func() error { stop(); return nil },
						}, nil
					},
				}
				stream, err := transport.ServerStream(ctx, platform.RPCRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				result := make(chan error, 1)
				go func() {
					var err error
					if operation == "Header" {
						_, err = stream.Header()
					} else {
						_, err = stream.Recv()
					}
					result <- err
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("host call never started")
				}
				if closeStream {
					if err := stream.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					cancel()
				}
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("blocked call = %v, want cancellation", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancellation left host call blocked")
				}
				if err := stream.Close(); err != nil {
					t.Fatalf("repeated close failed: %v", err)
				}
			})
		}
	}
}
