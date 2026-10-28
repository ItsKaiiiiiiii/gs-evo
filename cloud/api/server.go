// Package api 装配全部 HTTP 端点：设备注册/续期、WS 升级鉴权、bundle 票据、管理 API（§5/§6.7）。
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/auth"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/console"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/evo"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/gateway"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/skillsvc"
	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/skillpkg"
)

const (
	tokenTTL          = 90 * 24 * time.Hour // device token 有效期
	renewThreshold    = 7 * 24 * time.Hour  // 剩余 <7 天触发续期
	enrollTokenTTL    = 24 * time.Hour
	ticketTTL         = 10 * time.Minute
	defaultCmdTTL     = 10 * time.Minute
	maxBundleInMemory = 1 << 30 // 1GB 上限
)

type Config struct {
	Addr     string // 监听地址
	DataDir  string // bundle 存储 / 签名密钥
	AdminEnv string // 环境变量名：启动时注入的 admin token（测试/引导用）
}

type Server struct {
	cfg          Config
	st           *store.Store
	verifier     *auth.Verifier
	gw           *gateway.Gateway
	svc          *skillsvc.Service
	evo          *evo.Engine
	log          *slog.Logger
	privHex      string
	pubHex       string
	enrollSecret string // 装机密钥（GSEVO_ENROLL_SECRET；空=开放注册）
	upgrader     websocket.Upgrader
	httpCli      *http.Client
}

func NewServer(cfg Config, st *store.Store, log *slog.Logger) (*Server, error) {
	s := &Server{
		cfg: cfg, st: st, log: log,
		enrollSecret: os.Getenv("GSEVO_ENROLL_SECRET"),
		verifier:     auth.NewVerifier(st),
		upgrader:     websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096},
		httpCli:      &http.Client{Timeout: 30 * time.Second},
	}
	// ed25519 签名密钥：首次启动生成，落 data_dir/keys（§6.6）
	keysDir := filepath.Join(cfg.DataDir, "keys")
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		return nil, err
	}
	privPath := filepath.Join(keysDir, "ed25519.key")
	pubPath := filepath.Join(keysDir, "ed25519.pub")
	if b, err := os.ReadFile(privPath); err == nil {
		s.privHex = strings.TrimSpace(string(b))
	} else {
		pub, priv, err := skillpkg.GenerateKeyPair()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(privPath, []byte(priv+"\n"), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(pubPath, []byte(pub+"\n"), 0o644); err != nil {
			return nil, err
		}
		s.privHex, s.pubHex = priv, pub
		log.Info("generated ed25519 signing key", "pub", s.PubKeyHex())
	}
	if s.pubHex == "" {
		b, err := os.ReadFile(pubPath)
		if err != nil {
			return nil, err
		}
		s.pubHex = strings.TrimSpace(string(b))
	}

	bundleDir := filepath.Join(cfg.DataDir, "bundles")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return nil, err
	}
	s.svc = skillsvc.New(st, log, bundleDir, s.privHex)
	s.svc.CmdIssuer = s.CreateDeviceCommand
	s.svc.TicketURL = s.deviceTicketDownloadURL

	s.gw = gateway.New(st, log)
	s.gw.OnResult = s.onCmdResult
	s.gw.OnTaskEvent = s.onTaskEvent
	s.gw.LoadPend = s.loadPending
	s.setupEvolution()
	return s, nil
}

func (s *Server) Gateway() *gateway.Gateway { return s.gw }

func (s *Server) PubKeyHex() string { return s.pubHex }

