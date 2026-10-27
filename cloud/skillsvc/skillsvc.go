// Package skillsvc 是 Skill Registry 的业务逻辑：
// 采集入库（§7.2）、版本状态机 + 签名（D8/D9）、安装指令工厂（§7.3）。
package skillsvc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

// 合法状态迁移（§7.5：draft→candidate→reviewed→stable→deprecated）。
var transitions = map[string][]string{
	"draft":      {"candidate", "deprecated"},
	"candidate":  {"reviewed", "deprecated"},
	"reviewed":   {"stable", "candidate"}, // 允许打回
	"stable":     {"deprecated"},
	"deprecated": {},
}

var ErrNotFound = errors.New("skill or version not found")
var ErrBadTransition = errors.New("invalid status transition")
var ErrNotStable = errors.New("version is not stable (must be signed stable to install, D8/D9)")

type Service struct {
	st        *store.Store
	log       *slog.Logger
	bundleDir string // 内容寻址存储根
	privHex   string // ed25519 私钥（stable 签名用）

	// CmdIssuer 创建指令并尝试投递（api 装配层注入，避免环形依赖）。
	CmdIssuer func(ctx context.Context, deviceID, cmdType string, payload map[string]any, createdBy string) (*store.Command, error)
	// TicketURL 为指定设备生成一次性下载票据 URL。
	TicketURL func(ctx context.Context, deviceID, sha256Hex string) (string, error)
}

func New(st *store.Store, log *slog.Logger, bundleDir, privHex string) *Service {
	return &Service{st: st, log: log, bundleDir: bundleDir, privHex: privHex}
}

func (s *Service) bundlePath(sha string) string {
	return filepath.Join(s.bundleDir, sha[:2], sha+".tar.gz")
}

// ---- 采集入库（§7.2）----

