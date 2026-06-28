package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMaybeRunCrashReportSkipsRecoverable(t *testing.T) {
	t.Setenv("CI", "")            // ensure not classified as CI
	t.Setenv("WENDY_CRASHREPORT", "true")
	// Recoverable error must be a no-op (no panic, returns cleanly).
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		status.Error(codes.Unavailable, "down"), "grpc_unavailable")
}

func TestMaybeRunCrashReportSkipsInCI(t *testing.T) {
	t.Setenv("CI", "1")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		errors.New("boom"), "other")
}

func TestMaybeRunCrashReportSkipsWhenDisabled(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("WENDY_CRASHREPORT", "false")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		status.Error(codes.Internal, "boom"), "grpc_other")
}
