package worker

// client.update（P4 自更新）：下载 → 校验 → 验签 → 原子替换自身。
// v1 约定：替换完成后**不自动退出**（结果带回 restart_required: true），
// 由 systemd/LaunchAgent 拉起新版本（§5.1 自启同款机制）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

const updateBinName = "gs-worker"

func (e *Engine) handleClientUpdate(ctx context.Context, payload map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(payload)
	var p proto.ClientUpdatePayload
	if err := json.Unmarshal(raw, &p); err != nil || p.SHA256 == "" || p.URL == "" {
		return nil, errors.New("bad client.update payload")
	}
	if e.Cfg.SigningPubkey == "" {
		return nil, errors.New("no signing_pubkey configured — refusing update")
	}
	// 下载 + sha256 校验
	blob, err := e.downloadBundle(ctx, p.URL)
	if err != nil {
		return nil, err
	}
	if actual := sha256HexOfBytes(blob); actual != p.SHA256 {
		return nil, fmt.Errorf("sha256 mismatch: want %s got %s", p.SHA256[:8], actual[:8])
	}
	// 更新包验签（独立签名域，D8 同源密钥）
	if err := skillpkg.VerifyUpdate(e.Cfg.SigningPubkey, p.SHA256, p.Signature); err != nil {
		return nil, fmt.Errorf("update signature: %w", err)
	}
	// 解包（tar.gz 内含单个 updateBinName）
	tmpDir, err := os.MkdirTemp(e.Home.Dir, ".gs-evo-update-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	if err := skillpkg.ExtractTarGz(bytes.NewReader(blob), tmpDir); err != nil {
		return nil, fmt.Errorf("extract update: %w", err)
	}
	newBin := filepath.Join(tmpDir, updateBinName)
	if _, err := os.Stat(newBin); err != nil {
		return nil, fmt.Errorf("update package missing %s", updateBinName)
	}

	staged := filepath.Join(e.Home.Dir, "bin")
	_ = os.MkdirAll(staged, 0o755)
	stagedBin := filepath.Join(staged, updateBinName+"-"+p.Version)
	if err := copyFile(newBin, stagedBin, 0o755); err != nil {
		return nil, err
	}

	if e.Cfg.UpdateStub {
		// 测试形态（engine 内嵌在别的进程里跑）：验证整条链路后止步于落盘
		e.Log.Info("client update staged (stub)", "version", p.Version, "staged", stagedBin)
		data, _ := structToMap(map[string]any{"updated": true, "staged": true, "version": p.Version})
		return data, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if err := swapBinary(exe, stagedBin); err != nil {
		return nil, fmt.Errorf("swap binary: %w", err)
	}
	e.Log.Info("client updated", "version", p.Version, "binary", exe)
	data, _ := structToMap(map[string]any{
		"updated": true, "version": p.Version,
		"restart_required": true, // v1 不自动退出：由自启机制/人工重启拉起新版
	})
	return data, nil
}

// swapBinary 原子替换可执行文件：current ← staged（旧版留 .old 一代）。
func swapBinary(current, staged string) error {
	old := current + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(current, old); err != nil {
		return err
	}
	if err := os.Rename(staged, current); err != nil {
		_ = os.Rename(old, current) // 恢复现场
		return err
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