// IngestCollectResult 消费 skills.collect 的 cmd_result：内联 bundle 落盘或校验票据上传的 bundle，
// 内容去重后创建 draft 版本（source=collected）。
func (s *Service) IngestCollectResult(deviceID string, res *proto.CmdResult) error {
	if !res.OK {
		return nil
	}
	raw, _ := json.Marshal(res.Data)
	var cr proto.CollectResult
	if err := json.Unmarshal(raw, &cr); err != nil || cr.Name == "" || cr.ContentHash == "" {
		return fmt.Errorf("bad collect result: %w", err)
	}
	sha := trimHashPrefix(cr.ContentHash)

	// bundle 落盘（内联解码；票据上传的须已存在）
	path := s.bundlePath(sha)
	if cr.BundleB64 != "" {
		blob, err := base64.StdEncoding.DecodeString(cr.BundleB64)
		if err != nil {
			return fmt.Errorf("bad bundle b64: %w", err)
		}
		if err := writeBundle(path, blob, sha); err != nil {
			return err
		}
		if err := s.st.UpsertBundle(sha, int64(len(blob))); err != nil {
			return err
		}
	} else if ok, _, err := s.st.BundleExists(sha); err != nil || !ok {
		if err != nil {
			return err
		}
		return fmt.Errorf("bundle %s missing (upload incomplete)", sha)
	}

	tx, err := s.st.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sk, err := s.st.UpsertSkillCollected(tx, cr.Name)
	if err != nil {
		return err
	}
	dup, err := s.st.ContentExists(tx, sk.ID, sha)
	if err != nil {
		return err
	}
	if dup {
		s.log.Info("collect dedup: content already registered", "skill", cr.Name, "sha", sha[:8])
		return tx.Commit()
	}
	manifest, _ := json.Marshal(map[string]any{
		"name": cr.Name, "version": cr.Version, "content_hash": cr.ContentHash, "size": cr.Size,
	})
	v := &store.SkillVersionRow{
		SkillID: sk.ID, Version: cr.Version, Manifest: manifest,
		ContentSHA256: sha, Size: cr.Size, OriginDevice: deviceID,
		Source: "collected",
	}
	if err := s.st.InsertVersion(tx, v); err != nil {
		return fmt.Errorf("insert version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.log.Info("skill collected", "skill", cr.Name, "version", cr.Version, "from", deviceID, "sha", sha[:8])
	return nil
}

func writeBundle(path string, blob []byte, sha string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	// 落盘前校验内容哈希（§6.6）
	if got := sha256HexOf(blob); got != sha {
		_ = os.Remove(tmp)
		return fmt.Errorf("bundle hash mismatch: want %s got %s", sha[:8], got[:8])
	}
	return os.Rename(tmp, path)
}

// StoreUploadedBundle 处理票据 PUT 上传的字节流（api 层已边收边算 hash）。
func (s *Service) StoreUploadedBundle(sha string, size int64) error {
	return s.st.UpsertBundle(sha, size)
}

// ---- 状态机 + 签名（D8/D9）----

// SetStatus 迁移版本状态；candidate→reviewed→stable 链上 stable 必须签名。
func (s *Service) SetStatus(skillName, version, newStatus string) (*store.SkillVersionRow, error) {
	v, err := s.st.GetVersion(skillName, version)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrNotFound
	}
	allowed := transitions[v.Status]
	ok := false
	for _, a := range allowed {
		if a == newStatus {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s → %s", ErrBadTransition, v.Status, newStatus)
	}
	sig := ""
	if newStatus == "stable" {
		// stable 闸门：管理员人工放行后才签名（进化产物不得自动 stable，D9）
		sig, err = skillpkg.Sign(s.privHex, v.ContentSHA256)
		if err != nil {
			return nil, fmt.Errorf("sign: %w", err)
		}
	}
	if err := s.st.UpdateVersionStatus(skillName, version, newStatus, sig); err != nil {
		return nil, err
	}
	s.log.Info("version status", "skill", skillName, "version", version, "status", newStatus)
	return s.st.GetVersion(skillName, version)
}

// ---- 安装下发（§7.3）----

// CreateInstallCommands 对目标设备（显式 id 或按 label 圈定）下发 skills.install。
// 仅 stable 且已签名版本可下发（服务端强制）；evaluationRunID 非空 = 评估用途的受控例外
// （此时为版本签发评估签名，指令与安装记录都留痕 run id）。
func (s *Service) CreateInstallCommands(ctx context.Context, skillName, version, targetRoot, onConflict, createdBy string,
	deviceIDs, labels []string, evaluationRunID string) ([]*store.Command, error) {
	v, err := s.st.GetVersion(skillName, version)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrNotFound
	}
	signature := v.Signature
	if v.Status != "stable" {
		if evaluationRunID == "" {
			return nil, ErrNotStable
		}
		// 评估受控例外：candidate 评估签名（不改变 status，普通 install 仍拒绝）
		sig, err := skillpkg.Sign(s.privHex, v.ContentSHA256)
		if err != nil {
			return nil, err
		}
		signature = sig
	}
	if signature == "" {
		return nil, ErrNotStable
	}
	targets, err := s.resolveDevices(deviceIDs, labels)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no matching devices")
	}
	var cmds []*store.Command
	for _, dev := range targets {
		url, err := s.TicketURL(ctx, dev.ID, v.ContentSHA256)
		if err != nil {
			return nil, err
		}
		// target_root 语义 = skill 自身目录（§6.5 manifest.root）；管理面传白名单根，此处拼接 skill 名
		payload, _ := json.Marshal(proto.InstallPayload{
			Name: skillName, Version: version, BundleURL: url,
			SHA256: v.ContentSHA256, Signature: signature,
			TargetRoot: filepath.Join(targetRoot, skillName), OnConflict: onConflict,
		})
		var pm map[string]any
		_ = json.Unmarshal(payload, &pm)
		cmd, err := s.CmdIssuer(ctx, dev.ID, proto.CmdSkillsInstall, pm, createdBy)
		if err != nil {
			return cmds, err
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

// OnInstallResult 安装结果回传：落 DeviceSkillInstall（§8）。
func (s *Service) OnInstallResult(deviceID string, res *proto.CmdResult) {
	raw, _ := json.Marshal(res.Data)
	var ir proto.InstallResult
	if err := json.Unmarshal(raw, &ir); err != nil || ir.Name == "" || ir.Installed == "" {
		return
	}
	v, err := s.st.GetVersion(ir.Name, ir.Installed)
	if err != nil || v == nil {
		return
	}
	result := "ok"
	if ir.Result == "conflict_skipped" {
		result = "conflict_skipped"
	}
	_ = s.st.RecordInstall(deviceID, v, result, ir.Replaced, ir.EvaluationRunID)
}

// ResolveDevices 导出：client-update rollout 等复用设备圈定。
func (s *Service) ResolveDevices(deviceIDs, labels []string) ([]*store.Device, error) {
	return s.resolveDevices(deviceIDs, labels)
}

// resolveDevices 圈定下发目标：显式 id 优先，否则按 label 匹配。
func (s *Service) resolveDevices(deviceIDs, labels []string) ([]*store.Device, error) {
	if len(deviceIDs) > 0 {
		var out []*store.Device
		for _, id := range deviceIDs {
			d, err := s.st.GetDevice(id)
			if err != nil {
				return nil, err
			}
			if d != nil && d.Status != "revoked" {
				out = append(out, d)
			}
		}
		return out, nil
	}
	all, err := s.st.ListDevices()
	if err != nil {
		return nil, err
	}
	var out []*store.Device
	for _, d := range all {
		if d.Status == "revoked" {
			continue
		}
		if len(labels) == 0 {
			out = append(out, d)
			continue
		}
		for _, want := range labels {
			for _, has := range d.Labels {
				if want == has {
					out = append(out, d)
					break
				}
			}
		}
	}
	return out, nil
}

func trimHashPrefix(h string) string {
	if len(h) > 7 && h[:7] == "sha256:" {
		return h[7:]
	}
	return h
}

func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
