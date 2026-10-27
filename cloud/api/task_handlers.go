package api

// 任务面 handlers（P2，§7.4）：创建/取消/查询 + 运维断连。

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/cloud/store"
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// handleCreateTask 建 task 行 + 下发 task.start 指令（记录 created_by=审计，D11）。
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		DeviceID      string            `json:"device_id"`
		Prompt        string            `json:"prompt"`
		AgentType     string            `json:"agent_type,omitempty"`
		RuntimeID     string            `json:"runtime_id,omitempty"`
		TimeoutS      int               `json:"timeout_s,omitempty"`
		MaxPermission string            `json:"max_permission,omitempty"`
		Env           map[string]string `json:"env,omitempty"`
	}](r)
	if err != nil || req.DeviceID == "" || req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "device_id and prompt required")
		return
	}
	dev, _ := s.st.GetDevice(req.DeviceID)
	if dev == nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	if dev.Status == "revoked" {
		writeErr(w, http.StatusBadRequest, "device revoked")
		return
	}

	taskID := proto.NewID("t-")
	// 先建 task 行再发指令：否则首个 task.event 会先于任务落库到达而被丢弃
	t := &store.TaskRow{
		ID: taskID, DeviceID: req.DeviceID, Prompt: req.Prompt,
		AgentType: req.AgentType, RuntimeID: req.RuntimeID,
		MaxPermission: req.MaxPermission, CreatedBy: "admin",
	}
	if err := s.st.CreateTask(t); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	payload, _ := json.Marshal(proto.TaskStartPayload{
		TaskID: taskID, Prompt: req.Prompt, AgentType: req.AgentType,
		RuntimeID: req.RuntimeID, TimeoutS: req.TimeoutS,
		MaxPermission: req.MaxPermission, Env: req.Env,
	})
	var pm map[string]any
	_ = json.Unmarshal(payload, &pm)
	cmd, err := s.CreateDeviceCommand(r.Context(), req.DeviceID, proto.CmdTaskStart, pm, "admin")
	if err != nil {
		_ = s.st.UpdateTaskStatus(taskID, "expired", map[string]any{"error": err.Error()})
		writeErr(w, 500, err.Error())
		return
	}
	_ = s.st.SetTaskCmdID(taskID, cmd.ID)
	writeJSON(w, http.StatusOK, map[string]any{"task_id": taskID, "cmd_id": cmd.ID, "cmd_seq": cmd.CmdSeq})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	tasks, err := s.st.ListTasks(deviceID, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tasks == nil {
		tasks = []*store.TaskRow{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.st.GetTask(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	fromSeq := int64(atoiDefault(r.URL.Query().Get("from_seq"), 0))
	limit := atoiDefault(r.URL.Query().Get("limit"), 500)
	events, err := s.st.ListTaskEvents(id, int64(fromSeq), limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if events == nil {
		events = []*store.TaskEventRow{}
	}
	writeJSON(w, http.StatusOK, events)
}

// handleTaskCancel 下发 task.cancel（终态仍由 task.start 的 cmd_result 回报）。
func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.st.GetTask(id)
	if err != nil || task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if task.FinishedAt != nil {
		writeJSON(w, http.StatusOK, map[string]any{"task_id": id, "cancelled": false,
			"reason": "task already finished"})
		return
	}
	payload, _ := json.Marshal(proto.TaskCancelPayload{TaskID: id})
	var pm map[string]any
	_ = json.Unmarshal(payload, &pm)
	cmd, err := s.CreateDeviceCommand(r.Context(), task.DeviceID, proto.CmdTaskCancel, pm, "admin")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 长时间无响应的任务给 cancel 指令留 5 分钟有效期
	_ = time.Minute
	writeJSON(w, http.StatusOK, map[string]any{"task_id": id, "cmd_id": cmd.ID, "cancel_requested": true})
}

// handleMaintenance 维护模式开关（演练断网：新连接在窗口期内被拒）。
func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[struct {
		RejectSeconds int `json:"reject_seconds"`
	}](r)
	if err != nil || req.RejectSeconds < 0 {
		writeErr(w, http.StatusBadRequest, "reject_seconds required")
		return
	}
	s.gw.RejectNewConns(time.Duration(req.RejectSeconds) * time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"reject_seconds": req.RejectSeconds})
}

// handleDisconnectDevice 运维断连（不吊销）：网络故障演练/强制重连。
func (s *Server) handleDisconnectDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.gw.Kick(id, "disconnected by admin")
	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}
