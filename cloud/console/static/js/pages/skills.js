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
    c.innerHTML = `<div class="page-title">Skill</div>
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

    const skills = await API.get(`${API_}/skills`);
    const sel = document.getElementById('skill-name');
    sel.innerHTML = skills.map(s => `<option>${App.esc(s.name)}</option>`).join('') || '<option disabled>暂无 skill（在「任务与操作」页点「扫描/获取」收集）</option>';
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

    sel.onchange = () => loadVersions(sel.value);
    if (skills.length) await loadVersions(sel.value);
    else document.getElementById('ver-view').innerHTML = '<div class="card"><span class="muted">暂无 skill。去「任务与操作」页点「扫描 skill / 获取全部 skill」收集第一份。</span></div>';
  }

  async function loadVersions(name) {
    const view = document.getElementById('ver-view');
    view.innerHTML = '<div class="card">加载中…</div>';
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
