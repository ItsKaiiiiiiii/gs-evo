// Package proto 定义 client 与 cloud 之间的全部线上消息（JSON 文本帧）。
// 本包是协议的单一事实来源，两侧只 import 这里，不各自定义消息结构。
package proto

import "time"

// ProtoVersion 当前协议版本；hello 不匹配时云端拒绝并提示最低支持版本。
const ProtoVersion = 1

// 内嵌 bundle 的体积阈值：< 该值直接 base64 放 cmd_result，否则走一次性票据 URL。
const InlineBundleLimit = 256 << 10 // 256KB

// 单帧 task.event 的体积上限（P2 生效，先行声明）。
const MaxEventFrameBytes = 64 << 10

// ---- token 前缀（见 DESIGN.md §6.7）----

const (
	TokenPrefixDevice  = "gsd_" // device token：client 全部云端通信
	TokenPrefixEnroll  = "gse_" // enroll token：一次性注册
	TokenPrefixAdmin   = "gsa_" // admin token：cloud ctl / 指令创建者
	tokenRandomHexSize = 40     // 20 字节 crypto/rand
)

// ---- 消息 type 常量 ----

const (
	TypeHello     = "hello"      // c→cloud 连接后首条（鉴权已在 WS upgrade 的 Bearer 头完成）
	TypeHelloOK   = "hello_ok"   // cloud→c 对账应答：补发未 ack 指令
	TypePing      = "ping"       // c→cloud 应用层心跳兜底
	TypePong      = "pong"       // cloud→c
	TypeCmd       = "cmd"        // cloud→c 统一指令
	TypeCmdResult = "cmd_result" // c→cloud 指令结果（兼做 cmd_seq ack）
	TypeKick      = "kick"       // cloud→c 吊销踢线（关闭前发送，尽力而为）
)

// ---- 指令类型（封闭枚举，见 DESIGN.md §6.3）----

const (
	CmdAgentProbe       = "agent.probe"
	CmdSkillsScan       = "skills.scan"
	CmdSkillsCollect    = "skills.collect"
	CmdSkillsInstall    = "skills.install"
	CmdTaskStart        = "task.start"         // P2
	CmdTaskCancel       = "task.cancel"        // P2
	CmdTaskEventsReplay = "task.events_replay" // P2
	CmdClientUpdate     = "client.update"      // P4 预留
)

// SupportedCommands 客户端声明的能力集。
var SupportedCommands = []string{
	CmdAgentProbe, CmdSkillsScan, CmdSkillsCollect, CmdSkillsInstall,
	CmdTaskStart, CmdTaskCancel, CmdTaskEventsReplay, CmdRuntimeRegister, CmdRuntimeRemove, CmdRuntimeRemove,
}

// ---- 基础档案 ----

// DeviceInfo 设备静态摘要（hello 上报，云端 upsert 设备档案）。
type DeviceInfo struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	WorkerVersion string `json:"worker_version"`
}

// AgentInfo 本机探测到的 coding agent。
type AgentInfo struct {
	Type    string `json:"type"` // claude-code / codex / ...
	Version string `json:"version"`
	Path    string `json:"path"`
	OK      bool   `json:"ok"`
}

// Manifest skill 能力描述（§6.5），client 解析生成、cloud 原样入库。
type Manifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Root         string   `json:"root"`
	Entry        string   `json:"entry"`
	Summary      string   `json:"summary"`
	Capabilities []string `json:"capabilities"`
	ContentHash  string   `json:"content_hash"`
	Files        int      `json:"files"`
	LocalState   string   `json:"local_state"`
	UpdatedAt    string   `json:"updated_at"`
}

// SkillBrief hello/scan 上报的 skill 摘要（完整列表，不用摘要比对，见 C9）。
type SkillBrief struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"`
	LocalState  string `json:"local_state"` // clean | modified | new
}

// ---- hello / hello_ok ----

