package skillpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackExtractRoundTrip(t *testing.T) {
	src := writeTree(t, map[string]string{
		"SKILL.md":     "---\nname: demo\nversion: 2\n---\nbody",
		"scripts/a.sh": "echo hi",
	})
	var buf bytes.Buffer
	sha, size, err := PackDir(src, &buf)
	if err != nil || size == 0 || len(sha) != 64 {
		t.Fatalf("pack: %v size=%d", err, size)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ExtractTarGz(bytes.NewReader(buf.Bytes()), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || !strings.Contains(string(got), "version: 2") {
		t.Fatalf("roundtrip content: %v %s", err, got)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts/a.sh")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
	// 同内容重复打包 → hash 稳定（内容寻址的前提）
	var buf2 bytes.Buffer
	sha2, _, err := PackDir(src, &buf2)
	if err != nil || sha2 != sha {
		t.Fatalf("hash unstable: %s vs %s", sha, sha2)
	}
	// 内容变化 → hash 变化
	_ = os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: demo\nversion: 3\n---\nbody"), 0o644)
	var buf3 bytes.Buffer
	sha3, _, _ := PackDir(src, &buf3)
	if sha3 == sha {
		t.Fatal("hash must change with content")
	}
}

func TestExtractRejectsUnsafeEntries(t *testing.T) {
	dest := t.TempDir()
	// 手工构造带 ../ 的 gzip+tar（绕过打包器）
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	if err := ExtractTarGz(bytes.NewReader(buf.Bytes()), dest); err == nil {
		t.Fatal("unsafe entry must be rejected")
	}
	if _, err := os.Stat(filepath.Join(dest, "evil.txt")); err == nil {
		t.Fatal("path traversal succeeded — security bug")
	}
}

func TestFrontMatter(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"my-skill/SKILL.md": "---\nname: my-skill\nversion: 7\ndescription: does things\ncapabilities: a.b, c.d\n---\n# body",
	})
	ms, err := ScanDir(dir, nil)
	if err != nil || len(ms) != 1 {
		t.Fatalf("scan: %v %d", err, len(ms))
	}
	m := ms[0]
	if m.Name != "my-skill" || m.Version != "7" || m.Summary != "does things" {
		t.Fatalf("manifest fields: %+v", m)
	}
	if len(m.Capabilities) != 2 || m.Capabilities[1] != "c.d" {
		t.Fatalf("capabilities: %v", m.Capabilities)
	}
	if m.LocalState != "new" {
		t.Fatalf("local_state: %s", m.LocalState)
	}
	// 安装记录比对：clean / modified
	hash := strings.TrimPrefix(m.ContentHash, "sha256:")
	if m2, _ := LoadManifest(filepath.Join(dir, "my-skill"), dir, map[string]string{"my-skill": hash}); m2.LocalState != "clean" {
		t.Fatalf("want clean, got %s", m2.LocalState)
	}
	if m3, _ := LoadManifest(filepath.Join(dir, "my-skill"), dir, map[string]string{"my-skill": "deadbeef"}); m3.LocalState != "modified" {
		t.Fatalf("want modified, got %s", m3.LocalState)
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("ab", 32)
	sig, err := Sign(priv, sha)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(pub, sha, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify(pub, strings.Repeat("cd", 32), sig); err == nil {
		t.Fatal("verify must fail for different content")
	}
	if err := Verify(pub, sha, strings.Repeat("00", 64)); err == nil {
		t.Fatal("verify must fail for bad signature")
	}
}

func TestPackDeterministicIgnoreMtime(t *testing.T) {
	src := writeTree(t, map[string]string{"SKILL.md": "---\nname: d\nversion: 1\n---\nsame"})
	var b1, b2 bytes.Buffer
	sha1, _, err := PackDir(src, &b1)
	if err != nil {
		t.Fatal(err)
	}
	// touch：只改 mtime，不改内容
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(src, "SKILL.md"), future, future); err != nil {
		t.Fatal(err)
	}
	sha2, _, err := PackDir(src, &b2)
	if err != nil {
		t.Fatal(err)
	}
	if sha1 != sha2 {
		t.Fatalf("content hash must ignore mtime: %s vs %s", sha1[:8], sha2[:8])
	}
}
