package diag

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyUnrecoverableGRPC(t *testing.T) {
	if got := Classify(status.Error(codes.Internal, "boom")); got != Unrecoverable {
		t.Errorf("Internal => %q, want unrecoverable", got)
	}
	if got := Classify(status.Error(codes.Unknown, "boom")); got != Unrecoverable {
		t.Errorf("Unknown => %q, want unrecoverable", got)
	}
}

func TestClassifyRecoverable(t *testing.T) {
	if got := Classify(status.Error(codes.Unavailable, "down")); got != Recoverable {
		t.Errorf("Unavailable => %q, want recoverable", got)
	}
	if got := Classify(errors.New("plain")); got != Recoverable {
		t.Errorf("plain => %q, want recoverable", got)
	}
}

func TestClassifyBuildFailure(t *testing.T) {
	err := MarkBuildFailure(errors.New("docker build failed"))
	if got := Classify(err); got != Unrecoverable {
		t.Errorf("build failure => %q, want unrecoverable", got)
	}
}

func TestWrapAndChain(t *testing.T) {
	base := errors.New("connection refused")
	werr := Wrap(base, "deploy").WithDevice("orin").WithStage("push")
	chain := Chain(werr)
	if chain == "" || !strings.Contains(chain, "connection refused") || !strings.Contains(chain, "deploy") {
		t.Errorf("Chain = %q", chain)
	}
	if !errors.Is(werr, base) {
		t.Error("DiagError must unwrap to base")
	}
	_ = fmt.Sprint(werr)
}
