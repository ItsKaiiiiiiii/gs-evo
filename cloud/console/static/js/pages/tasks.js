// tasks.js：任务与操作页。
// 主路径是一键功能（下发指令 → 本地 worker 执行 → 自动上报回执）：
//   扫描 skill / 获取 skill（skills.collect）/ 探测 agent。
// 自定义 prompt 任务保留为「高级」折叠项。
(function () {
  const API_ = '/api/v1/admin';
  const timers = [];

  function statusBadge(s) {
    const map = { queued: 'gray', running: 'blue', completed: 'green', failed: 'red', cancelled: 'yellow', timeout: 'red', expired: 'gray' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  async function render(c) {
    const devices = await API.get(`${API_}/devices`);
    const online = devices.filter(d => d.status === 'online');

    c.innerHTML = `<div class="page-title">任务与操作</div>

    <!-- 快捷操作：一键下发指令，worker 执行后自动上报 -->
    <div class="card">
      <h3>快捷操作</h3>
      ${App.canWrite() ? `
      <div class="form-row">
        <label>目标设备</label>
        <select id="q-device" style="min-width:220px">
          ${online.map(d => `<option value="${d.id}">${App.esc(d.hostname)} [${(d.labels || []).join(',')}]</option>`).join('') || '<option disabled>无在线设备</option>'}
        </select>
        <button id="q-scan" class="primary">扫描 skill</button>
        <button id="q-collect-all" class="primary">获取全部 skill</button>
        <button id="q-probe">探测 agent</button>
      </div>
      <div id="q-status" class="muted">选择在线设备后点击操作；指令由本地 worker 执行并自动回传结果。</div>
      <div id="q-result" style="margin-top:12px"></div>`
      : '<span class="muted">viewer 只读</span>'}
    </div>

    <!-- 任务列表 -->
    <div class="card">
      <h3>任务列表（30s 自动刷新）</h3>
      <div id="t-table"></div>
      <div class="form-row" style="margin-top:8px">
        <select id="t-dev-filter"><option value="">全部设备</option>${devices.map(d => `<option value="${d.id}">${App.esc(d.hostname)}</option>`).join('')}</select>
        <button class="small" id="t-refresh">刷新</button>
      </div>
    </div>
    <div class="card hidden" id="t-detail-card"><h3>任务详情</h3><div id="t-detail"></div></div>

    <!-- 高级：自定义 prompt 任务 -->
    <details class="card">
      <summary style="cursor:pointer;color:#57606a">高级：自定义任务（手动输入 prompt，交给本机 agent 执行）</summary>
      <div style="margin-top:12px">
      ${App.canWrite() ? `
      <div class="form-row">
        <label>设备</label>
        <select id="t-device">${online.map(d => `<option value="${d.id}">${App.esc(d.hostname)}（在线）</option>`).join('') || '<option disabled>无在线设备</option>'}</select>
        <label>agent</label>
        <select id="t-agent"><option value="">设备默认</option><option value="gsagent">gsagent（自定义二进制）</option><option value="claude-code">claude-code</option></select>
        <label>超时(秒)</label><input id="t-timeout" type="number" value="600" style="width:80px">
      </div>
      <textarea id="t-prompt" placeholder="任务 prompt…"></textarea>
      <div class="form-row" style="margin-top:8px"><button id="t-create" class="primary">下发任务</button></div>`
      : '<span class="muted">viewer 只读</span>'}
      </div>
    </details>`;

    // ---- 快捷操作 ----
    const deviceOf = () => document.getElementById('q-device').value;
    const setBusy = (msg) => { document.getElementById('q-status').textContent = msg || '就绪'; };
    const out = () => document.getElementById('q-result');

    async function runCmd(cmdType, payload, busyMsg) {
      const device = deviceOf();
      if (!device) { App.toast('无在线设备', true); return null; }
      setBusy(busyMsg + '：指令已下发，等待本地 worker 执行…');
      const r = await API.post(`${API_}/commands`, { device_id: device, cmd_type: cmdType, payload, ttl_seconds: 120 });
      return API.pollCmd(r.cmd_id);
    }

    // 扫描 skill：展示清单，每行带「获取」按钮
    async function scanSkills() {
      setBusy('扫描 skill：指令已下发，等待本地 worker 执行…');
      out().innerHTML = '';
      try {
        const r = await runCmd('skills.scan', {}, '扫描 skill');
        if (!r.ok) { setBusy('扫描失败：' + r.error); return; }
        const ms = r.data.manifests || [];
        setBusy(`扫描完成：发现 ${ms.length} 个 skill`);
        out().innerHTML = !ms.length ? '<span class="muted">设备白名单目录里没有发现 skill</span>' : `<table>
          <tr><th>名称</th><th>版本</th><th>摘要</th><th>本地状态</th><th>操作</th></tr>
          ${ms.map(m => `<tr>
            <td><b>${App.esc(m.name)}</b></td>
            <td>v${App.esc(m.version)}</td>
            <td>${App.esc(m.summary || '')}</td>
            <td>${m.local_state === 'modified' ? '<span class="badge yellow">本地有改动</span>' : m.local_state === 'clean' ? '<span class="badge green">clean</span>' : '<span class="badge blue">新</span>'}</td>
            <td><button class="small primary" data-collect="${App.esc(m.name)}">获取</button></td>
          </tr>`).join('')}
          <tr><td colspan="5"><button class="small" data-collect-all>全部获取</button></td></tr>
        </table>`;
        out().querySelectorAll('[data-collect]').forEach(b => { b.onclick = () => collectSkill(b.dataset.collect, collectOneDone); });
        const all = out().querySelector('[data-collect-all]');
        if (all) all.onclick = () => collectAll(ms.map(m => m.name));
      } catch (e) { setBusy('扫描失败：' + e.message); }
    }

    async function collectSkill(name, done) {
      App.toast(`正在获取 ${name}…`);
      try {
        const r = await runCmd('skills.collect', { name }, `获取 ${name}：本地打包上传中…`);
        if (done) done(name, r);
      } catch (e) { App.toast(e.message, true); }
    }

    function collectOneDone(name, r) {
      if (!r.ok) { setBusy(`${name} 获取失败：${r.error}`); App.toast(`${name} 获取失败`, true); return; }
      const ing = r.envelope.ingest || '';
      if (r.status === 'acked' && (r.data.version)) {
        setBusy(`${name} v${r.data.version} 已上报云端（${ing === 'unchanged' ? '内容无变化' : '新登记'}）。去「Skill」页可审阅与发布。`);
        App.toast(`${name} 获取成功（${ing === 'unchanged' ? '内容无变化' : '已登记新版本'}）`);
      } else {
        setBusy(`${name} 上报状态：${ing || r.status}`);
        App.toast(`${name}：${ing || r.status}`);
      }
    }

    async function collectAll(names) {
      const results = [];
      for (const n of names) {
        let r = null;
        await collectSkill(n, (nn, rr) => { r = rr; });
        results.push({ name: n, r });
      }
      const line = results.map(({ name, r }) => `${name}: ${r && r.ok ? '✅' : '❌'}`).join('  ');
      setBusy('全部获取完成 → ' + line + '（去「Skill」页审阅发布）');
    }

    if (App.canWrite()) {
      document.getElementById('q-scan').onclick = scanSkills;
      document.getElementById('q-probe').onclick = async () => {
        out().innerHTML = '';
        try {
          const r = await runCmd('agent.probe', {}, '探测 agent');
          const agents = r.data.agents || [];
          setBusy('探测完成');
          out().innerHTML = `<table><tr><th>类型</th><th>状态</th><th>版本</th><th>路径</th></tr>
            ${agents.map(a => `<tr><td>${App.esc(a.type)}</td><td>${a.ok ? '<span class="badge green">ok</span>' : '<span class="badge gray">未安装</span>'}</td><td>${App.esc(a.version || '-')}</td><td>${App.esc(a.path || '-')}</td></tr>`).join('')}
          </table>`;
        } catch (e) { setBusy('探测失败：' + e.message); }
      };
      document.getElementById('q-collect-all').onclick = async () => {
        // 先扫描再逐个收集（收集的 root 由 worker 自行定位）
        setBusy('获取全部 skill：先扫描…');
        const r = await runCmd('skills.scan', {}, '扫描中');
        if (!r.ok) { setBusy('扫描失败：' + r.error); return; }
        const names = (r.data.manifests || []).map(m => m.name);
        if (!names.length) { setBusy('白名单目录里没有 skill'); return; }
        await collectAll(names);
      };
    }

    // ---- 任务列表（保留） ----
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

    // ---- 任务详情 ----
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
  }

  App.register('tasks', '任务与操作', (c) => {
    timers.splice(0).forEach(fn => fn());
    return render(c);
  });
})();
