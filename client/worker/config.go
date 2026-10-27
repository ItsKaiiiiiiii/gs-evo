// client：gs-agent-worker 的配置与本地状态（§10 本地布局）。
package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

const WorkerVersion = "0.1.0"

// Config config.toml（白名单主权在本地，D10；云端 policies 只能收紧）。
type Config struct {
	ServerURL string   `toml:"server_url"`
	Token     string   `json:"-" toml:"token"`
	Roots     []string `toml:"roots" ` // skill 目录白名单

	MaxConcurrency int    `toml:"max_concurrency"`
	SigningPubkey  string `toml:"signing_pubkey"` // ed25519 hex（装 stable 包必填，D8）
	AgentPath      string `toml:"agent_path"`     // agent 二进制显式路径（测试/非 PATH 安装）

	PermissionMode string `toml:"permission_mode"` // P2 生效，预置 readonly
	RequireConfirm bool   `toml:"require_confirm"` // P2 生效，预置 false
}

// InstalledRec state.json 的安装记录（冲突检测依据，§6.4）。
type InstalledRec struct {
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"` // 纯 hex（无 sha256: 前缀）
	InstalledAt string `json:"installed_at"`
}

// ResultRec 幂等缓存条目（§5.2：重复指令直接回缓存结果）。
type ResultRec struct {
	CmdSeq int64          `json:"cmd_seq"`
	OK     bool           `json:"ok"`
	Data   map[string]any `json:"data,omitempty"`
	Error  string         `json:"error,omitempty"`
	At     string         `json:"at"`
}

// State state.json。
type State struct {
	Installed map[string]InstalledRec `json:"installed"`
	Results   map[string]ResultRec    `json:"results"` // cmd_id → 结果（上限 128）
	MaxSeq    int64                   `json:"max_seq"`
}

const maxCachedResults = 128

// Home 封装 ~/.gs-agent-worker（测试用 -home 覆盖）。
type Home struct {
	Dir string
}

func DefaultHome() Home {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return Home{Dir: filepath.Join(home, ".gs-agent-worker")}
}

func (h Home) ConfigPath() string   { return filepath.Join(h.Dir, "config.toml") }
func (h Home) StatePath() string    { return filepath.Join(h.Dir, "state.json") }
func (h Home) SpoolDir() string     { return filepath.Join(h.Dir, "spool") }
func (h Home) CacheDir() string     { return filepath.Join(h.Dir, "cache", "bundles") }
func (h Home) TasksDir() string     { return filepath.Join(h.Dir, "tasks") }
func (h Home) DeviceIDPath() string { return filepath.Join(h.Dir, "device.id") }
func (h Home) LogPath() string      { return filepath.Join(h.Dir, "worker.log") }

// LoadConfig 读 config.toml；不存在返回 errNoConfig。
var errNoConfig = errors.New("config not found (run `worker ctl login` first)")

func (h Home) LoadConfig() (*Config, error) {
	raw, err := os.ReadFile(h.ConfigPath())
	if err != nil {
		return nil, errNoConfig
	}
	var c Config
	if _, err := toml.Decode(string(raw), &c); err != nil {
		return nil, err
	}
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 2
	}
	if c.PermissionMode == "" {
		c.PermissionMode = "readonly"
	}
	return &c, nil
}

// SaveConfig 0600 + 临时文件原子写（§6.7 enroll 第 4 步）。
func (h Home) SaveConfig(c *Config) error {
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(c); err != nil {
		return err
	}
	return atomicWrite(h.ConfigPath(), []byte(buf.String()), 0o600)
}

func (h Home) LoadState() *State {
	st := &State{Installed: map[string]InstalledRec{}, Results: map[string]ResultRec{}}
	b, err := os.ReadFile(h.StatePath())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, st)
	if st.Installed == nil {
		st.Installed = map[string]InstalledRec{}
	}
	if st.Results == nil {
		st.Results = map[string]ResultRec{}
	}
	return st
}

func (h Home) SaveState(st *State) {
	// 结果缓存按上限裁剪：超限时淘汰最旧（按 At 排序）
	if len(st.Results) > maxCachedResults {
		type kv struct {
			k string
			r ResultRec
		}
		var all []kv
		for k, v := range st.Results {
			all = append(all, kv{k, v})
		}
		for i := 0; i < len(all)-1; i++ {
			for j := i + 1; j < len(all); j++ {
				if all[j].r.At < all[i].r.At {
					all[i], all[j] = all[j], all[i]
				}
			}
		}
		for _, x := range all[:len(all)-maxCachedResults] {
			delete(st.Results, x.k)
		}
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = atomicWrite(h.StatePath(), b, 0o600)
}

// DeviceID 本机持久身份（UUIDv7，与 token 分离存放，§6.7）。
func (h Home) DeviceID() (string, error) {
	if b, err := os.ReadFile(h.DeviceIDPath()); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b)), nil
	}
	id := proto.NewDeviceID()
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return "", err
	}
	if err := atomicWrite(h.DeviceIDPath(), []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
