// evo.js：进化页——基准集、生成候选、评估、自动发布策略。
(function () {
  const API_ = '/api/v1/admin';

  function badge(v) {
    const map = { passed: 'green', failed: 'red', running: 'blue', error: 'red' };
    return `<span class="badge ${map[v] || 'gray'}">${App.esc(v || '-')}</span>`;
  }

  async function render(c) {
    const skills = await API.get(`${API_}/skills`);
    const bms = await API.get(`${API_}/benchmarks`);
    const devices = await API.get(`${API_}/devices`);
    const usable = devices.filter(d => d.status !== 'revoked');

    c.innerHTML = `<div class="page-title">进化闭环</div>
    <div class="grid2">
      <div class="card">
        <h3>① 基准集</h3>
        <div id="bm-list">${(bms || []).map(b => `<div>📦 ${App.esc(b.name)} v${b.version}（${(b.cases || []).length} 用例）<span class="muted"> id=${App.esc(b.id)}</span></div>`).join('') || '<span class="muted">暂无（用 CLI benchmark create 创建）</span>'}</div>
      </div>
      <div class="card">
        <h3>② 生成候选（evolve）</h3>
        ${App.canWrite() ? `
        <div class="form-row"><label>skill</label>
          <select id="e-skill">${skills.map(s => `<option>${App.esc(s.name)}</option>`).join('')}</select></div>
        <div class="form-row"><label>改进要求</label>
          <input id="e-instruction" style="flex:1" placeholder="如：补充错误处理说明"></div>
        <button id="e-evolve" class="primary">生成候选</button>
        <div id="e-out" class="muted" style="margin-top:8px"></div>` : '<span class="muted">viewer 只读</span>'}
      </div>
      <div class="card">
        <h3>③ 评估（灰度安装 + 跑基准）</h3>
        ${App.canWrite() ? `
        <div class="form-row"><label>skill</label><input id="v-skill" placeholder="skill 名"></div>
        <div class="form-row"><label>版本</label><input id="v-ver" placeholder="候选版本号" style="width:100px">
          <label>基准集</label><select id="v-bm">${(bms || []).map(b => `<option value="${b.id}">${App.esc(b.name)} v${b.version}</option>`).join('')}</select></div>
        <div class="form-row"><label>灰度设备</label>
          <select id="v-dev">${usable.map(d => `<option value="${d.id}">${App.esc(d.hostname)} [${(d.labels || []).join(',')}]</option>`).join('')}</select>
          <label style="min-width:auto"><input type="checkbox" id="v-install" checked> 先安装候选</label></div>
        <div class="form-row"><label>目标根</label><input id="v-root" style="flex:1" placeholder="设备白名单根目录（install 时必填）"></div>
        <div class="form-row"><label>agent</label>
          <select id="v-agent"><option value="gsagent">gsagent</option><option value="claude-code">claude-code</option><option value="">设备默认</option></select>
          <button id="v-run" class="primary">开始评估</button></div>
        <div id="v-out" class="muted" style="margin-top:8px"></div>` : '<span class="muted">viewer 只读</span>'}
      </div>
      <div class="card">
        <h3>④ 自动发布策略</h3>
        ${App.canWrite() ? `
        <div class="form-row"><label>skill</label><input id="p-skill" placeholder="skill 名"></div>
        <div class="form-row"><label>达标线</label>
          <input id="p-rate" type="number" step="0.1" min="0" max="1" value="1.0" style="width:80px">
          连续 <input id="p-runs" type="number" min="1" value="2" style="width:60px"> 次评估通过</div>
        <div class="form-row"><label>目标根</label><input id="p-root" style="flex:1" placeholder="<根>/<skill>（必填）"></div>
        <div class="form-row"><button id="p-enable" class="primary">启用自动发布</button>
          <button id="p-disable">停用</button></div>
        <div id="p-out" class="muted" style="margin-top:8px"></div>` : '<span class="muted">viewer 只读</span>'}
      </div>
    </div>
    <div class="card"><h3>EvaluationRun 结果</h3>
      <div class="form-row"><label>skill / 版本</label>
        <input id="r-skill" placeholder="skill 名"><input id="r-ver" placeholder="版本" style="width:100px">
        <button class="small" id="r-query">查询</button></div>
      <div id="r-out">输入 skill 与版本查询评估历史</div>
    </div>`;

    const skillOf = () => {
      const a = document.getElementById('e-skill'), b = document.getElementById('v-skill'), c2 = document.getElementById('p-skill');
      return a && a.value || b && b.value || c2 && c2.value || '';
    };

    if (App.canWrite()) {
      const btn = id => document.getElementById(id);
      if (btn('e-evolve')) btn('e-evolve').onclick = async () => {
        const name = btn('e-skill').value, ins = btn('e-instruction').value.trim();
        if (!ins) { App.toast('填写改进要求', true); return; }
        btn('e-evolve').disabled = true;
        btn('e-evolve').textContent = '生成中…';
        try {
          const v = await API.post(`${API_}/skills/${name}/evolve`, { instruction: ins });
          btn('e-out').textContent = `候选已生成：v${v.version}（candidate）rationale: ${v.rationale || ''}`;
          App.toast(`候选 v${v.version} 已生成，去 Skill 页审阅 diff`);
        } catch (e) { btn('e-out').textContent = e.message; App.toast(e.message, true); }
        btn('e-evolve').disabled = false;
        btn('e-evolve').textContent = '生成候选';
      };

      if (btn('v-run')) btn('v-run').onclick = async () => {
        const body = {
          version: btn('v-ver').value.trim(), benchmark_set_id: btn('v-bm').value,
          device_ids: [btn('v-dev').value], install: btn('v-install').checked,
          target_root: btn('v-root').value.trim(), agent_type: btn('v-agent').value,
        };
        if (!body.version || !body.benchmark_set_id) { App.toast('填版本与基准集', true); return; }
        if (body.install && !body.target_root) { App.toast('install 需要 target_root', true); return; }
        btn('v-run').disabled = true;
        btn('v-run').textContent = '评估中…';
        try {
          const run = await API.post(`${API_}/skills/${skillOf() || btn('v-skill').value}/evaluate`.replace('//', '/'), body);
          btn('v-out').innerHTML = `verdict: ${badge(run.verdict)} | 通过 ${(run.metrics || {}).passed}/${(run.metrics || {}).total}（pass_rate ${(run.metrics || {}).pass_rate}）<br>
            per_case: ${(run.metrics && run.metrics.per_case || []).map(pc => `${App.esc(pc.case)}=${pc.pass ? '✅' : '❌'}`).join(' ')}`;
          App.toast('评估完成');
        } catch (e) { btn('v-out').textContent = e.message; App.toast(e.message, true); }
        btn('v-run').disabled = false;
        btn('v-run').textContent = '开始评估';
      };

      if (btn('p-enable')) btn('p-enable').onclick = async () => {
        const name = btn('p-skill').value.trim();
        if (!name) { App.toast('填 skill 名', true); return; }
        try {
          const r = await API.post(`${API_}/skills/${name}/auto-policy`, {
            enabled: true,
            min_pass_rate: parseFloat(btn('p-rate').value) || 1.0,
            min_runs: parseInt(btn('p-runs').value) || 2,
            target_root: btn('p-root').value.trim(),
          });
          btn('p-out').innerHTML = `已启用 ✅（达标线 ${r.min_pass_rate} × 连续 ${r.min_runs} 次）`;
          App.toast('自动发布策略已启用（达标自动转 stable 并下发）');
        } catch (e) { btn('p-out').textContent = e.message; App.toast(e.message, true); }
      };
      if (btn('p-disable')) btn('p-disable').onclick = async () => {
        const name = btn('p-skill').value.trim();
        if (!name) { App.toast('填 skill 名', true); return; }
        try {
          await API.post(`${API_}/skills/${name}/auto-policy`, { enabled: false });
          btn('p-out').textContent = '已停用';
        } catch (e) { btn('p-out').textContent = e.message; }
      };

      if (btn('r-query')) btn('r-query').onclick = async () => {
        const name = btn('r-skill').value.trim(), ver = btn('r-ver').value.trim();
        if (!name || !ver) { App.toast('填 skill 与版本', true); return; }
        const runs = await API.get(`${API_}/skills/${name}/versions/${ver}/runs`);
        document.getElementById('r-out').innerHTML = (runs || []).map(r => `<div>
          ${badge(r.status)} verdict=${App.esc(r.verdict || '-')} | 通过 ${(r.metrics || {}).passed}/${(r.metrics || {}).total} | ${App.esc((r.created_at || '').replace('T', ' ').slice(0, 19))}</div>`).join('') || '<span class="muted">无评估记录</span>';
      };
    }
  }

  App.register('evo', '进化', render);
})();
