package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestRunRejectsInvalidReadinessDeadlineBeforeExecuting(t *testing.T) {
	for _, value := range []any{-1, 0, 3601, 1.5, "invalid"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			srv := New(&config.Config{}, nil)
			result, err := srv.callTool(context.Background(), "run", map[string]any{
				"project_path": "/unused", "readiness_timeout_seconds": value,
			})
			if err != nil || !result.IsError || !strings.Contains(fmt.Sprint(result.Content), "readiness_timeout_seconds must be a whole number") {
				t.Fatalf("result=%v error=%v", result, err)
			}
		})
	}
}
