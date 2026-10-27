// tasks.js：任务页——下发 prompt、状态轮询、事件流查看、取消。
(function () {
  const API_ = '/api/v1/admin';
  const timers = [];

  function statusBadge(s) {
    const map = { queued: 'gray', running: 'blue', completed: 'green', failed: 'red', cancelled: 'yellow', timeout: 'red', expired: 'gray' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  async function render(c) {
    const devices = await API.get(`${API_}/devices`);
    const usable = devices.filter(d => d.status === 'online');
    c.innerHTML = `<div class="page-title">任务</div>
      <div class="card">
        <h3>下发任务</h3>
        ${App.canWrite() ? `
        <div class="form-row">
          <label>设备</label>
          <select id="t-device">${usable.map(d => `<option value="${d.id}">${App.esc(d.hostname)}（在线）</option>`).join('') || '<option disabled>无在线设备</option>'}</select>
          <label>agent</label>
          <select id="t-agent"><option value="">设备默认</option><option value="gsagent">gsagent（自定义二进制）</option><option value="claude-code">claude-code</option></select>
          <label>超时(秒)</label><input id="t-timeout" type="number" value="600" style="width:80px">
        </div>
        <textarea id="t-prompt" placeholder="任务 prompt…"></textarea>
        <div class="form-row" style="margin-top:8px"><button id="t-create" class="primary">下发任务</button></div>`
        : '<span class="muted">viewer 只读</span>'}
      </div>
      <div class="card"><h3>任务列表（30s 自动刷新）</h3><div id="t-table"></div>
        <div class="form-row" style="margin-top:8px">
          <select id="t-dev-filter"><option value="">全部设备</option>${devices.map(d => `<option value="${d.id}">${App.esc(d.hostname)}</option>`).join('')}</select>
          <button class="small" id="t-refresh">刷新</button>
        </div>
      </div>
      <div class="card hidden" id="t-detail-card"><h3>任务详情</h3><div id="t-detail"></div></div>`;

    if (App.canWrite()) {
      document.getElementById('t-create').onclick = async () => {
        const device = document.getElementById('t-device').value;
        const prompt = document.getElementById('t-prompt').value.trim();
        if (!device || !prompt) { App.toast('选设备并填写 prompt', true); return; }
        try {
          const r = await API.post(`${API_}/tasks`, {
            device_id: device, prompt,
            agent_type: document.getElementById('t-agent').value,
            timeout_s: parseInt(document.getElementById('t-timeout').value) || 600,
          });
          App.toast('任务已下发：' + r.task_id);
          document.getElementById('t-prompt').value = '';
          showTask(device, r.task_id);
          refresh();
        } catch (e) { App.toast(e.message, true); }
      };
    }

    async function refresh() {
      const dev = document.getElementById('t-dev-filter').value;
      const tasks = await API.get(`${API_}/tasks?device_id=${dev}&limit=50`);
      const rows = (tasks || []).map(t => `<tr style="cursor:pointer" data-id="${t.id}" data-dev="${t.device_id}">
        <td title="${App.esc(t.id)}">${App.esc(t.id.slice(0, 13))}…</td>
        <td>${App.esc(t.device_id.slice(0, 13))}…</td>
        <td title="${App.esc(t.prompt)}">${App.esc(t.prompt.slice(0, 40))}</td>
        <td>${statusBadge(t.status)}</td>
        <td>${App.esc((t.created_at || '').replace('T', ' ').slice(0, 19))}</td>
      </tr>`).join('');
      document.getElementById('t-table').innerHTML = `<table>
        <tr><th>ID</th><th>设备</th><th>Prompt</th><th>状态</th><th>创建时间</th></tr>
        ${rows || '<tr><td colspan="5" class="muted">暂无任务</td></tr>'}
      </table>`;
      document.querySelectorAll('#t-table tr[data-id]').forEach(tr => {
        tr.onclick = () => showTask(tr.dataset.dev, tr.dataset.id);
      });
    }
    document.getElementById('t-refresh').onclick = refresh;
    document.getElementById('t-dev-filter').onchange = refresh;
    const iv = setInterval(refresh, 30000);
    timers.push(() => clearInterval(iv));
    await refresh();
  }

  // 任务详情：结果 + 事件流（运行中 3s 轮询）
  async function showTask(deviceID, taskID) {
    const card = document.getElementById('t-detail-card');
    card.classList.remove('hidden');
    const el = document.getElementById('t-detail');
    el.innerHTML = '加载中…';

    async function draw() {
      const events = await API.get(`${API_}/tasks/${taskID}/events?limit=500`);
      const evHtml = (events || []).map(e => {
        const t = (e.ts || '').replace('T', ' ').slice(11, 19);
        let body = '';
        if (e.kind === 'message' || e.kind === 'stdout' || e.kind === 'stderr') body = App.esc((e.data || {}).text || '');
        else if (e.kind === 'tool_call') body = `🔧 ${App.esc((e.data || {}).name || '')} ${App.esc(JSON.stringify((e.data || {}).input || {}))}`;
        else if (e.kind === 'status') body = `状态 → ${App.esc((e.data || {}).status || '')} ${App.esc((e.data || {}).reason || '')}`;
        return `<div>[${t}] seq${e.seq} <b>${App.esc(e.kind)}</b> ${body}</div>`;
      }).join('') || '<span class="muted">暂无事件</span>';
      el.innerHTML = `<pre style="max-height:320px;overflow-y:auto">${evHtml}</pre>
        <div class="form-row" style="margin-top:8px">
          ${App.canWrite() ? `<button id="t-cancel" class="danger small">取消任务</button>` : ''}
          <span class="muted">task: ${App.esc(taskID)}</span>
        </div>`;
      const cancelBtn = document.getElementById('t-cancel');
      if (cancelBtn) cancelBtn.onclick = async () => {
        if (!confirm('取消该任务？（将终止设备上的 agent 进程树）')) return;
        try { await API.post(`${API_}/tasks/${taskID}/cancel`); App.toast('已请求取消'); draw(); } catch (e) { App.toast(e.message, true); }
      };
    }
    await draw();
    const iv = setInterval(draw, 3000);
    timers.push(() => clearInterval(iv));
  }

  // 离开页面时清掉轮询
  App.register('tasks', '任务', (c) => {
    timers.splice(0).forEach(fn => fn());
    return render(c);
  });
})();
