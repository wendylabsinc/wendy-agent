package commands

import (
	"errors"
	"fmt"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
)

func TestBuildFailureIsUnrecoverable(t *testing.T) {
	err := wrapBuildError(errors.New("exit status 1"))
	if diag.Classify(err) != diag.Unrecoverable {
		t.Errorf("wrapped build error should be unrecoverable, got %v", diag.Classify(err))
	}
}

func TestProviderBuildFailureIsUnrecoverable(t *testing.T) {
	err := wrapBuildError(fmt.Errorf("provider build: %w", errors.New("exit status 1")))
	if diag.Classify(err) != diag.Unrecoverable {
		t.Errorf("provider build failure should be unrecoverable, got %v", diag.Classify(err))
	}
}
