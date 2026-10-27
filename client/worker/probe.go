package worker

// agent.probe：探测本机 coding agent（路径/版本/可用性）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

// 探测对象与版本命令（后续 agent 类型在此追加，不动框架，§11）。
var probeTargets = []struct {
	Type string
	Bin  string
	Args []string
}{
	{"claude-code", "claude", []string{"--version"}},
	{"codex", "codex", []string{"--version"}},
	{"gemini", "gemini", []string{"--version"}},
	{"aider", "aider", []string{"--version"}},
	{"cursor-agent", "cursor-agent", []string{"--version"}},
}

const probeTTL = 5 * time.Minute

type probeCache struct {
	mu     sync.Mutex
	at     time.Time
	agents []proto.AgentInfo
}

var probes probeCache

func (e *Engine) ProbeAgents(ctx context.Context) []proto.AgentInfo {
	probes.mu.Lock()
	defer probes.mu.Unlock()
	if probes.agents != nil && time.Since(probes.at) < probeTTL {
		return probes.agents
	}
	var out []proto.AgentInfo
	for _, t := range probeTargets {
		info := proto.AgentInfo{Type: t.Type}
		path, err := exec.LookPath(t.Bin)
		if err != nil {
			out = append(out, info) // OK=false
			continue
		}
		info.Path = path
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		outBytes, err := exec.CommandContext(cctx, path, t.Args...).Output()
		cancel()
		if err == nil {
			info.OK = true
			info.Version = strings.TrimSpace(firstLine(string(outBytes)))
		}
		out = append(out, info)
	}
	probes.agents = out
	probes.at = time.Now()
	return out
}

func (e *Engine) handleProbe(ctx context.Context, payload map[string]any) (map[string]any, error) {
	data, _ := structToMap(proto.ProbeResult{Agents: e.ProbeAgents(ctx)})
	return data, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// ---- skillpkg 薄封装（便于测试替换/统一超时）----

func skillScanDir(root string, installed map[string]string) ([]proto.Manifest, error) {
	return skillpkg.ScanDir(root, installed)
}

func skillLoadManifest(dir, root string, installed map[string]string) (proto.Manifest, error) {
	return skillpkg.LoadManifest(dir, root, installed)
}

func skillPackDir(dir string, f *os.File) (string, int64, error) {
	return skillpkg.PackDir(dir, f)
}

func skillHashDir(dir string) (string, error) {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", err
	}
	return skillpkg.HashDir(dir)
}

func skillExtractTarGz(r *bytes.Reader, dest string) error {
	return skillpkg.ExtractTarGz(r, dest)
}

func skillRename(oldPath, newPath string) error {
	return skillpkg.RenameWithRetry(oldPath, newPath, 3)
}

func sha256HexOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func goos() string   { return runtime.GOOS }
func goarch() string { return runtime.GOARCH }
