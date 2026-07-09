package commands

import (
	"errors"
	"fmt"
	"testing"
)

func TestWithReason_NilPassthrough(t *testing.T) {
	if err := WithReason("install_download", nil); err != nil {
		t.Errorf("WithReason(_, nil) = %v, want nil", err)
	}
}

func TestWithReason_TagsAndUnwraps(t *testing.T) {
	base := errors.New("boom")
	tagged := WithReason("install_disk_write", base)

	var re *ReasonError
	if !errors.As(tagged, &re) {
		t.Fatal("tagged error is not a *ReasonError")
	}
	if re.Reason != "install_disk_write" {
		t.Errorf("Reason = %q, want %q", re.Reason, "install_disk_write")
	}
	if !errors.Is(tagged, base) {
		t.Error("tagged error lost the wrapped cause (errors.Is failed)")
	}
	if tagged.Error() != "boom" {
		t.Errorf("Error() = %q, want %q", tagged.Error(), "boom")
	}
}

func TestWithReason_DoesNotOverwriteExistingReason(t *testing.T) {
	// The reason set closest to the failure site (install_download) must win,
	// even when an outer layer re-tags with a different reason.
	inner := WithReason("install_download", errors.New("net"))
	outer := WithReason("install_disk_write", fmt.Errorf("wrapping: %w", inner))

	var re *ReasonError
	if !errors.As(outer, &re) {
		t.Fatal("not a *ReasonError")
	}
	if re.Reason != "install_download" {
		t.Errorf("Reason = %q, want the inner %q", re.Reason, "install_download")
	}
}