// Routes 注册全部路由。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// 设备面
	mux.HandleFunc("POST /api/v1/devices/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/devices/token/renew", s.authDevice(s.handleRenew))
	mux.HandleFunc("GET /api/v1/agent-ws", s.handleAgentWS)
	mux.HandleFunc("POST /api/v1/bundles/tickets", s.authDevice(s.handleTicket))
	mux.HandleFunc("PUT /api/v1/bundles/{sha256}", s.handleBundlePut)
	mux.HandleFunc("GET /api/v1/bundles/{sha256}", s.handleBundleGet)

	// Web 控制台（静态资源，数据操作走 Bearer 鉴权的 API）
	mux.HandleFunc("GET /{$}", console.Redirect)
	mux.HandleFunc("GET /console", console.Redirect)
	mux.Handle("GET /console/", console.Handler())

	// 管理面（gsa_ Bearer）
	mux.HandleFunc("POST /api/v1/admin/enroll-tokens", s.authAdmin(s.handleCreateEnroll))
	mux.HandleFunc("GET /api/v1/admin/devices", s.authAny(s.handleListDevices))
	mux.HandleFunc("POST /api/v1/admin/devices/{id}/revoke", s.authAdmin(s.handleRevokeDevice))
	mux.HandleFunc("POST /api/v1/admin/commands", s.authAdmin(s.handleCreateCommand))
	mux.HandleFunc("GET /api/v1/admin/commands", s.authAny(s.handleListCommands))
	mux.HandleFunc("GET /api/v1/admin/skills", s.authAny(s.handleListSkills))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}/versions", s.authAny(s.handleListVersions))
	mux.HandleFunc("POST /api/v1/admin/skills/{name}/versions/{version}/status", s.authAdmin(s.handleSetStatus))
	mux.HandleFunc("POST /api/v1/admin/skills/{name}/install", s.authAdmin(s.handleInstall))
	mux.HandleFunc("GET /api/v1/admin/key", s.authAdmin(s.handleKey))

	// 进化面（P3）
	mux.HandleFunc("POST /api/v1/admin/benchmarks", s.authAdmin(s.handleCreateBenchmark))
	mux.HandleFunc("GET /api/v1/admin/benchmarks", s.authAny(s.handleListBenchmarks))
	mux.HandleFunc("GET /api/v1/admin/benchmarks/{id}", s.authAny(s.handleGetBenchmark))
	mux.HandleFunc("POST /api/v1/admin/skills/{name}/evolve", s.authAdmin(s.handleEvolve))
	mux.HandleFunc("POST /api/v1/admin/skills/{name}/evaluate", s.authAdmin(s.handleEvaluate))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}/versions/{version}/runs", s.authAny(s.handleListRuns))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}/versions/{version}/diff", s.authAny(s.handleVersionDiff))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}/auto-policy", s.authAny(s.handleGetAutoPolicy))
	mux.HandleFunc("POST /api/v1/admin/skills/{name}/auto-policy", s.authAdmin(s.handleAutoPolicy))

	// 运营面（P4）
	mux.HandleFunc("POST /api/v1/admin/client-releases/{version}", s.authAdmin(s.handlePushClientRelease))
	mux.HandleFunc("POST /api/v1/admin/client-update/rollout", s.authAdmin(s.handleClientUpdateRollout))
	mux.HandleFunc("GET /api/v1/admin/report/summary", s.authAny(s.handleReport))
	mux.HandleFunc("POST /api/v1/admin/tokens", s.authAdmin(s.handleCreateAdminToken))
	mux.HandleFunc("POST /api/v1/admin/tokens/revoke", s.authAdmin(s.handleRevokeAdminToken))
	mux.HandleFunc("POST /api/v1/admin/maintenance", s.authAdmin(s.handleMaintenance))

	// 任务面（P2）
	mux.HandleFunc("POST /api/v1/admin/tasks", s.authAdmin(s.handleCreateTask))
	mux.HandleFunc("GET /api/v1/admin/tasks", s.authAny(s.handleListTasks))
	mux.HandleFunc("GET /api/v1/admin/tasks/{id}/events", s.authAdmin(s.handleTaskEvents))
	mux.HandleFunc("POST /api/v1/admin/tasks/{id}/cancel", s.authAdmin(s.handleTaskCancel))
	mux.HandleFunc("POST /api/v1/admin/devices/{id}/disconnect", s.authAdmin(s.handleDisconnectDevice))
	mux.HandleFunc("POST /api/v1/admin/devices/{id}/labels", s.authAdmin(s.handleSetDeviceLabels))
	mux.HandleFunc("GET /api/v1/admin/devices/{id}/installs", s.authAny(s.handleDeviceInstalls))
	mux.HandleFunc("GET /api/v1/admin/devices/{id}/runtimes", s.authAny(s.handleDeviceRuntimes))
	mux.HandleFunc("GET /api/v1/admin/runtimes", s.authAny(s.handleListRuntimes))
	mux.HandleFunc("GET /api/v1/admin/runtime-types", s.authAny(s.handleRuntimeTypes))
	mux.HandleFunc("GET /api/v1/admin/whoami", s.authAny(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"role": s.verifier.VerifyAdminRole(r.Context(), r.Header.Get("Authorization"))})
	}))
	mux.HandleFunc("GET /api/v1/admin/skills/{name}/coverage", s.authAny(s.handleSkillCoverage))
	mux.HandleFunc("GET /api/v1/admin/evaluations", s.authAny(s.handleRecentEvaluations))
	mux.HandleFunc("GET /api/v1/admin/tokens", s.authAny(s.handleListTokens))
	return mux
}

