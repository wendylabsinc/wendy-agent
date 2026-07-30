package commands

import "testing"

func TestSelectExtension(t *testing.T) {
	exts := []extensionEntry{
		{Name: "wendyos-hello", KernelVersion: "6.12.87-v8-16k", Path: "images/x/0.17.0/wendyos-hello.raw"},
		{Name: "acme-npu", KernelVersion: "6.12.87-v8-16k", Path: "p1"},
		{Name: "acme-npu", KernelVersion: "6.6.0-other", Path: "p2"}, // same name, different kernel
		{Name: "no-kernel", KernelVersion: "", Path: "p3"},           // agent refuses these
	}
	dev := "6.12.87-v8-16k"

	tests := []struct {
		name, kernel string
		wantOK       bool
		wantPath     string
	}{
		{"wendyos-hello", dev, true, "images/x/0.17.0/wendyos-hello.raw"},
		{"acme-npu", dev, true, "p1"},                                    // picks the entry for THIS kernel
		{"acme-npu", "6.6.0-other", true, "p2"},                          // picks the other-kernel entry
		{"acme-npu", "9.9.9-nope", false, ""},                            // no entry for this kernel
		{"no-kernel", dev, false, ""},                                    // agent refuses an undeclared kernel
		{"missing", dev, false, ""},                                      // unknown name
		{"wendyos-hello", "", true, "images/x/0.17.0/wendyos-hello.raw"}, // empty kernel disables filter
	}
	for _, tt := range tests {
		got, ok := selectExtension(exts, tt.name, tt.kernel)
		if ok != tt.wantOK {
			t.Errorf("selectExtension(%q, %q) ok=%v, want %v", tt.name, tt.kernel, ok, tt.wantOK)
			continue
		}
		if ok && got.Path != tt.wantPath {
			t.Errorf("selectExtension(%q, %q) path=%q, want %q", tt.name, tt.kernel, got.Path, tt.wantPath)
		}
	}
}
