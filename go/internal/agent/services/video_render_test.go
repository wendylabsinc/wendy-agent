package services

import (
	"strings"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func fullElements() map[string]bool {
	return map[string]bool{
		thermalNormalizeElement: true,
		"coloreffects":          true,
	}
}

// TestRenderSegmentRawIsInert is the contract that protects analytic consumers:
// the default mode must not add anything to the pipeline, so pixel values reach
// the encoder untouched.
func TestRenderSegmentRawIsInert(t *testing.T) {
	seg, err := renderSegment(agentpb.RenderMode_RENDER_MODE_RAW, fullElements())
	if err != nil {
		t.Fatalf("raw render must never error: %v", err)
	}
	if seg != "" {
		t.Fatalf("raw render must add no elements, got %q", seg)
	}
	// Also inert when nothing is installed — raw must not depend on optional plugins.
	if seg, err := renderSegment(agentpb.RenderMode_RENDER_MODE_RAW, map[string]bool{}); err != nil || seg != "" {
		t.Fatalf("raw render must be inert without plugins, got %q / %v", seg, err)
	}
}

func TestRenderSegmentThermal(t *testing.T) {
	seg, err := renderSegment(agentpb.RenderMode_RENDER_MODE_THERMAL, fullElements())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{thermalNormalizeElement, "coloreffects preset=heat", "videoconvert"} {
		if !strings.Contains(seg, want) {
			t.Errorf("thermal segment missing %q; got %q", want, seg)
		}
	}
	// Must be terminated so it splices in front of the encoder.
	if !strings.HasSuffix(seg, " ! ") {
		t.Errorf("segment must end with a pipeline separator, got %q", seg)
	}
}

// TestRenderSegmentThermalMissingPlugin: a missing optional plugin must produce
// a specific, actionable error rather than letting the pipeline fail later with
// "Internal data stream error", which is what sent an earlier investigation
// chasing the wrong layer entirely.
func TestRenderSegmentThermalMissingPlugin(t *testing.T) {
	for _, missing := range []string{thermalNormalizeElement, "coloreffects"} {
		els := fullElements()
		delete(els, missing)
		_, err := renderSegment(agentpb.RenderMode_RENDER_MODE_THERMAL, els)
		if err == nil {
			t.Fatalf("expected an error when %s is absent", missing)
		}
		if status.Code(err) != codes.FailedPrecondition {
			t.Errorf("want FailedPrecondition for missing %s, got %v", missing, status.Code(err))
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error should name the missing element %s; got %v", missing, err)
		}
	}
}

func TestRenderSegmentUnknownMode(t *testing.T) {
	_, err := renderSegment(agentpb.RenderMode(99), fullElements())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for an unknown mode, got %v", status.Code(err))
	}
}

// TestEnumerateFrameSizesUnopenable: a device we cannot open reports no modes
// rather than failing, so one bad node never breaks the whole listing.
func TestEnumerateFrameSizesUnopenable(t *testing.T) {
	if got := enumerateFrameSizes("/definitely/not/a/device"); got != nil {
		t.Fatalf("expected nil for an unopenable device, got %v", got)
	}
	if got := enumerateFrameSizes(""); got != nil {
		t.Fatalf("expected nil for an empty path, got %v", got)
	}
}
