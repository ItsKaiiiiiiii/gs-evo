package worker

// Skill Manager：扫描/采集/安装/回滚（§5.1、§7.2、§7.3）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

// expandRoots 白名单目录（~ 展开 + 绝对化）；extra 非空时校验其必须是白名单子集（C10/§7.2）。
func (e *Engine) expandRoots(extra []string) []string {
	roots := make([]string, 0, len(e.Cfg.Roots))
	for _, r := range e.Cfg.Roots {
		roots = append(roots, absPath(expandHome(r)))
	}
	if len(extra) == 0 {
		return roots
	}
	var out []string
	for _, x := range extra {
		ax := absPath(expandHome(x))
		for _, r := range roots {
			if ax == r || strings.HasPrefix(ax, r+string(filepath.Separator)) {
				out = append(out, ax)
				break
			}
		}
	}
	return out
}

// inWhitelist 安装目标校验（§7.3 第 3 步：云端不能写任意路径）。
func (e *Engine) inWhitelist(target string) (string, bool) {
	at := absPath(expandHome(target))
	for _, r := range e.expandRoots(nil) {
		if at == r || strings.HasPrefix(at, r+string(filepath.Separator)) {
			return at, true
		}
	}
	return at, false
}

func (e *Engine) installedHashes() map[string]string {
	m := map[string]string{}
	for name, rec := range e.State.Installed {
		m[name] = rec.ContentHash
	}
	return m
}

// ---- skills.scan ----

func (e *Engine) Scan(ctx context.Context, payload map[string]any) (map[string]any, error) {
	var p proto.ScanPayload
	if b, err := json.Marshal(payload); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	roots := e.expandRoots(p.Roots)
	if len(roots) == 0 {
		return nil, errors.New("no whitelisted roots configured")
	}
	installed := e.installedHashes()
	var manifests []proto.Manifest
	for _, root := range roots {
		ms, err := skillScanDir(root, installed)
		if err != nil {
			continue // 根目录不存在等：跳过
		}
		manifests = append(manifests, ms...)
	}
	if manifests == nil {
		manifests = []proto.Manifest{}
	}
	data, _ := structToMap(proto.ScanResult{Manifests: manifests})
	return data, nil
}

// ---- skills.collect ----

