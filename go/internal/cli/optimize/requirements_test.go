package optimize

import "testing"

func TestParseRequirements(t *testing.T) {
	src := "# deps\n" +
		"--extra-index-url https://download.pytorch.org/whl/cpu\n" +
		"torch==2.3.0+cpu\n" +
		"numpy>=1.26\n" +
		"onnxruntime-gpu\n"

	r := ParseRequirements("requirements.txt", []byte(src))

	if len(r.Packages) != 3 {
		t.Fatalf("got %d packages, want 3: %+v", len(r.Packages), r.Packages)
	}
	if len(r.IndexURLs) != 1 || r.IndexURLs[0] != "https://download.pytorch.org/whl/cpu" {
		t.Fatalf("IndexURLs = %v", r.IndexURLs)
	}

	torch := r.Packages[0]
	if torch.Name != "torch" || torch.VersionSpec != "==2.3.0" || torch.LocalLabel != "cpu" || torch.Line != 3 {
		t.Fatalf("torch = %+v", torch)
	}
	if r.Packages[2].Name != "onnxruntime-gpu" {
		t.Fatalf("third pkg = %+v, want onnxruntime-gpu", r.Packages[2])
	}
}

func TestParseRequirementsEqualsIndexURL(t *testing.T) {
	r := ParseRequirements("requirements.txt", []byte("--index-url=https://download.pytorch.org/whl/cpu\ntorch\n"))
	if len(r.IndexURLs) != 1 || r.IndexURLs[0] != "https://download.pytorch.org/whl/cpu" {
		t.Fatalf("IndexURLs = %v, want one whl/cpu url", r.IndexURLs)
	}
}
