package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSwapBinary(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "gs-worker")
	if err := os.WriteFile(cur, []byte("v1-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "gs-worker-2")
	if err := os.WriteFile(staged, []byte("v2-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(cur, staged); err != nil {
		t.Fatalf("swap: %v", err)
	}
	got, err := os.ReadFile(cur)
	if err != nil || string(got) != "v2-binary" {
		t.Fatalf("current binary not replaced: %v %s", err, got)
	}
	old, err := os.ReadFile(cur + ".old")
	if err != nil || string(old) != "v1-binary" {
		t.Fatalf(".old not kept: %v", err)
	}
}