type Hello struct {
	Type            string        `json:"type"`
	ProtoVersion    int           `json:"proto_version"`
	Device          DeviceInfo    `json:"device"`
	Agents          []AgentInfo   `json:"agents"`   // 兼容字段（旧云端）；新云端读 Runtimes
	Runtimes        []RuntimeInfo `json:"runtimes"` // 运行时一等实体（D14）
	Roots           []string      `json:"roots"`    // skill 白名单根目录（安装目标下拉数据源）
	Skills          []SkillBrief  `json:"skills"`
	LastAckedCmdSeq int64         `json:"last_acked_cmd_seq"`
	Capabilities    []string      `json:"capabilities"`
}

// HelloOK 云端对账应答；PendingCommands 为全部未 ack 且未过期的指令（重连补发）。
type HelloOK struct {
	Type            string    `json:"type"`
	ServerTime      time.Time `json:"server_time"`
	Policies        Policies  `json:"policies"`
	PendingCommands []*Cmd    `json:"pending_commands"`
}

// Policies 云端下发的收紧类策略（只能收紧不能扩大，见 D10；当前仅有并发上限）。
type Policies struct {
	MaxConcurrency int `json:"max_concurrency,omitempty"`
}

// ---- 心跳 ----

type Ping struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
}

type Pong struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
}

// Kick 吊销踢线：发送后服务端立即关闭连接。
type Kick struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// ---- 统一指令模型 ----

// Cmd 云→端唯一指令形状。
type Cmd struct {
	Type      string         `json:"type"`
	CmdID     string         `json:"cmd_id"`
	CmdSeq    int64          `json:"cmd_seq"`
	CmdType   string         `json:"cmd_type"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Payload   map[string]any `json:"payload"`
}

// CmdResult 端→cloud 指令结果；同时充当对该 cmd_seq 的 ack。
type CmdResult struct {
	Type   string         `json:"type"`
	CmdID  string         `json:"cmd_id"`
	CmdSeq int64          `json:"cmd_seq"`
	OK     bool           `json:"ok"`
	Data   map[string]any `json:"data,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// ---- 指令 payload / result 结构（弱类型 map 的键常量与解析辅助）----

// agent.probe
type ProbeResult struct {
	Agents []AgentInfo `json:"agents"`
}

// skills.scan
type ScanPayload struct {
	Roots []string `json:"roots,omitempty"` // 只能是本地白名单的子集
}
type ScanResult struct {
	Manifests []Manifest `json:"manifests"`
}

// skills.collect
type CollectPayload struct {
	Name        string `json:"name"`
	Root        string `json:"root"`
	ContentHash string `json:"content_hash,omitempty"`
}
type CollectResult struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"`
	Size        int64  `json:"size"`
	Uploaded    bool   `json:"uploaded"`             // true=已走票据 URL 上传
	BundleB64   string `json:"bundle_b64,omitempty"` // <256KB 时内嵌
}

// skills.install
type InstallPayload struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	BundleURL  string `json:"bundle_url"`
	SHA256     string `json:"sha256"`
	Signature  string `json:"signature"` // ed25519 hex，对 "gs-evo-bundle-v1:"+sha256 签名
	TargetRoot string `json:"target_root"`
	OnConflict string `json:"on_conflict,omitempty"` // skip(默认) | overwrite

	// 评估灰度安装留痕（client 原样回传；cloud 侧受控例外，普通 install 为空）
	EvaluationRunID string `json:"evaluation_run_id,omitempty"`
}
type InstallResult struct {
	Name            string `json:"name"`
	Result          string `json:"result"` // installed | conflict_skipped
	Installed       string `json:"installed,omitempty"`
	Replaced        string `json:"replaced,omitempty"` // 上一版本号，无则 "none"
	EvaluationRunID string `json:"evaluation_run_id,omitempty"`
}

// bundle 一次性票据（REST，见 §6.6/§6.7）
type TicketRequest struct {
	SHA256 string `json:"sha256"`
	Action string `json:"action"` // upload | download
}
type TicketResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"` // 秒
}
