// skills.js：Skill 页——版本列表、状态机流转、diff 审阅、安装下发。
(function () {
  const API_ = '/api/v1/admin';
  const CHAIN = ['draft', 'candidate', 'reviewed', 'stable'];

  function statusBadge(s) {
    const map = { draft: 'gray', candidate: 'yellow', reviewed: 'blue', stable: 'green', deprecated: 'red' };
    return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
  }

  async function render(c) {
    c.innerHTML = `<div class="page-title">Skill</div>
      <div class="card"><h3>版本与状态机</h3><div id="skill-sel">加载中…</div></div>
      <div id="skill-detail"></div>`;

    const skills = await API.get(`${API_}/skills`);
    const sel = document.getElementById('skill-sel');
    if (!skills.length) {
      sel.innerHTML = '<span class="muted">暂无 skill（先用 gs-cloud cmd send --type skills.collect 收集）</span>';
      return;
    }
    sel.innerHTML = `<div class="form-row"><label>skill</label>
      <select id="skill-name">${skills.map(s => `<option>${App.esc(s.name)}</option>`).join('')}</select>
      <button id="skill-refresh" class="small">刷新</button></div><div id="ver-table"></div>`;

    async function loadVersions() {
      const name = document.getElementById('skill-name').value;
      const vers = await API.get(`${API_}/skills/${name}/versions`);
      const rows = vers.map(v => {
        const idx = CHAIN.indexOf(v.status);
        const next = (idx >= 0 && idx < CHAIN.length - 1) ? CHAIN[idx + 1] : null;
        return `<tr>
          <td>v${App.esc(v.version)} ${v.version === (vers[0] && vers[0].version) ? '' : ''}</td>
          <td>${statusBadge(v.status)}</td>
          <td>${App.esc(v.source || '')}</td>
          <td title="${App.esc(v.content_sha256)}">${App.esc((v.content_sha256 || '').slice(0, 10))}…</td>
          <td title="${App.esc(v.rationale || '')}">${App.esc((v.rationale || '').slice(0, 40))}</td>
          <td>
            <button class="small" data-act="diff" data-v="${v.version}">审阅</button>
            ${App.canWrite() && next && v.status !== 'stable' ? `<button class="small primary" data-act="promote" data-v="${v.version}" data-next="${next}">晋升 ${next}</button>` : ''}
            ${App.canWrite() && v.status === 'stable' ? `<button class="small" data-act="install" data-v="${v.version}">安装…</button>` : ''}
          </td>
        </tr>`;
      }).join('');
      document.getElementById('ver-table').innerHTML = `<table>
        <tr><th>版本</th><th>状态</th><th>来源</th><th>内容</th><th>rationale</th><th>操作</th></tr>
        ${rows || '<tr><td colspan="6" class="muted">无版本</td></tr>'}
      </table>`;

      document.querySelectorAll('#ver-table button').forEach(b => {
        b.onclick = async () => {
          const name = document.getElementById('skill-name').value;
          const v = b.dataset.v;
          if (b.dataset.act === 'diff') return showDiff(name, v);
          if (b.dataset.act === 'promote') {
            if (!confirm(`将 ${name} v${v} 晋升为 ${b.dataset.next}？${b.dataset.next === 'stable' ? '（转 stable 会签名并允许安装）' : ''}`)) return;
            try {
              await API.post(`${API_}/skills/${name}/versions/${v}/status`, { status: b.dataset.next });
              App.toast(`已晋升为 ${b.dataset.next}`);
              loadVersions();
            } catch (e) { App.toast(e.message, true); }
          }
          if (b.dataset.act === 'install') return installDialog(name, v);
        };
      });
    }
    document.getElementById('skill-refresh').onclick = loadVersions;
    document.getElementById('skill-name').onchange = loadVersions;
    await loadVersions();
  }

  // diff 审阅：rationale + 行级差异
  async function showDiff(name, v) {
    const d = await API.get(`${API_}/skills/${name}/versions/${v}/diff`);
    const oldLines = (d.parent_skill_md || '').split('\n');
    const newLines = (d.current_skill_md || '').split('\n');
    const newSet = new Set(newLines);
    const html = newLines.map(l => newSet.has(l)
      ? `  ${App.esc(l)}`
      : `<div class="diff-add">+ ${App.esc(l)}</div>`).join('\n') +
      '\n' + oldLines.filter(l => !newSet.has(l))
        .map(l => `<div class="diff-del">- ${App.esc(l)}</div>`).join('\n');
    document.getElementById('skill-detail').innerHTML = `<div class="card">
      <h3>审阅 v${App.esc(d.parent_version || '?')} → v${App.esc(v)}</h3>
      <p class="muted">rationale：${App.esc(d.rationale || '（无）')}</p>
      <pre>${html}</pre></div>`;
    document.getElementById('skill-detail').scrollIntoView();
  }

  // 安装对话框：目标根 + 设备/标签圈定
  async function installDialog(name, v) {
    const target = prompt(`安装 ${name} v${v}：输入目标根目录（skill 将装到 <根>/<名称>，须在设备白名单内）\n例：/home/user/.claude/skills`);
    if (!target) return;
    const devices = await API.get(`${API_}/devices`);
    const usable = devices.filter(d => d.status !== 'revoked');
    const ids = usable.map(d => d.id).filter(id => confirm(`安装到设备 ${id.slice(0, 13)}…（${usable.find(x => x.id === id).hostname}）？`));
    if (!ids.length) { App.toast('未选择任何设备'); return; }
    try {
      const out = await API.post(`${API_}/skills/${name}/install`,
        { version: v, target_root: target, device_ids: ids });
      App.toast(`已下发 ${out.length} 台设备，回执见指令审计（或用 CLI skill install --wait）`);
      App.go('tasks'); // 引导到任务/指令视图看回执
    } catch (e) { App.toast(e.message, true); }
  }

  App.register('skills', 'Skill', render);
})();
