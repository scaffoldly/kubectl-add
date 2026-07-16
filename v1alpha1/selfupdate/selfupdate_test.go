//go:build !noselfupdate

package selfupdate

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedInstall(t *testing.T) {
	// managedInstall normalizes separators, so slash literals cover every OS
	// (and a backslash krew path, as Windows would produce).
	managed := map[string]string{
		"/opt/homebrew/Cellar/kubectl-add/0.1.0/bin/kubectl-add": "Homebrew",
		"/nix/store/abc-kubectl-add-0.1.0/bin/kubectl-add":       "Nix",
		"/home/u/.krew/store/add/v0.1.0/kubectl-add":             "krew",
		`C:\Users\u\.krew\store\add\v0.1.0\kubectl-add.exe`:      "krew",
	}
	for path, want := range managed {
		if got, ok := managedInstall(path); !ok || got != want {
			t.Errorf("managedInstall(%q) = (%q, %v), want (%q, true)", path, got, ok, want)
		}
	}

	unmanaged := []string{
		"/home/u/.local/bin/kubectl_add_0.1.0",
		"/usr/local/bin/kubectl_add_0.1.0",
	}
	for _, path := range unmanaged {
		if got, ok := managedInstall(path); ok {
			t.Errorf("managedInstall(%q) = (%q, true), want false", path, got)
		}
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("the release archive bytes")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])

	// GitHub-style "<digest>  <filename>" and bare-digest both accepted.
	for _, sumFile := range []string{hexsum + "  kubectl-add_linux_amd64.zip\n", hexsum} {
		if err := verifyChecksum(data, []byte(sumFile)); err != nil {
			t.Errorf("verifyChecksum(valid) = %v", err)
		}
	}

	if err := verifyChecksum(data, []byte("deadbeef  x.zip")); err == nil {
		t.Error("verifyChecksum(mismatch): expected error")
	}
	if err := verifyChecksum(data, []byte("")); err == nil {
		t.Error("verifyChecksum(empty): expected error")
	}
}

func TestExtractBinary(t *testing.T) {
	name := "kubectl-add"
	if runtime.GOOS == "windows" {
		name = "kubectl-add.exe"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for fname, content := range map[string]string{
		name:        "BINARY-BYTES",
		"LICENSE":   "MIT",
		"README.md": "docs",
	} {
		w, err := zw.Create(fname)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()

	got, err := extractBinary(buf.Bytes(), name)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if string(got) != "BINARY-BYTES" {
		t.Errorf("extractBinary = %q, want BINARY-BYTES", got)
	}

	if _, err := extractBinary(buf.Bytes(), "not-present"); err == nil {
		t.Error("extractBinary(missing): expected error")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	versions := []string{"0.0.1", "0.1.0", "0.2.0", "0.3.0", "0.4.0"}
	for _, v := range versions {
		if err := os.WriteFile(filepath.Join(dir, "kubectl_add_"+v), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated file must survive pruning.
	os.WriteFile(filepath.Join(dir, "kubectl-add"), []byte("link-or-other"), 0o755)

	prune(dir, "kubectl_add_0.4.0", 3)

	kept := map[string]bool{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		kept[e.Name()] = true
	}
	// Newest 3 versioned binaries + the unrelated file remain.
	for _, want := range []string{"kubectl_add_0.4.0", "kubectl_add_0.3.0", "kubectl_add_0.2.0", "kubectl-add"} {
		if !kept[want] {
			t.Errorf("prune removed %q, expected kept", want)
		}
	}
	for _, gone := range []string{"kubectl_add_0.1.0", "kubectl_add_0.0.1"} {
		if kept[gone] {
			t.Errorf("prune kept %q, expected removed", gone)
		}
	}
}
