package commands

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestUnpackProgressTitleForPullingPhase(t *testing.T) {
	progress := &agentpb.CreateContainerProgress{
		Phase: agentpb.CreateContainerProgress_UNPACKING,
	}

	if got := unpackProgressTitle(progress); got != "Pulling image on device..." {
		t.Fatalf("title = %q; want pull title", got)
	}
}

func TestUnpackProgressTitleAndPercentForLayerUpdates(t *testing.T) {
	progress := &agentpb.CreateContainerProgress{
		Phase:          agentpb.CreateContainerProgress_APPLYING_LAYER,
		LayerIndex:     1,
		TotalLayers:    4,
		ReusedSnapshot: true,
	}

	if got := unpackProgressTitle(progress); got != "Unpacking image on device... (2/4 layers, reused snapshot)" {
		t.Fatalf("title = %q; want layer detail title", got)
	}

	if got := unpackProgressPercent(progress); got != 0.5 {
		t.Fatalf("percent = %v; want 0.5", got)
	}
}

func TestUnpackProgressDetailForPlan(t *testing.T) {
	progress := &agentpb.CreateContainerProgress{
		Phase:       agentpb.CreateContainerProgress_UNPACKING,
		TotalLayers: 4,
	}

	if got := unpackProgressDetail(progress); got != "Unpack plan: 4 layers" {
		t.Fatalf("detail = %q; want unpack plan", got)
	}
}

func TestUnpackProgressDetailForApplyingLayer(t *testing.T) {
	progress := &agentpb.CreateContainerProgress{
		Phase:       agentpb.CreateContainerProgress_UNPACKING,
		LayerIndex:  1,
		TotalLayers: 4,
		LayerSize:   2 * 1024 * 1024,
	}

	if got := unpackProgressDetail(progress); got != "Layer 2/4 applying (2.0 MiB)" {
		t.Fatalf("detail = %q; want applying layer detail", got)
	}

	if got := unpackProgressPercent(progress); got != 0.25 {
		t.Fatalf("percent = %v; want 0.25", got)
	}
}

func TestUnpackProgressDetailForReusedLayer(t *testing.T) {
	progress := &agentpb.CreateContainerProgress{
		Phase:          agentpb.CreateContainerProgress_APPLYING_LAYER,
		LayerIndex:     1,
		TotalLayers:    4,
		LayerSize:      512,
		ReusedSnapshot: true,
	}

	if got := unpackProgressDetail(progress); got != "Layer 2/4 reused snapshot (512 B)" {
		t.Fatalf("detail = %q; want reused layer detail", got)
	}
}

func TestProgressModelUserCancelled(t *testing.T) {
	model, _ := tui.NewProgress("Unpacking...").Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !progressModelUserCancelled(model) {
		t.Fatal("expected direct ctrl+c cancellation to be treated as user cancellation")
	}
}

func TestProgressModelUserCancelledIgnoresWrappedContextCanceled(t *testing.T) {
	model, _ := tui.NewProgress("Unpacking...").Update(tui.ProgressDoneMsg{
		Err: fmt.Errorf("creating container: %w", context.Canceled),
	})
	if progressModelUserCancelled(model) {
		t.Fatal("expected wrapped context cancellation to not be treated as direct user cancellation")
	}
}
