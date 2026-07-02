package t234

import (
	"os"
	"path/filepath"
	"testing"
)

// realRCMBootCmd is a real rcmbootcmd.txt as tegraflash's sign step writes it
// for the AGX Orin (0.16.1 bundle).
const realRCMBootCmd = `./tegrarcm_v2 --new_session --chip 0x23 0 --uid --download bct_br br_bct_BR.bct --download mb1 mb1_t234_prod_aligned_sigheader.bin.encrypt --download psc_bl1 psc_bl1_t234_prod_aligned_sigheader.bin.encrypt --download bct_mb1 mb1_bct_MB1_sigheader.bct.encrypt
./tegrarcm_v2 --chip 0x23 0 --pollbl --download bct_mem mem_rcm_sigheader.bct.encrypt --download blob blob.bin
`

func writeRCMFixture(t *testing.T, cmdText string, files []string) string {
	t.Helper()
	bundle := t.TempDir()
	dir := filepath.Join(bundle, RCMBootBlobDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rcmbootcmd.txt"), []byte(cmdText), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}

func TestParseRCMBootCmd(t *testing.T) {
	bundle := writeRCMFixture(t, realRCMBootCmd, []string{
		"br_bct_BR.bct",
		"mb1_t234_prod_aligned_sigheader.bin.encrypt",
		"psc_bl1_t234_prod_aligned_sigheader.bin.encrypt",
		"mb1_bct_MB1_sigheader.bct.encrypt",
		"mem_rcm_sigheader.bct.encrypt",
		"blob.bin",
	})
	boot, err := ParseRCMBootCmd(bundle)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"br_bct_BR.bct",
		"mb1_t234_prod_aligned_sigheader.bin.encrypt",
		"psc_bl1_t234_prod_aligned_sigheader.bin.encrypt",
		"mb1_bct_MB1_sigheader.bct.encrypt",
	}
	if len(boot.SendOrder) != len(wantOrder) {
		t.Fatalf("send order %v", boot.SendOrder)
	}
	for i, w := range wantOrder {
		if boot.SendOrder[i] != w {
			t.Errorf("send order [%d] = %q, want %q", i, boot.SendOrder[i], w)
		}
	}
	if boot.MemBCT != "mem_rcm_sigheader.bct.encrypt" {
		t.Errorf("MemBCT = %q", boot.MemBCT)
	}
	if boot.Blob != "blob.bin" {
		t.Errorf("Blob = %q", boot.Blob)
	}
}

func TestParseRCMBootCmdMissingArtifact(t *testing.T) {
	bundle := writeRCMFixture(t, realRCMBootCmd, []string{"br_bct_BR.bct"})
	if _, err := ParseRCMBootCmd(bundle); err == nil {
		t.Fatal("expected missing-artifact error")
	}
}

func TestParseRCMBootCmdWrongShape(t *testing.T) {
	bundle := writeRCMFixture(t, "./tegrarcm_v2 --chip 0x23 0\n", nil)
	if _, err := ParseRCMBootCmd(bundle); err == nil {
		t.Fatal("expected shape error")
	}
}