func (e *Engine) handleCollect(ctx context.Context, payload map[string]any) (map[string]any, error) {
	var p proto.CollectPayload
	b, _ := json.Marshal(payload)
	if err := json.Unmarshal(b, &p); err != nil || p.Name == "" || p.Root == "" {
		return nil, errors.New("need name and root")
	}
	root := absPath(expandHome(p.Root))
	found := false
	var dir string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(path, "SKILL.md")); statErr == nil {
			m, mErr := skillLoadManifest(path, root, e.installedHashes())
			if mErr == nil && m.Name == p.Name {
				dir = path
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if !found {
		return nil, fmt.Errorf("skill %q not found under %s", p.Name, p.Root)
	}

	_ = os.MkdirAll(e.Home.CacheDir(), 0o700)
	tmp, err := os.CreateTemp(e.Home.CacheDir(), "pack-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	sha, size, err := skillPackDir(dir, tmp)
	_ = tmp.Close()
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}

	out := proto.CollectResult{Name: p.Name, ContentHash: "sha256:" + sha, Size: size}
	// manifest 里的版本随包一起走（云端以 manifest.version 入库）
	if m, err := skillLoadManifest(dir, root, e.installedHashes()); err == nil {
		out.Version = m.Version
	}

	if size < proto.InlineBundleLimit {
		blob, rerr := os.ReadFile(tmp.Name())
		if rerr != nil {
			return nil, rerr
		}
		out.BundleB64 = base64.StdEncoding.EncodeToString(blob)
	} else {
		// 大包：一次性票据上传（§6.6）
		if err := e.uploadBundle(ctx, sha, tmp.Name()); err != nil {
			return nil, err
		}
		out.Uploaded = true
	}
	data, _ := structToMap(out)
	return data, nil
}

func (e *Engine) uploadBundle(ctx context.Context, sha, path string) error {
	tr, err := e.apiJSON(ctx, "POST", "/api/v1/bundles/tickets", &proto.TicketRequest{SHA256: sha, Action: "upload"})
	if err != nil {
		return fmt.Errorf("ticket: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, "PUT", tr.URL, f)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("bundle upload: HTTP %d %s", resp.StatusCode, body)
	}
	return nil
}

// ---- skills.install ----

func (e *Engine) handleInstall(ctx context.Context, payload map[string]any) (map[string]any, error) {
	var p proto.InstallPayload
	b, _ := json.Marshal(payload)
	if err := json.Unmarshal(b, &p); err != nil || p.Name == "" || p.SHA256 == "" || p.TargetRoot == "" {
		return nil, errors.New("bad install payload")
	}
	// 1. 目标必须在白名单内（D10）
	target, ok := e.inWhitelist(p.TargetRoot)
	if !ok {
		return nil, fmt.Errorf("target_root %q not in local whitelist", p.TargetRoot)
	}
	// 2. 下载 → sha256 校验（§7.3 第 1 步）
	blob, err := e.downloadBundle(ctx, p.BundleURL)
	if err != nil {
		return nil, err
	}
	if actual := sha256HexOfBytes(blob); actual != p.SHA256 {
		return nil, fmt.Errorf("sha256 mismatch: want %s got %s", p.SHA256[:8], actual[:8])
	}
	e.Log.Debug("install step: verified", "name", p.Name, "bytes", len(blob))
	// 3. ed25519 验签（D8：非签名包拒装）
	if e.Cfg.SigningPubkey == "" {
		return nil, errors.New("no signing_pubkey configured — refusing install")
	}
	if err := skillpkg.Verify(e.Cfg.SigningPubkey, p.SHA256, p.Signature); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	// 4. 冲突检测：磁盘 hash vs state.json 记录（C6）
	ir := proto.InstallResult{Name: p.Name}
	prev := "none"
	if rec, exists := e.State.Installed[p.Name]; exists {
		prev = rec.Version
	}
	if actualDirHash, dirErr := skillHashDir(target); dirErr == nil {
		expected := ""
		if rec, exists := e.State.Installed[p.Name]; exists {
			expected = rec.ContentHash
		}
		if expected != "" && actualDirHash != expected && p.OnConflict != "overwrite" {
			ir.Result = "conflict_skipped"
			data, _ := structToMap(ir)
			return data, nil // 默认 skip_and_report，不静默覆盖
		}
	}
	// 5. 原子安装：tmp 解压 → 旧版移 .prev → rename（§7.3 第 5 步）
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(parent, ".gs-evo-tmp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := skillExtractTarGz(bytes.NewReader(blob), tmp); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	prevDir := target + ".prev"
	_ = os.RemoveAll(prevDir)
	if _, err := os.Stat(target); err == nil {
		if err := skillRename(target, prevDir); err != nil {
			return nil, fmt.Errorf("backup prev: %w", err)
		}
	}
	if err := skillRename(tmp, target); err != nil {
		// 回滚 prev 恢复现场
		if _, statErr := os.Stat(prevDir); statErr == nil {
			_ = skillRename(prevDir, target)
		}
		return nil, fmt.Errorf("install rename: %w", err)
	}
	if ents, rerr := os.ReadDir(target); rerr == nil {
		names := make([]string, 0, len(ents))
		for _, en := range ents {
			names = append(names, en.Name())
		}
		e.Log.Debug("install step: renamed into target", "target", target, "entries", strings.Join(names, ","))
	}
	ir.Result = "installed"
	ir.Installed = p.Version
	ir.Replaced = prev
	// 6. 回写 state.json
	e.mu.Lock()
	e.State.Installed[p.Name] = InstalledRec{Version: p.Version, ContentHash: p.SHA256, InstalledAt: nowRFC3339()}
	e.Home.SaveState(e.State)
	e.mu.Unlock()

	data, _ := structToMap(ir)
	return data, nil
}

func (e *Engine) downloadBundle(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bundle download: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

// apiJSON 带设备凭证的 REST 调用（票据申请等）。
func (e *Engine) apiJSON(ctx context.Context, method, path string, body any) (*proto.TicketResponse, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(e.Cfg.ServerURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.Cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, b)
	}
	out := new(proto.TicketResponse)
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, err
	}
	return out, nil
}

// rollbackLocal 本地秒回滚：current ↔ .prev（§7.3 回滚）。
func (e *Engine) rollbackLocal(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	root := ""
	for _, r := range e.expandRoots(nil) {
		root = r // 单根场景；多根时按 state 无从定位，取第一个含该 skill 的根
		m, err := skillScanDir(r, nil)
		if err == nil {
			for _, x := range m {
				if x.Name == name {
					root = r
				}
			}
		}
	}
	if root == "" {
		return fmt.Errorf("no whitelist roots")
	}
	var target string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if m, mErr := skillLoadManifest(path, root, nil); mErr == nil && m.Name == name {
			target = path
			return filepath.SkipAll
		}
		return nil
	})
	if target == "" {
		return fmt.Errorf("skill %q not found", name)
	}
	prev := target + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("no previous version kept for %q", name)
	}
	trash := target + ".gs-evo-rollback-tmp"
	_ = os.RemoveAll(trash)
	if err := skillRename(target, trash); err != nil {
		return err
	}
	if err := skillRename(prev, target); err != nil {
		_ = skillRename(trash, target)
		return err
	}
	_ = os.RemoveAll(trash)
	// 恢复 state：以回滚后的目录实际内容为准
	if m, err := skillLoadManifest(target, root, nil); err == nil {
		e.State.Installed[name] = InstalledRec{
			Version: m.Version, ContentHash: trimSha(m.ContentHash), InstalledAt: nowRFC3339(),
		}
	} else {
		delete(e.State.Installed, name)
	}
	e.Home.SaveState(e.State)
	return nil
}

func trimSha(h string) string { return strings.TrimPrefix(h, "sha256:") }

// ---- 小工具 ----

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func absPath(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return filepath.Clean(a)
}

func structToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}
