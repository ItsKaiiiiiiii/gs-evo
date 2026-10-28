// skills.js：Skill 实体页——版本时间线（diff/rationale/评估/策略/覆盖率）+ 全部动作。
// 评审裂伤一修复：一个 skill 的生命周期动作收敛在本页，进化对象的全局视图在「进化」页。
(function () {
  const API_ = '/api/v1/admin';
  const CHAIN = ['draft', 'candidate', 'reviewed', 'stable'];

  function statusBadge(s) {
    const map = { draft: 'gray', candidate: 'yellow', reviewed: 'blue', stable: 'green', deprecated: 'red' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  async function render(c) {
    const devices = await API.get(`${API_}/devices`).catch(() => []);
    const online = (devices || []).filter(d => d.status === 'online');

    c.innerHTML = `<div class="page-title">Skill</div>

    <div class="card">
      <h3>设备上的 skill（未收录）</h3>
      <p class="muted">扫描在线设备，发现尚未在云端管理的 skill；导入云端后进入版本管理（审阅 → 发布）。</p>
      ${App.canWrite() ? `
      <div class="form-row">
        <button id="scan-btn" class="primary">扫描在线设备（${online.length} 台）</button>
        ${online.length ? `<select id="scan-dev"><option value="">全部在线设备</option>${online.map(d => `<option value="${d.id}">${App.esc(d.hostname)}</option>`).join('')}</select>` : ''}
      </div>
      <div id="scan-status" class="muted">点击扫描后，指令下发到各设备，由本地 worker 收集清单回传。</div>
      <div id="scan-result" style="margin-top:12px"></div>`
      : '<span class="muted">viewer 只读</span>'}
    </div>

    <div class="card">
      <div class="form-row">
        <label>skill</label>
        <select id="skill-name" style="min-width:200px"></select>
        <button class="small" id="skill-refresh">刷新</button>
        ${App.canWrite() ? `<button class="small primary" id="skill-evolve">生成改进候选（evolve）</button>` : ''}
      </div>
      <div id="evo-dialog" class="hidden">
        <div class="form-row"><label>改进要求</label><input id="evo-instruction" style="flex:1" placeholder="如：补充错误处理说明"></div>
        <div class="form-row"><label>父版本</label><input id="evo-parent" placeholder="默认最新 stable" style="width:140px">
          <button class="small primary" id="evo-run">生成</button></div>
        <div id="evo-out" class="muted"></div>
      </div>
      <div id="cov-summary" class="muted" style="margin-top:8px"></div>
    </div>
    <div id="ver-view"><div class="card">加载中…</div></div>`;

    let skills = [];
    try {
      skills = await API.get(`${API_}/skills`);
    } catch (e) {
      c.innerHTML = `<div class="page-title">Skill</div><div class="card"><span class="muted">无法加载 skill 列表：${App.esc(e.message)}</span></div>`;
      return;
    }
    const sel = document.getElementById('skill-name');
    sel.innerHTML = skills.map(s => `<option>${App.esc(s.name)}</option>`).join('') || '<option disabled>暂无 skill（先在上方扫描设备收集）</option>';
    document.getElementById('skill-refresh').onclick = () => render(c);
    if (App.canWrite()) {
      document.getElementById('skill-evolve').onclick = () => {
        document.getElementById('evo-dialog').classList.toggle('hidden');
      };
      document.getElementById('evo-run').onclick = async () => {
        const name = sel.value;
        const instruction = document.getElementById('evo-instruction').value.trim();
        if (!instruction) { App.toast('填写改进要求', true); return; }
        const out = document.getElementById('evo-out');
        out.textContent = '生成中…';
        try {
          const v = await API.post(`${API_}/skills/${name}/evolve`, {
            instruction,
            parent_version: document.getElementById('evo-parent').value.trim(),
          });
          out.textContent = `候选 v${v.version} 已生成（candidate）。rationale：${v.rationale || ''}`;
          App.toast(`候选 v${v.version} 已生成`);
          loadVersions(name);
        } catch (e) { out.textContent = e.message; App.toast(e.message, true); }
      };
    }

    // ---- 设备上的 skill：扫描 → 聚合 → 导入（一期主流程）----
    const scanBtn = document.getElementById('scan-btn');
    if (scanBtn) {
      const scanStatus = document.getElementById('scan-status');
      const scanDev = document.getElementById('scan-dev');
      scanBtn.onclick = async () => {
        scanBtn.disabled = true;
        const targets = scanDev && scanDev.value ? [scanDev.value] : online.map(d => d.id);
        if (!targets.length) { scanBtn.disabled = false; return; }
        scanStatus.textContent = '扫描中（' + targets.length + ' 台设备）：指令已下发，等待本地 worker 回传清单…';
        try {
          const results = await Promise.all(targets.map(dev =>
            API.post(`${API_}/commands`, { device_id: dev, cmd_type: 'skills.scan', payload: {}, ttl_seconds: 120 })
              .then(r0 => API.pollCmd(r0.cmd_id, 60000).then(r => ({ dev, r })))
              .catch(() => ({ dev, r: null }))
          ));
          const agg = {};
          for (const { dev, r } of results) {
            if (!r || !r.ok) continue;
            for (const m of (r.data.manifests || [])) {
              const k = m.name + '@' + m.version;
              if (!agg[k]) agg[k] = { name: m.name, version: m.version, summary: m.summary || '',
                devices: [], registered: !!m.registered };
              agg[k].devices.push(dev);
              if (m.registered) agg[k].registered = true;
            }
          }
          renderScan(agg);
        } finally {
          scanBtn.disabled = false;
        }
      };
    }

    function renderScan(agg) {
      const el = document.getElementById('scan-result');
      const keys = Object.keys(agg);
      if (!keys.length) { el.innerHTML = '<span class="muted">在线设备上没有发现 skill</span>'; return; }
      el.innerHTML = `<table>
        <tr><th>名称</th><th>版本</th><th>摘要</th><th>所在设备</th><th>云端状态</th><th>操作</th></tr>
        ${keys.map(k => {
          const a = agg[k];
          const reg = a.registered;
          return `<tr>
            <td><b>${App.esc(a.name)}</b></td><td>v${App.esc(a.version)}</td>
            <td>${App.esc(a.summary)}</td>
            <td>${a.devices.length} 台</td>
            <td>${reg ? '<span class="badge blue">已登记</span>' : '<span class="badge yellow">未收录</span>'}</td>
            <td>${App.canWrite() && !reg ? `<button class="small primary" data-imp="${App.esc(k)}">导入</button>` : '—'}</td>
          </tr>`;
        }).join('')}
      </table>`;
      el.querySelectorAll('[data-imp]').forEach(b => {
        b.onclick = async () => {
          const k = b.dataset.imp;
          const a = (scanAgg || agg)[k];
          b.disabled = true;
          b.textContent = '导入中…';
          try {
            const r0 = await API.post(`${API_}/commands`, { device_id: a.devices[0],
              cmd_type: 'skills.collect', payload: { name: a.name }, ttl_seconds: 120 });
            const r = await API.pollCmd(r0.cmd_id, 60000);
            if (!r.ok) { App.toast('导入失败：' + r.error, true); b.disabled = false; b.textContent = '导入'; return; }
            const ing = r.envelope.ingest || '';
            b.textContent = ing === 'unchanged' ? '内容无变化' : '✅ 已导入';
            // 把新导入的 skill 加入下拉并选中，直接进入版本时间线（修复 P0-1 断点）
            await refreshSkillList(a.name);
            App.toast(a.name + ' 已导入云端，已进入版本管理');
          } catch (e) { App.toast(e.message, true); b.disabled = false; b.textContent = '导入'; }
        };
      });
    }
    // 全局聚合供导入使用
    let scanAgg = {};
    const origRenderScan = renderScan;
    renderScan = function(agg) { scanAgg = agg; origRenderScan(agg); };

    // 重拉 skill 列表并选中目标（导入完成后的闭环）
    async function refreshSkillList(selectName) {
      const skills = await API.get(`${API_}/skills`);
      sel.innerHTML = skills.map(s => `<option>${App.esc(s.name)}</option>`).join('');
      if (selectName) {
        sel.value = selectName;
        if (sel.value !== selectName) { // 列表没有则补一个
          sel.insertAdjacentHTML('beforeend', `<option>${App.esc(selectName)}</option>`);
          sel.value = selectName;
        }
      }
      await loadVersions(sel.value);
    }
    window.__refreshSkillList = refreshSkillList;

    sel.onchange = () => loadVersions(sel.value);
    // 深链接预填：任务页「去 Skill 页审阅 X」
    try {
      const pre = sessionStorage.getItem('gsevo_prefill_skill');
      if (pre) {
        sessionStorage.removeItem('gsevo_prefill_skill');
        if ([...sel.options].some(o => o.value === pre)) sel.value = pre;
      }
    } catch (e) { /* 忽略 */ }
    if (skills.length) await loadVersions(sel.value);
    else document.getElementById('ver-view').innerHTML = '<div class="card"><span class="muted">暂无 skill。去「任务与操作」页点「扫描 skill / 获取全部 skill」收集第一份。</span></div>';
  }

  async function loadVersions(name) {
    const view = document.getElementById('ver-view');
    if (!name) {
      view.innerHTML = '<div class="card"><span class="muted">先选择 skill</span></div>';
      return;
    }
    view.innerHTML = '<div class="card">加载中…</div>';
    try {
    const [vers, cov, policy] = await Promise.all([
      API.get(`${API_}/skills/${name}/versions`),
      API.get(`${API_}/skills/${name}/coverage`).catch(() => []),
      API.get(`${API_}/skills/${name}/auto-policy`).catch(() => ({})),
    ]);

    // 覆盖率汇总（评审问题 6）
    const okCount = (cov || []).filter(x => x.result === 'ok').length;
    document.getElementById('cov-summary').innerHTML =
      `装机覆盖：${(cov || []).length} 台设备有记录，其中 <b>${okCount}</b> 台安装成功`;

    const verView = view;
    verView.innerHTML = `
      <div class="card">
        <h3>版本时间线</h3>
        ${(vers || []).map(v => `
        <div class="card" style="margin-bottom:10px">
          <div class="form-row">
            <b>v${App.esc(v.version)}</b> ${statusBadge(v.status)}
            <span class="badge ${v.source === 'evolved' ? 'blue' : 'gray'}">${App.esc(v.source || '')}</span>
            ${v.signature ? '<span class="badge green">已签名</span>' : ''}
            <span class="muted">${App.esc((v.created_at || '').Valid ? '' : '')}${''}</span>
          </div>
          ${v.rationale ? `<p class="muted">rationale：${App.esc(v.rationale)}</p>` : ''}
          <div class="form-row">
            <button class="small" data-act="diff" data-v="${App.esc(v.version)}">审阅差异</button>
            ${App.canWrite() && CHAIN.indexOf(v.status) >= 0 && CHAIN.indexOf(v.status) < CHAIN.length - 1 && v.status !== 'stable'
              ? `<button class="small primary" data-act="promote" data-v="${App.esc(v.version)}" data-next="${CHAIN[CHAIN.indexOf(v.status) + 1]}">晋升 ${CHAIN[CHAIN.indexOf(v.status) + 1]}</button>` : ''}
            ${App.canWrite() && v.status === 'stable' ? `<button class="small" data-act="install" data-v="${App.esc(v.version)}">安装下发…</button>` : ''}
            ${App.canWrite() && (v.status === 'candidate' || v.status === 'reviewed') ? `<button class="small" data-act="evaluate" data-v="${App.esc(v.version)}">灰度评估…</button>` : ''}
            <button class="small" data-act="runs" data-v="${App.esc(v.version)}">评估历史</button>
          </div>
          <div id="detail-${App.esc(v.version)}"></div>
        </div>`).join('') || '<span class="muted">无版本</span>'}
      </div>
      <div class="card">
        <h3>自动发布策略</h3>
        <div class="form-row"><span>当前：${policy && policy.enabled ? `<span class="badge green">启用</span> 达标线 ${policy.min_pass_rate} × 连续 ${policy.min_runs} 次，目标根 ${App.esc(policy.target_root || '未设')}` : '<span class="badge gray">关闭</span>'}</span>
        <button class="small" id="policy-show-details">查看/修改（CLI 亦可）</button></div>
        <div id="policy-detail" class="muted"></div>
      </div>`;

    document.getElementById('policy-show-details').onclick = () => {
      document.getElementById('policy-detail').innerHTML = `<pre>${App.esc(JSON.stringify(policy, null, 2))}</pre>
      <p class="muted">修改：gs-cloud policy --name ${App.esc(name)} --enable --min-pass-rate R --min-runs N --target-root R [--label L]</p>`;
    };
    } catch (e) {
      verView.innerHTML = `<div class="card"><span class="err">加载失败：${App.esc(e.message)}</span> <button class="small" onclick="App.go('skills')">重试</button></div>`;
    }

    verView.querySelectorAll('button[data-act]').forEach(b => {
      b.onclick = async () => {
        const v = b.dataset.v;
        if (b.dataset.act === 'diff') return showDiff(name, v, v);
        if (b.dataset.act === 'promote') {
          const next = b.dataset.next;
          if (!confirm(`将 ${name} v${v} 晋升为 ${next}？${next === 'stable' ? '（签名并允许安装）' : ''}`)) return;
          try { await API.post(`${API_}/skills/${name}/versions/${v}/status`, { status: next }); App.toast(`已晋升 ${next}`); loadVersions(name); } catch (e) { App.toast(e.message, true); }
        }
        if (b.dataset.act === 'install') return installDialog(name, v);
        if (b.dataset.act === 'evaluate') return evaluateDialog(name, v, view);
        if (b.dataset.act === 'runs') {
          const runs = await API.get(`${API_}/skills/${name}/versions/${v}/runs`);
          document.getElementById('detail-' + v).innerHTML = (runs || []).map(r => `<div>
            ${r.status === 'passed' ? '<span class="badge green">passed</span>' : r.status === 'running' ? '<span class="badge blue">running</span>' : '<span class="badge red">' + App.esc(r.status) + '</span>'}
            通过 ${(r.metrics || {}).passed ?? '-'}/${(r.metrics || {}).total ?? '-'} · ${App.esc((r.created_at || '').Valid ? r.created_at.Time : r.created_at || '')}</div>`).join('') || '<span class="muted">无评估记录</span>';
        }
      };
    });
  }

  function showDiff(name, v) {
    API.get(`${API_}/skills/${name}/versions/${v}/diff`).then(d => {
      const newSet = new Set((d.current_skill_md || '').split('\n'));
      const html = (d.current_skill_md || '').split('\n')
        .map(l => newSet.has(l) ? `  ${App.esc(l)}` : `<div class="diff-add">+ ${App.esc(l)}</div>`).join('\n')
        + '\n' + (d.parent_skill_md || '').split('\n').filter(l => !newSet.has(l))
          .map(l => `<div class="diff-del">- ${App.esc(l)}</div>`).join('\n');
      document.getElementById('detail-' + v).innerHTML = `
        <p class="muted">rationale：${App.esc(d.rationale || '（无）')}</p>
        <pre>${html}</pre>`;
    }).catch(e => App.toast(e.message, true));
  }

  function installDialog(name, v) {
    const target = prompt(`安装 ${name} v${v}：目标根目录（skill 装到 <根>/<名称>，须在设备白名单内）\n例：/home/user/.claude/skills`);
    if (!target) return;
    API.get(`${API_}/devices`).then(devices => {
      const usable = devices.filter(d => d.status !== 'revoked');
      const ids = usable.map(d => d.id).filter(id => confirm(`安装到 ${usable.find(x => x.id === id).hostname}？`));
      if (!ids.length) { App.toast('未选择设备'); return; }
      API.post(`${API_}/skills/${name}/install`, { version: v, target_root: target, device_ids: ids })
        .then(out => App.toast(`已下发 ${out.length} 台设备，回执见「设备」详情的指令历史`))
        .catch(e => App.toast(e.message, true));
    });
  }

  function evaluateDialog(name, v, view) {
    Promise.all([API.get(`${API_}/benchmarks`), API.get(`${API_}/devices`)]).then(([bms, devices]) => {
      const bmID = prompt(`评估 ${name} v${v}\n基准集 id（进化页可查）：`);
      if (!bmID) return;
      const dev = prompt('灰度设备 id：');
      if (!dev) return;
      const root = prompt('灰度安装目标根（直接回车=不安装只跑任务）：');
      const body = { version: v, benchmark_set_id: bmID, device_ids: [dev], agent_type: 'gsagent' };
      if (root) { body.install = true; body.target_root = root; }
      API.post(`${API_}/skills/${name}/evaluate`, body)
        .then(run => { App.toast(`评估完成：${run.verdict}`); loadVersions(name); })
        .catch(e => App.toast(e.message, true));
    });
  }

  App.register('skills', 'Skill', render);
})();
