// tasks.js：任务与操作页（运行时中心，multica 产品思路）。
// 指派粒度 = 运行时（本机 agent 实例，天然绑定机器；runtime_id 跨设备会重复，
// 因此选择器 value = device_id|runtime_id 复合键，提交/查找一律按复合键解析）。
(function () {
  const API_ = '/api/v1/admin';
  const timers = [];
  let runtimes = [];
  let expandedTask = null;   // 已展开详情的任务（重渲染保留）
  let taskLimit = 50;        // 「加载更多」
  const TERMINAL = ['completed', 'failed', 'cancelled', 'timeout', 'expired'];

  function statusBadge(s) {
    const map = { queued: 'gray', running: 'blue', completed: 'green', failed: 'red', cancelled: 'yellow', timeout: 'red', expired: 'gray' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  // 复合键 ↔ 运行时（device_id|runtime_id，全局唯一）
  function rtKey(deviceID, runtimeID) { return deviceID + '|' + runtimeID; }
  function currentRuntime() {
    const key = document.getElementById('q-runtime') ? document.getElementById('q-runtime').value : '';
    if (!key) return null;
    const [dev, rid] = key.split('|');
    return runtimes.find(r => r.device_id === dev && r.runtime_id === rid) || null;
  }

  async function render(c) {
    try { runtimes = await API.get(API_ + '/runtimes'); } catch (e) { runtimes = []; }
    const usable = runtimes.filter(r => r.ok && r.device_status === 'online');
    const devices = await API.get(API_ + '/devices').catch(() => []);
    const hostOf = id => { const d = (devices || []).find(x => x.id === id); return d ? d.hostname : id.slice(0, 13); };

    // 运行时选项（去重：label 相同追加短 id）
    const labelCount = {};
    const rtOptions = usable.map(r => {
      const base = `${r.hostname} · ${r.type} ${r.version || ''}${(r.labels || []).length ? ' [' + r.labels.join(',') + ']' : ''}`;
      labelCount[base] = (labelCount[base] || 0) + 1;
      const label = labelCount[base] > 1 ? base + '（' + r.runtime_id.slice(0, 8) + '）' : base;
      return `<option value="${App.esc(rtKey(r.device_id, r.runtime_id))}">${App.esc(label)}</option>`;
    }).join('');

    c.innerHTML = `<div class="page-title">任务与操作</div>

    <div class="card">
      <h3>选择运行时</h3>
      <div class="form-row">
        <label>运行时</label>
        <select id="q-runtime" style="min-width:380px">${rtOptions || '<option disabled>暂无可用运行时（设备在线 + agent 可用后自动出现）</option>'}</select>
        <button class="small" id="q-reload">刷新</button>
      </div>
      <p class="muted">运行时 = 设备上的一个 agent 实例（自动发现上报）。新增运行时：在「设备」页点设备行进详情 → 「添加运行时」。</p>
    </div>

    <details class="card">
      <summary style="cursor:pointer;color:#57606a">自定义任务（手动输入 prompt，交给所选运行时执行）</summary>
      <div style="margin-top:12px">
      ${App.canWrite() ? `
      <div class="form-row">
        <label>运行时</label>
        <span id="t-runtime-label" class="badge blue"></span>
        <span class="muted">使用上方所选运行时</span>
        <label>超时(秒)</label><input id="t-timeout" type="number" value="600" style="width:80px">
      </div>
      <textarea id="t-prompt" placeholder="任务 prompt…"></textarea>
      <div class="form-row" style="margin-top:8px"><button id="t-create" class="primary">下发任务</button></div>`
      : '<span class="muted">viewer 只读</span>'}
      </div>
    </details>

    <div class="card">
      <h3>快捷操作</h3>
      ${App.canWrite() ? `
      <div class="form-row">
        <button id="q-scan" class="primary">扫描 skill</button>
        <button id="q-collect-all" class="primary">获取全部 skill</button>
        <button id="q-probe">探测 agent</button>
      </div>
      <div id="q-status" class="muted">点击操作后指令下发到所选运行时所在的设备，本地 worker 执行并自动回传结果。</div>
      <div id="q-result" style="margin-top:12px"></div>`
      : '<span class="muted">viewer 只读</span>'}
    </div>

    <div class="card">
      <h3>任务列表（自动刷新）</h3>
      <div class="form-row">
        <label>设备</label>
        <select id="t-dev-filter"><option value="">全部</option>${(devices || []).map(d => `<option value="${d.id}">${App.esc(d.hostname)}${(d.labels || []).length ? ' [' + App.esc(d.labels.join(',')) + ']' : ''}（${App.esc(d.id.slice(0, 8))}）</option>`).join('')}</select>
        <button class="small" id="t-refresh">刷新</button>
      </div>
      <div id="t-table"></div>
      <div class="form-row" style="margin-top:8px"><button class="small hidden" id="t-more">加载更多</button></div>
    </div>
    <div class="card hidden" id="t-detail-card"><h3>任务详情</h3><div id="t-detail"></div></div>`;

    // 事件委托：行点击与「加载更多」在重渲染后依然有效
    const table = document.getElementById('t-table');
    table.addEventListener('click', e => {
      const tr = e.target.closest('tr[data-id]');
      if (tr) { expandedTask = tr.dataset.id; showTask(tr.dataset.dev, tr.dataset.id); }
    });
    document.getElementById('t-more').onclick = () => { taskLimit += 50; refresh(); };
    document.getElementById('t-refresh').onclick = refresh;
    document.getElementById('t-dev-filter').onchange = () => { taskLimit = 50; refresh(); };
    const iv = setInterval(refresh, 5000);
    timers.push(() => clearInterval(iv));
    if (expandedTask) showTask(expandedTask.split('|')[0] || '', expandedTask);
    await refresh();

    // ---- 快捷操作 ----
    function setBusy(msg) { const el = document.getElementById('q-status'); if (el) el.textContent = msg; }
    function out() { return document.getElementById('q-result'); }

    async function runCmd(cmdType, payload, busyMsg) {
      const rt = currentRuntime();
      if (!rt) { App.toast('先选择运行时', true); return null; }
      setBusy(busyMsg + `：指令已下发到 ${rt.hostname}${(rt.labels || []).length ? ' [' + rt.labels.join(',') + ']' : ''}，等待本地 worker 执行…`);
      const r = await API.post(API_ + '/commands', { device_id: rt.device_id, cmd_type: cmdType, payload, ttl_seconds: 120 });
      return API.pollCmd(r.cmd_id);
    }

    async function scanSkills() {
      out().innerHTML = '';
      try {
        const r = await runCmd('skills.scan', {}, '扫描 skill');
        if (!r || !r.ok) { setBusy('扫描失败：' + (r ? r.error : '无运行时')); return; }
        const ms = r.data.manifests || [];
        setBusy('扫描完成：发现 ' + ms.length + ' 个 skill');
        out().innerHTML = !ms.length ? '<span class="muted">设备白名单目录里没有发现 skill</span>' : `<table>
          <tr><th>名称</th><th>版本</th><th>摘要</th><th>本地</th><th>云端</th><th>操作</th></tr>
          ${ms.map(m => {
            const reg = !!m.registered;
            const collectDisabled = reg && m.local_state === 'clean';
            return `<tr>
            <td><b>${App.esc(m.name)}</b></td>
            <td>v${App.esc(m.version)}</td>
            <td>${App.esc(m.summary || '')}</td>
            <td>${m.local_state === 'modified' ? '<span class="badge yellow">本地有改动</span>' : m.local_state === 'clean' ? '<span class="badge green">clean</span>' : '<span class="badge gray">新</span>'}</td>
            <td>${reg ? '<span class="badge blue">已登记</span>' : '<span class="badge gray">未登记</span>'}</td>
            <td>${collectDisabled
              ? '<span class="muted">已是最新</span>'
              : `<button class="small primary" data-collect="${App.esc(m.name)}">获取</button>`}</td>
          </tr>`;
          }).join('')}
          <tr><td colspan="6"><button class="small" data-collect-all>获取未登记项</button></td></tr>
        </table>`;
        out().querySelectorAll('[data-collect]').forEach(b => { b.onclick = () => collectSkill(b.dataset.collect); });
        const all = out().querySelector('[data-collect-all]');
        if (all) all.onclick = () => collectAll(ms.filter(m => !(m.registered && m.local_state === 'clean')).map(m => m.name));
      } catch (e) { setBusy('扫描失败：' + e.message); }
    }

    async function collectSkill(name) {
      App.toast('正在获取 ' + name + '…');
      try {
        const r = await runCmd('skills.collect', { name }, '获取 ' + name + '：本地打包上传中…');
        if (!r) return;
        if (!r.ok) { setBusy(name + ' 获取失败：' + r.error); App.toast(name + ' 获取失败：' + r.error, true); return; }
        const ing = r.envelope.ingest || '';
        const msg = name + ' v' + (r.data.version || '') + ' 已上报（' + (ing === 'unchanged' ? '内容无变化' : '新登记') + '）';
        setBusy(msg + ' → 去「Skill」页审阅发布');
        // 引导：带上 skill 名的深链接
        try { sessionStorage.setItem('gsevo_prefill_skill', name); } catch (e) {}
        out().insertAdjacentHTML('beforeend',
          `<div class="form-row"><a href="#skills" onclick="location.hash='#skills';App.go('skills');return false;">→ 去「Skill」页审阅「${App.esc(name)}」</a></div>`);
        App.toast(msg);
        scanSkillsQuiet();
      } catch (e) { App.toast(e.message, true); }
    }

    // 获取后静默重扫（刷新本地/云端状态列，不打断用户）
    async function scanSkillsQuiet() {
      try {
        const r = await runCmd('skills.scan', {}, '');
        if (r && r.ok) { const keep = out().innerHTML; scanSkillsRender(r); }
      } catch (e) { /* 忽略 */ }
    }
    function scanSkillsRender(r) {
      const ms = r.data.manifests || [];
      setBusy('扫描完成：发现 ' + ms.length + ' 个 skill（含最新云端状态）');
      out().innerHTML = !ms.length ? '' : `<table>
        <tr><th>名称</th><th>版本</th><th>摘要</th><th>本地</th><th>云端</th><th>操作</th></tr>
        ${ms.map(m => `<tr>
          <td><b>${App.esc(m.name)}</b></td><td>v${App.esc(m.version)}</td><td>${App.esc(m.summary || '')}</td>
          <td>${m.local_state === 'modified' ? '<span class="badge yellow">本地有改动</span>' : m.local_state === 'clean' ? '<span class="badge green">clean</span>' : '<span class="badge gray">新</span>'}</td>
          <td>${m.registered ? '<span class="badge blue">已登记</span>' : '<span class="badge gray">未登记</span>'}</td>
          <td>${(m.registered && m.local_state === 'clean') ? '<span class="muted">已是最新</span>' : `<button class="small primary" data-collect="${App.esc(m.name)}">获取</button>`}</td>
        </tr>`).join('')}
      </table>`;
      out().querySelectorAll('[data-collect]').forEach(b => { b.onclick = () => collectSkill(b.dataset.collect); });
    }

    async function collectAll(names) {
      const results = [];
      for (const n of names) {
        let r = null;
        try { r = await runCmd('skills.collect', { name: n }, '获取 ' + n + '…'); } catch (e) { App.toast(e.message, true); }
        let note = '❌';
        if (r && r.ok) note = (r.envelope.ingest === 'unchanged') ? '内容无变化' : '✅ 已登记';
        else if (r) note = '❌ ' + (r.error || '');
        results.push({ name: n, note: note });
      }
      setBusy('全部获取完成 → ' + results.map(x => x.name + ': ' + x.note).join('  ') + '（去「Skill」页审阅发布）');
    }

    if (App.canWrite()) {
      document.getElementById('q-scan').onclick = scanSkills;
      document.getElementById('q-collect-all').onclick = async () => {
        setBusy('获取全部 skill：先扫描…');
        const r = await runCmd('skills.scan', {}, '扫描中');
        if (!r || !r.ok) { setBusy('扫描失败：' + (r ? r.error : '无运行时')); return; }
        const names = (r.data.manifests || []).map(m => m.name);
        if (!names.length) { setBusy('白名单目录里没有 skill'); return; }
        out().innerHTML = '';
        await collectAll(names);
        scanSkillsQuiet();
      };
      document.getElementById('q-probe').onclick = async () => {
        out().innerHTML = '';
        try {
          const r = await runCmd('agent.probe', {}, '探测 agent');
          const agents = r.data.agents || [];
          setBusy('探测完成（仅覆盖内置 agent 类型；运行时列表见「设备」详情）');
          out().innerHTML = `<table><tr><th>类型</th><th>状态</th><th>版本</th><th>路径</th></tr>
            ${agents.map(a => `<tr><td>${App.esc(a.type)}</td><td>${a.ok ? '<span class="badge green">ok</span>' : '<span class="badge gray">未安装</span>'}</td><td>${App.esc(a.version || '-')}</td><td>${App.esc(a.path || '-')}</td></tr>`).join('')}
          </table>`;
        } catch (e) { setBusy('探测失败：' + e.message); }
      };
      document.getElementById('t-create').onclick = async () => {
        const rt = currentRuntime();
        const prompt = document.getElementById('t-prompt').value.trim();
        if (!rt) { App.toast('先在上方选择运行时', true); return; }
        if (!prompt) { App.toast('填写 prompt', true); return; }
        try {
          const r = await API.post(API_ + '/tasks', {
            device_id: rt.device_id, prompt, runtime_id: rt.runtime_id,
            timeout_s: parseInt(document.getElementById('t-timeout').value) || 600,
          });
          App.toast('任务已下发：' + r.task_id);
          document.getElementById('t-prompt').value = '';
          expandedTask = r.task_id;
          showTask(rt.device_id, r.task_id);
          taskLimit = Math.max(taskLimit, 50);
          refresh();
        } catch (e) { App.toast(e.message, true); }
      };
      // 运行时切换：更新高级区标签
      const qsel = document.getElementById('q-runtime');
      if (qsel) qsel.onchange = () => {
        const rt = currentRuntime();
        const lb = document.getElementById('t-runtime-label');
        if (lb && rt) lb.textContent = rt.hostname + ' · ' + rt.type;
      };
      if (qsel) qsel.dispatchEvent(new Event('change'));
    }

    // ---- 任务列表 ----
    async function refresh() {
      if (!document.getElementById('t-table')) return; // 页面已切走
      const dev = document.getElementById('t-dev-filter') ? document.getElementById('t-dev-filter').value : '';
      const tasks = await API.get(API_ + '/tasks?device_id=' + dev + '&limit=' + taskLimit).catch(() => []);
      const rows = (tasks || []).map(t => `<tr style="cursor:pointer" data-id="${t.id}" data-dev="${t.device_id}" class="${t.id === expandedTask ? 'active-row' : ''}">
        <td title="${App.esc(t.id)}">${App.esc(t.id.slice(0, 13))}…</td>
        <td>${App.esc(t.device_id.slice(0, 13))}…</td>
        <td title="${App.esc(t.prompt)}">${App.esc(t.prompt.slice(0, 40))}</td>
        <td>${statusBadge(t.status)}</td>
        <td>${App.fmtTime(t.created_at)}</td>
      </tr>`).join('');
      const emptyMsg = dev ? '该设备暂无任务记录' : '暂无任务';
      table.innerHTML = `<table>
        <tr><th>ID</th><th>设备</th><th>Prompt</th><th>状态</th><th>创建时间（本机时区）</th></tr>
        ${rows || `<tr><td colspan="5" class="muted">${emptyMsg}</td></tr>`}
      </table>`;
      const more = document.getElementById('t-more');
      if ((tasks || []).length >= taskLimit) more.classList.remove('hidden'); else more.classList.add('hidden');
    }

    // ---- 任务详情（终态隐藏取消；事件轮询中检测终态并刷新列表）----
    let listDirty = false;
    async function showTask(deviceID, taskID) {
      expandedTask = taskID;
      const card = document.getElementById('t-detail-card');
      card.classList.remove('hidden');
      const el = document.getElementById('t-detail');
      el.innerHTML = '加载中…';
      async function draw() {
        if (!document.getElementById('t-detail-card')) return; // 页面已切走
        const events = await API.get(API_ + '/tasks/' + taskID + '/events?limit=500').catch(() => []);
        const evHtml = (events || []).map(e => {
          const t = App.fmtTime(e.ts).slice(11);
          let body = '';
          if (e.kind === 'message' || e.kind === 'stdout') body = App.esc((e.data || {}).text || '');
          else if (e.kind === 'stderr') body = `<span style="color:#888">${App.esc((e.data || {}).text || '')}</span>`;
          else if (e.kind === 'tool_call') body = '🔧 ' + App.esc((e.data || {}).name || '') + ' ' + App.esc(JSON.stringify((e.data || {}).input || {}));
          else if (e.kind === 'status') body = '状态 → ' + App.esc((e.data || {}).status || '') + ' ' + App.esc((e.data || {}).reason || '');
          return `<div>[${t}] seq${e.seq} <b>${App.esc(e.kind)}</b> ${body}</div>`;
        }).join('') || '<span class="muted">暂无事件</span>';
        const tasks = await API.get(API_ + '/tasks?device_id=' + deviceID + '&limit=100').catch(() => []);
        const row = (tasks || []).find(x => x.id === taskID) || {};
        const terminal = TERMINAL.includes(row.status);
        el.innerHTML = `<div class="form-row">
            <span>状态 ${statusBadge(row.status || 'queued')}</span>
            ${row.runtime_id ? `<span class="muted">运行时 ${App.esc(row.runtime_id.slice(0, 12))}…</span>` : ''}
            ${terminal ? `<span class="muted">已结束 ${App.fmtTime(row.finished_at || '')}</span>` : ''}
          </div>
          <pre style="max-height:320px;overflow-y:auto">${evHtml}</pre>
          <div class="form-row" style="margin-top:8px">
            ${App.canWrite() && !terminal ? `<button id="t-cancel" class="danger small">取消任务</button>` : ''}
            <span class="muted">task: ${App.esc(taskID)}</span>
          </div>`;
        const cancelBtn = document.getElementById('t-cancel');
        if (cancelBtn) cancelBtn.onclick = async () => {
          if (!confirm('取消该任务？（将终止设备上的 agent 进程树）')) return;
          try {
            await API.post(API_ + '/tasks/' + taskID + '/cancel');
            App.toast('已请求取消');
            // 快速轮询直到终态翻转（≤15s）
            const deadline = Date.now() + 15000;
            while (Date.now() < deadline) {
              await new Promise(res => setTimeout(res, 2000));
              await refresh();
              const rows = await API.get(API_ + '/tasks?device_id=' + deviceID + '&limit=50').catch(() => []);
              const rr = (rows || []).find(x => x.id === taskID);
              if (rr && TERMINAL.includes(rr.status)) break;
            }
            draw();
          } catch (e) { App.toast(e.message, true); }
        };
        // 事件流中出现终态 → 标记列表待刷（下一轮 5s 刷新必到）
        if (terminal && events.some(e => e.kind === 'status')) listDirty = true;
      }
      await draw();
      const iv = setInterval(async () => {
        await draw();
        if (listDirty) { listDirty = false; refresh(); }
      }, 3000);
      timers.push(() => clearInterval(iv));
    }
  }

  // 一期目标为 skill 收集：任务下发（自定义 prompt）为架构预留，
  // 不经导航露出（hash #tasks 直达仍可用）。未来扩展时恢复 nav 注册即可。
  App.register('tasks', '任务与操作', (c) => {
    timers.splice(0).forEach(fn => fn());
    expandedTask = null;
    return render(c);
  }, { nav: false });
})();
