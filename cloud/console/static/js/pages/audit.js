// audit.js：审计页（新模块）——指令全量历史（操作者可见）+ token 管理。
// 评审裂伤三修复：指令是后端一等公民（cmd→cmd_result 全量落库 + created_by），
// 此前前端只有报表页的失败摘要；安全能力（RBAC）此前不可见。
(function () {
  const API_ = '/api/v1/admin';

  function statusBadge(s, result) {
    if (s === 'acked') {
      if (result && result.ok === false) return '<span class="badge red">失败</span>';
      return '<span class="badge green">成功</span>';
    }
    const map = { pending: 'gray', sent: 'blue', expired: 'yellow' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  async function render(c) {
    const devices = await API.get(`${API_}/devices`);
    c.innerHTML = `<div class="page-title">审计</div>
    <div class="card">
      <h3>指令历史（全量，最近 200 条）</h3>
      <div class="form-row">
        <label>设备</label>
        <select id="a-device"><option value="">全部</option>${devices.map(d => `<option value="${d.id}">${App.esc(d.hostname)}</option>`).join('')}</select>
        <label>类型</label>
        <select id="a-type"><option value="">全部</option>
          ${['agent.probe', 'skills.scan', 'skills.collect', 'skills.install', 'task.start', 'task.cancel', 'task.events_replay', 'client.update'].map(t => `<option>${t}</option>`).join('')}
        </select>
        <label>状态</label>
        <select id="a-status"><option value="">全部</option><option>pending</option><option>sent</option><option>acked</option><option>expired</option></select>
        <button class="small" id="a-refresh">查询</button>
      </div>
      <div id="a-table">加载中…</div>
    </div>
    <div class="card">
      <h3>管理 token（RBAC）</h3>
      <div id="tok-list"></div>
      ${App.canWrite() ? `
      <div class="form-row" style="margin-top:8px">
        <label>签发</label>
        <select id="tok-role"><option value="viewer">viewer（只读）</option><option value="admin">admin（全权）</option></select>
        <input id="tok-label" placeholder="label，如 ops-张三" style="width:200px">
        <button class="small primary" id="tok-create">签发</button>
      </div>
      <pre id="tok-out" class="hidden"></pre>` : '<span class="muted">viewer 只读</span>'}
    </div>`;

    async function refresh() {
      const dev = document.getElementById('a-device').value;
      const type = document.getElementById('a-type').value;
      const status = document.getElementById('a-status').value;
      let cmds = await API.get(`${API_}/commands?limit=200&device_id=${dev}`);
      if (type) cmds = cmds.filter(x => x.cmd_type === type);
      if (status) cmds = cmds.filter(x => x.status === status);
      const rows = (cmds || []).map(cmd => `<tr>
        <td>${App.esc((cmd.issued_at || '').replace('T', ' ').slice(0, 19))}</td>
        <td title="${App.esc(cmd.device_id)}">${App.esc(cmd.device_id.slice(0, 13))}…</td>
        <td>${App.esc(cmd.cmd_type)}</td>
        <td>${statusBadge(cmd.status, cmd.result)}</td>
        <td>${App.esc(cmd.created_by || '-')}</td>
        <td title="${App.esc((cmd.result && cmd.result.error) || '')}">${App.esc((cmd.result && cmd.result.error) ? String(cmd.result.error).slice(0, 60) : '-')}</td>
      </tr>`).join('');
      document.getElementById('a-table').innerHTML = `<table>
        <tr><th>时间</th><th>设备</th><th>类型</th><th>结果</th><th>操作者</th><th>错误</th></tr>
        ${rows || '<tr><td colspan="6" class="muted">无指令</td></tr>'}
      </table>`;
    }
    document.getElementById('a-refresh').onclick = refresh;
    await refresh();

    async function refreshTokens() {
      const tokens = await API.get(`${API_}/tokens`);
      document.getElementById('tok-list').innerHTML = `<table>
        <tr><th>角色</th><th>label</th><th>创建时间</th></tr>
        ${(tokens || []).map(t => `<tr><td><span class="badge ${t.role === 'admin' ? 'green' : 'yellow'}">${App.esc(t.role)}</span></td>
          <td>${App.esc(t.label || '-')}</td>
          <td>${App.esc((t.created_at || '').toString().replace('T', ' ').slice(0, 19))}</td></tr>`).join('')}
      </table>`;
    }
    await refreshTokens();

    const createBtn = document.getElementById('tok-create');
    if (createBtn) createBtn.onclick = async () => {
      try {
        const r = await API.post(`${API_}/tokens`, {
          role: document.getElementById('tok-role').value,
          label: document.getElementById('tok-label').value,
        });
        const out = document.getElementById('tok-out');
        out.classList.remove('hidden');
        out.textContent = '新 token（仅显示一次）：' + r.token;
        refreshTokens();
      } catch (e) { App.toast(e.message, true); }
    };
  }

  App.register('audit', '审计', render);
})();
