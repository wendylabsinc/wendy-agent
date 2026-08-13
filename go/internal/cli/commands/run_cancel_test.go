package commands

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRunWithInterruptChannelCancelsProviderBuildOnce(t *testing.T) {
	interrupts := make(chan os.Signal, 1)
	buildExited := false

	err := runWithInterruptChannel(context.Background(), interrupts, func(ctx context.Context) error {
		interrupts <- os.Interrupt
		<-ctx.Done()
		buildExited = true
		return errors.New("provider builder stopped")
	})

	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("err = %v, want ErrUserCancelled", err)
	}
	if !buildExited {
		t.Fatal("interrupt wrapper returned before provider build exited")
	}
}