// ---- 中间件 ----

type deviceCtxKey struct{}

func (s *Server) authDevice(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := s.verifier.VerifyDevice(r.Context(), r.Header.Get("Authorization"))
		if d == nil {
			writeErr(w, http.StatusUnauthorized, "invalid device token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), deviceCtxKey{}, d)))
	}
}

// authAny admin 或 viewer（只读端点用；写操作仍走 authAdmin）。
func (s *Server) authAny(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.verifier.VerifyAdminRole(r.Context(), r.Header.Get("Authorization")) == "" {
			writeErr(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		next(w, r)
	}
}

func (s *Server) authAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := s.verifier.VerifyAdminRole(r.Context(), r.Header.Get("Authorization"))
		if role == "" {
			writeErr(w, http.StatusUnauthorized, "invalid admin token")
			return
		}
		if role != "admin" {
			writeErr(w, http.StatusForbidden,
				"insufficient role: this operation requires an admin token (current role: viewer)")
			return
		}
		next(w, r)
	}
}

func deviceOf(r *http.Request) *store.Device {
	return r.Context().Value(deviceCtxKey{}).(*store.Device)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readBody[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// ---- 设备面 ----

// handleEnroll 设备注册：安装时配好云端地址，首次连接云端自动颁发 device token（§6.7，multica 接入思路）。
// 准入模式（GSEVO_ENROLL_SECRET）：
//
//	未设置 → 开放注册（内网/测试场景）；
//	设置后   → 请求必须携带匹配的 install_secret（装机时预置），否则 401；
//
// 一次性 enroll token 流程保留兼容（管理员显式分发场景，label 落设备标签）。
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		EnrollToken   string `json:"enroll_token,omitempty"`   // 一次性 token（兼容流程）
		InstallSecret string `json:"install_secret,omitempty"` // 装机密钥（自动颁发准入）
		DeviceID      string `json:"device_id"`
		Hostname      string `json:"hostname"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		WorkerVersion string `json:"worker_version"`
	}](r)
	if err != nil || req.DeviceID == "" || req.Hostname == "" {
		writeErr(w, http.StatusBadRequest, "device_id and hostname required")
		return
	}

	var owner string
	deviceLabels := []string{}
	token := proto.NewDeviceToken()

	if req.EnrollToken != "" && strings.HasPrefix(req.EnrollToken, proto.TokenPrefixEnroll) {
		// 一次性 enroll token 流（管理员显式分发；label 落设备标签）
		hash := proto.HashToken(req.EnrollToken)
		tx, err := s.st.DB.Begin()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer tx.Rollback()
		et, err := s.st.ConsumeEnrollToken(tx, hash)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if et == nil {
			writeErr(w, http.StatusUnauthorized, "invalid or used enroll token")
			return
		}
		owner = et.CreatedBy
		if et.Label != "" {
			deviceLabels = append(deviceLabels, et.Label)
		}
		if err := tx.Commit(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	} else if s.enrollSecret != "" {
		// 自动颁发 + 准入密钥
		if req.InstallSecret != s.enrollSecret {
			writeErr(w, http.StatusUnauthorized,
				"install secret required（云端已开启准入：装机时需配置与云端一致的安装密钥）")
			return
		}
		owner = "auto"
	} else {
		// 开放注册
		owner = "auto"
	}

	d := &store.Device{
		ID: req.DeviceID, Owner: owner,
		TokenHash: proto.HashToken(token), TokenExpiresAt: time.Now().Add(tokenTTL),
		Hostname: req.Hostname, OS: req.OS, Arch: req.Arch, WorkerVersion: req.WorkerVersion,
		Labels: deviceLabels,
	}
	isNew, err := s.st.UpsertDeviceOnEnrollAuto(d)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.log.Info("device enrolled", "device", req.DeviceID, "hostname", req.Hostname,
		"new", isNew, "mode", map[bool]string{true: "enroll-token", false: "auto"}[req.EnrollToken != ""])
	writeJSON(w, http.StatusOK, map[string]any{
		"device_token": token, // 明文仅此一次
		"device_id":    req.DeviceID,
		"expires_at":   d.TokenExpiresAt,
	})
}

// handleRenew 90 天原地续期（剩余 <7 天才实际延长，CAS 防并发，§6.7）。
func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	d := deviceOf(r)
	newExpiry := d.TokenExpiresAt.Add(tokenTTL)
	renewed := time.Until(d.TokenExpiresAt) < renewThreshold
	if renewed {
		res, err := s.st.DB.Exec(
			`UPDATE device SET token_expires_at=? WHERE id=? AND token_expires_at=?`,
			newExpiry, d.ID, d.TokenExpiresAt)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if n, _ := res.RowsAffected(); n == 0 { // 并发输家：读现值
			fresh, _ := s.st.GetDevice(d.ID)
			if fresh != nil {
				newExpiry = fresh.TokenExpiresAt
			}
			renewed = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"renewed": renewed, "expires_at": newExpiry})
}

// handleAgentWS WS 升级：Bearer 头在 upgrade 前完成鉴权（§6.7，未鉴权连接不存在）。
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	if s.gw.Rejecting() {
		writeErr(w, http.StatusServiceUnavailable, "server maintenance, retry later")
		return
	}
	d := s.verifier.VerifyDevice(r.Context(), r.Header.Get("Authorization"))
	if d == nil {
		// 401 而非 404：本端点不承载可枚举资源（设备枚举防护在多租户 P4 引入）
		writeErr(w, http.StatusUnauthorized, "invalid device token")
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.gw.ServeConn(d.ID, ws)
}

// ---- bundle 票据（§6.6：云端自签发一次性票据 URL）----

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	d := deviceOf(r)
	req, err := readBody[proto.TicketRequest](r)
	if err != nil || (req.Action != "upload" && req.Action != "download") || len(req.SHA256) != 64 {
		writeErr(w, http.StatusBadRequest, "need sha256(64hex) and action upload|download")
		return
	}
	if req.Action == "download" {
		if ok, _, err := s.st.BundleExists(req.SHA256); err != nil || !ok {
			writeErr(w, http.StatusNotFound, "bundle not found")
			return
		}
	}
	ticket := proto.NewToken("gst_")
	expires := time.Now().Add(ticketTTL)
	if err := s.st.CreateBundleTicket(proto.NewID("bt-"), proto.HashToken(ticket), d.ID, req.SHA256, req.Action, expires); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	writeJSON(w, http.StatusOK, proto.TicketResponse{
		URL:       base + "/api/v1/bundles/" + req.SHA256 + "?ticket=" + ticket,
		ExpiresIn: int(ticketTTL.Seconds()),
	})
}

func (s *Server) deviceTicketDownloadURL(ctx context.Context, deviceID, sha string) (string, error) {
	ticket := proto.NewToken("gst_")
	if err := s.st.CreateBundleTicket(proto.NewID("bt-"), proto.HashToken(ticket), deviceID, sha, "download", time.Now().Add(ticketTTL)); err != nil {
		return "", err
	}
	// 云端自身知道对外地址；此处读环境变量 GSEVO_PUBLIC_URL，
	// 缺省回退本机监听地址（loopback，测试够用）。
	base := os.Getenv("GSEVO_PUBLIC_URL")
	if base == "" {
		base = "http://127.0.0.1" + s.cfg.Addr // ":8080" 形态
		if !strings.HasPrefix(s.cfg.Addr, ":") {
			base = "http://" + s.cfg.Addr // "host:port" 形态
		}
	}
	return base + "/api/v1/bundles/" + sha + "?ticket=" + ticket, nil
}

func (s *Server) handleBundlePut(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	ticket := r.URL.Query().Get("ticket")
	if len(sha) != 64 || ticket == "" {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	deviceID, ok, err := s.st.ConsumeBundleTicket(proto.HashToken(ticket), sha, "upload")
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "invalid or used ticket")
		return
	}
	_ = deviceID
	if err := s.receiveBundle(sha, r.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stored"})
}

func (s *Server) handleBundleGet(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	ticket := r.URL.Query().Get("ticket")
	if len(sha) != 64 || ticket == "" {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if _, ok, err := s.st.ConsumeBundleTicket(proto.HashToken(ticket), sha, "download"); err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "invalid or used ticket")
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.DataDir, "bundles", sha[:2], sha+".tar.gz"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "bundle not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = io.Copy(w, f)
}

// receiveBundle 边收边算哈希，落盘前校验（§6.6 一律附 sha256）。
func (s *Server) receiveBundle(sha string, body io.Reader) error {
	h := sha256.New()
	tmp, err := os.CreateTemp(filepath.Join(s.cfg.DataDir, "bundles"), "upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	size, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, maxBundleInMemory))
	if err != nil {
		_ = tmp.Close()
		return err
	}
	_ = tmp.Close()
	if hex.EncodeToString(h.Sum(nil)) != sha {
		return errors.New("sha256 mismatch")
	}
	dest := filepath.Join(s.cfg.DataDir, "bundles", sha[:2], sha+".tar.gz")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	return s.svc.StoreUploadedBundle(sha, size)
}

// ---- 指令创建（管理面与 skillsvc 共用）----

// CreateDeviceCommand 分配 cmd_seq → 落库 → 在线即推（§6.2）。
func (s *Server) CreateDeviceCommand(ctx context.Context, deviceID, cmdType string, payload map[string]any, createdBy string) (*store.Command, error) {
	exp := time.Now().Add(defaultCmdTTL)
	c := &store.Command{
		ID: proto.NewID("c-"), DeviceID: deviceID, CmdType: cmdType,
		Payload: payload, CreatedBy: createdBy, ExpiresAt: &exp,
	}
	seq, err := s.st.CreateCommand(nil, c)
	if err != nil {
		return nil, err
	}
	c.CmdSeq = seq
	if s.gw.TryDeliver(deviceID, c.ToProto()) {
		_ = s.st.MarkSent(c.ID)
	}
	return c, nil
}

// onCmdResult 汇聚：collect → 入库；install → 记录安装覆盖（§7.2/§7.3）。
func (s *Server) onCmdResult(deviceID string, res *proto.CmdResult) {
	// res 缺 cmd_type；从落库行补齐
	cmd, err := s.st.GetCommand(res.CmdID)
	if err != nil || cmd == nil {
		return
	}
	switch cmd.CmdType {
	case proto.CmdSkillsCollect:
		ingest := "registered"
		if !res.OK {
			ingest = "" // worker 侧执行失败：不给 ingest 字段，失败信息已在 error
		} else if err := s.svc.IngestCollectResult(deviceID, res); err != nil {
			if strings.Contains(err.Error(), "already registered") {
				ingest = "unchanged" // 内容与已有版本一致：明确告知而非静默
			} else {
				ingest = "error: " + err.Error()
				s.log.Warn("ingest collect failed", "device", deviceID, "err", err)
			}
		}
		if ingest != "" {
			s.appendCommandResult(cmd.ID, map[string]any{"ingest": ingest})
		}
	case proto.CmdSkillsScan:
		// 富化扫描结果：云端已登记的 skill 打 registered 标（前端区分"新/已登记"）
		if res.OK && res.Data != nil {
			if ms, ok := res.Data["manifests"].([]any); ok {
				changed := false
				for _, mi := range ms {
					m, ok2 := mi.(map[string]any)
					if !ok2 {
						continue
					}
					name, _ := m["name"].(string)
					ver, _ := m["version"].(string)
					if name == "" || ver == "" {
						continue
					}
					if gv, err := s.st.GetVersion(name, ver); err == nil && gv != nil {
						m["registered"] = true
						m["cloud_status"] = gv.Status
						changed = true
					}
				}
				if changed {
					s.appendCommandResult(cmd.ID, map[string]any{"data": res.Data})
				}
			}
		}
	case proto.CmdSkillsInstall:
		s.svc.OnInstallResult(deviceID, res)
	case proto.CmdTaskStart:
		s.onTaskResult(deviceID, res)
	}
}

// onTaskResult task.start 终态 → task 表归态（§7.4）。
func (s *Server) onTaskResult(deviceID string, res *proto.CmdResult) {
	raw, _ := json.Marshal(res.Data)
	var tr proto.TaskResult
	_ = json.Unmarshal(raw, &tr)
	// 启动失败时 data 里没有 task_id：从指令 payload 取（NEW-1：否则任务永久僵尸 queued）
	if tr.TaskID == "" {
		cmd, err := s.st.GetCommand(res.CmdID)
		if err != nil || cmd == nil {
			return
		}
		if tid, _ := cmd.Payload["task_id"].(string); tid != "" {
			tr.TaskID = tid
		} else {
			return
		}
	}
	if tr.TaskID == "" {
		// 启动失败的 result 不带 task_id：从指令 payload 反查
		cmd, err := s.st.GetCommand(res.CmdID)
		if err != nil || cmd == nil {
			return
		}
		tr.TaskID, _ = cmd.Payload["task_id"].(string)
		if rt, _ := cmd.Payload["runtime_id"].(string); rt != "" {
			tr.RuntimeID = rt
		}
	}
	if tr.TaskID == "" {
		return
	}
	if tr.RuntimeID != "" {
		_ = s.st.SetTaskRuntime(tr.TaskID, tr.RuntimeID)
	}
	if !res.OK && tr.Status == "" {
		// 任务没能启动（spawn 失败等）：归 failed，错误浮出到任务结果
		_ = s.st.SetTaskResult(tr.TaskID, proto.TaskFailed,
			map[string]any{"error": res.Error})
		return
	}
	// cmd_result 是终态权威（status 事件先到时已写过 status，但 result 以此为准）
	_ = s.st.SetTaskResult(tr.TaskID, tr.Status, res.Data)
}

// onTaskEvent 事件落库（UNIQUE(task_id,seq) 幂等）+ status 迁移。
func (s *Server) onTaskEvent(deviceID string, ev *proto.TaskEvent) {
	task, err := s.st.GetTask(ev.TaskID)
	if err != nil || task == nil {
		return // 未知任务（或已清理）：丢弃
	}
	if task.DeviceID != deviceID {
		return // 归属校验：不接受其他设备的帧（§6.7 连接级 context）
	}
	if err := s.st.InsertTaskEvent(ev); err != nil {
		s.log.Warn("task event insert failed", "task", ev.TaskID, "seq", ev.Seq, "err", err)
	}
	if ev.Kind == proto.EventKindStatus {
		if st, _ := ev.Data["status"].(string); st != "" {
			_ = s.st.UpdateTaskStatus(ev.TaskID, st, nil)
		}
	}
}

// handleSetDeviceLabels 设置设备标签（rollout 圈定用）。
func (s *Server) handleSetDeviceLabels(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Labels []string `json:"labels"`
	}](r)
	if err != nil || req.Labels == nil {
		writeErr(w, http.StatusBadRequest, "labels required (array of strings)")
		return
	}
	if err := s.st.SetDeviceLabels(r.PathValue("id"), req.Labels); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	d, _ := s.st.GetDevice(r.PathValue("id"))
	if d == nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": d.ID, "labels": d.Labels})
}

// handleDeviceInstalls 设备详情：装机清单（评审问题 5）。
func (s *Server) handleDeviceInstalls(w http.ResponseWriter, r *http.Request) {
	installs, err := s.st.ListInstallsByDevice(r.PathValue("id"), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, installs)
}

// handleRuntimeTypes 受支持的运行时类型（创建对话框渲染用）。
func (s *Server) handleRuntimeTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"types": proto.RuntimeTypes()})
}

// handleListRuntimes 全局运行时列表（任务/操作页选择器，含设备主机名）。
func (s *Server) handleListRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.st.ListRuntimes()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rts)
}

// handleDeviceRuntimes 设备详情：运行时列表（D14）。
func (s *Server) handleDeviceRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.st.ListRuntimesByDevice(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rts)
}

// handleSkillCoverage skill 覆盖（评审问题 6）。
func (s *Server) handleSkillCoverage(w http.ResponseWriter, r *http.Request) {
	cov, err := s.st.CoverageBySkill(r.PathValue("name"), 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cov)
}

// handleRecentEvaluations 全局评估队列（进化页降级为全局队列）。
func (s *Server) handleRecentEvaluations(w http.ResponseWriter, r *http.Request) {
	runs, err := s.st.ListRecentEvaluationRuns(atoiDefault(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*store.EvaluationRunRow{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleListTokens token 清单（审计页；哈希不外露）。
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.st.ListAdminTokens()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) loadPending(deviceID string) ([]*store.Command, error) {
	return s.st.PendingCommands(deviceID)
}

// appendCommandResult 把云端侧处理结论并入指令结果（用户查 cmd list 时能看到"收没收到、变没变"）。
func (s *Server) appendCommandResult(cmdID string, extra map[string]any) {
	cmd, err := s.st.GetCommand(cmdID)
	if err != nil || cmd == nil || cmd.Result == nil {
		return
	}
	merged := map[string]any{}
	for k, v := range cmd.Result {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	b, _ := json.Marshal(merged)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, _ = s.st.DB.Exec(`UPDATE command SET result=? WHERE id=?`, mustJSON(m), cmdID)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---- 管理面 handlers ----

func (s *Server) handleCreateEnroll(w http.ResponseWriter, r *http.Request) {
	type enrollReq struct {
		Label string `json:"label"`
	}
	req, err := readBody[enrollReq](r)
	if err != nil {
		req = &enrollReq{}
	}
	token := proto.NewEnrollToken()
	if err := s.st.CreateEnrollToken(proto.HashToken(token), req.Label, "admin", time.Now().Add(enrollTokenTTL)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": time.Now().Add(enrollTokenTTL)})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.st.ListDevices()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type devView struct {
		ID            string   `json:"id"`
		Hostname      string   `json:"hostname"`
		OS            string   `json:"os"`
		Arch          string   `json:"arch"`
		WorkerVersion string   `json:"worker_version"`
		Status        string   `json:"status"`
		Paused        bool     `json:"paused"`
		LastSeen      string   `json:"last_seen,omitempty"`
		LastAcked     int64    `json:"last_acked_cmd_seq"`
		Labels        []string `json:"labels"`
	}
	out := make([]devView, 0, len(devs))
	for _, d := range devs {
		labels := d.Labels
		if labels == nil {
			labels = []string{}
		}
		v := devView{ID: d.ID, Hostname: d.Hostname, OS: d.OS, Arch: d.Arch,
			WorkerVersion: d.WorkerVersion, Status: d.Status, Paused: d.Paused,
			LastAcked: d.LastAckedCmdSeq, Labels: labels}
		if d.LastSeen != nil {
			v.LastSeen = d.LastSeen.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d, _ := s.st.GetDevice(id); d == nil {
		writeErr(w, http.StatusNotFound, "device not found: "+id)
		return
	}
	if err := s.verifier.RevokeDevice(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.gw.Kick(id, "token revoked") // 吊销即踢线（比 multica 更强，§6.7）
	_ = s.st.SetOffline(id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleCreateCommand 白名单原始指令（install 走专用端点强制 stable+签名）。
func (s *Server) handleCreateCommand(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		DeviceID   string         `json:"device_id"`
		CmdType    string         `json:"cmd_type"`
		Payload    map[string]any `json:"payload"`
		TTLSeconds int            `json:"ttl_seconds,omitempty"`
	}](r)
	if err != nil || req.DeviceID == "" || req.CmdType == "" {
		writeErr(w, http.StatusBadRequest, "device_id and cmd_type required")
		return
	}
	switch req.CmdType {
	case proto.CmdAgentProbe, proto.CmdSkillsScan, proto.CmdSkillsCollect, proto.CmdTaskEventsReplay, proto.CmdRuntimeRegister, proto.CmdRuntimeRemove:
	default:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"cmd_type %q not allowed here; allowed: %s, %s, %s, %s, %s（skill 安装请用 skill install，任务请用 task create）",
			req.CmdType, proto.CmdAgentProbe, proto.CmdSkillsScan, proto.CmdSkillsCollect,
			proto.CmdTaskEventsReplay, proto.CmdRuntimeRegister))
		return
	}
	if d, _ := s.st.GetDevice(req.DeviceID); d == nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultCmdTTL
	}
	// 直接构造以支持自定义 TTL
	exp := time.Now().Add(ttl)
	c := &store.Command{
		ID: proto.NewID("c-"), DeviceID: req.DeviceID, CmdType: req.CmdType,
		Payload: req.Payload, CreatedBy: "admin", ExpiresAt: &exp,
	}
	seq, err := s.st.CreateCommand(nil, c)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	c.CmdSeq = seq
	if s.gw.TryDeliver(req.DeviceID, c.ToProto()) {
		_ = s.st.MarkSent(c.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"cmd_id": c.ID, "cmd_seq": seq})
}

func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	cmdID := r.URL.Query().Get("cmd_id")
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	if cmdID != "" { // CLI 回执轮询路径：按 cmd_id 精确查
		c, err := s.st.GetCommand(cmdID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out := []*store.Command{}
		if c != nil {
			out = append(out, c)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	cmds, err := s.st.ListCommands(deviceID, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, c := range cmds {
		elideBundleB64(c.Payload)
		elideBundleB64(c.Result)
	}
	writeJSON(w, http.StatusOK, cmds)
}

// elideBundleB64 就地替换超大 base64 块为占位说明（P3-5：别淹没 status/result）。
func elideBundleB64(m map[string]any) {
	if m == nil {
		return
	}
	for k, v := range m {
		switch t := v.(type) {
		case string:
			if (k == "bundle_b64" || strings.HasSuffix(k, "_b64")) && len(t) > 128 {
				m[k] = fmt.Sprintf("<base64:%d bytes omitted>", len(t))
			}
		case map[string]any:
			elideBundleB64(t)
		}
	}
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := s.st.ListSkills()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if skills == nil {
		skills = []*store.SkillRow{}
	}
	writeJSON(w, http.StatusOK, skills)
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.st.ListVersions(r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if versions == nil {
		versions = []*store.SkillVersionRow{}
	}
	writeJSON(w, http.StatusOK, versions)
}

func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Status string `json:"status"`
	}](r)
	if err != nil || req.Status == "" {
		writeErr(w, http.StatusBadRequest, "status required")
		return
	}
	v, err := s.svc.SetStatus(r.PathValue("name"), r.PathValue("version"), req.Status)
	if errors.Is(err, skillsvc.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "version not found")
		return
	}
	if errors.Is(err, skillsvc.ErrBadTransition) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		Version    string   `json:"version"`
		TargetRoot string   `json:"target_root"`
		OnConflict string   `json:"on_conflict,omitempty"`
		DeviceIDs  []string `json:"device_ids,omitempty"`
		Labels     []string `json:"labels,omitempty"`
	}](r)
	if err != nil || req.Version == "" || req.TargetRoot == "" {
		writeErr(w, http.StatusBadRequest, "version and target_root required")
		return
	}
	cmds, err := s.svc.CreateInstallCommands(r.Context(), r.PathValue("name"), req.Version,
		req.TargetRoot, req.OnConflict, "admin", req.DeviceIDs, req.Labels, "")
	if errors.Is(err, skillsvc.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "skill or version not found")
		return
	}
	if errors.Is(err, skillsvc.ErrNotStable) || errors.Is(err, skillsvc.ErrBadTransition) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type issued struct {
		CmdID  string `json:"cmd_id"`
		Device string `json:"device_id"`
		CmdSeq int64  `json:"cmd_seq"`
	}
	out := make([]issued, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, issued{CmdID: c.ID, Device: c.DeviceID, CmdSeq: c.CmdSeq})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"signing_pubkey": s.pubHex})
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
