package skillpkg

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// ScanDir 扫描单个白名单根目录：含 SKILL.md 的一级子目录视为一个 skill。
// installedHashes: name → 安装时记录的 content_hash（来自 state.json），用于 local_state 判定。
func ScanDir(root string, installedHashes map[string]string) ([]proto.Manifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []proto.Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			continue // 无 SKILL.md 不是 skill
		}
		m, mErr := LoadManifest(dir, root, installedHashes)
		if mErr != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// LoadManifest 解析单个 skill 目录：front-matter 优先，缺省回退目录名/空值（§6.5）。
func LoadManifest(dir, rootLabel string, installedHashes map[string]string) (proto.Manifest, error) {
	fm, err := parseFrontMatter(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return proto.Manifest{}, err
	}
	hash, hashErr := HashDir(dir)
	if hashErr != nil {
		return proto.Manifest{}, hashErr
	}
	files := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, _ error) error {
		if d.Type().IsRegular() {
			files++
		}
		return nil
	})

	name := fm["name"]
	if name == "" {
		name = filepath.Base(dir)
	}
	version := fm["version"]
	if version == "" {
		version = "0"
	}
	m := proto.Manifest{
		Name:        name,
		Version:     version,
		Root:        rootLabel,
		Entry:       "SKILL.md",
		Summary:     fm["description"],
		ContentHash: "sha256:" + hash,
		Files:       files,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if caps := fm["capabilities"]; caps != "" {
		for _, c := range strings.Split(caps, ",") {
			if c = strings.TrimSpace(c); c != "" {
				m.Capabilities = append(m.Capabilities, c)
			}
		}
	}
	// local_state：与安装记录比对（§6.4 clean|modified；首次出现为 new）
	hashOnly := strings.TrimPrefix(m.ContentHash, "sha256:")
	switch {
	case installedHashes == nil:
		m.LocalState = "new"
	case installedHashes[name] == "":
		m.LocalState = "new"
	case installedHashes[name] == hashOnly:
		m.LocalState = "clean"
	default:
		m.LocalState = "modified"
	}
	return m, nil
}

// parseFrontMatter 极简 YAML front-matter 解析：仅支持 "---" 围栏内的 key: value 行。
func parseFrontMatter(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return out, nil // 无 front-matter
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		val = strings.Trim(val, `"'`)
		if key != "" {
			out[key] = val
		}
	}
	return out, nil
}
