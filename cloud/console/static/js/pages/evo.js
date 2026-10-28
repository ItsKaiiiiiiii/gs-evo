// evo.js：进化页（降级为全局队列）——跨实体对象：BenchmarkSet + 全局 EvaluationRun。
// 生成/评估/策略等动作已下沉到「Skill」实体页（评审裂伤一修复）。
(function () {
  const API_ = '/api/v1/admin';
  async function render(c) {
    let bms = [], runs = [];
    try { bms = await API.get(`${API_}/benchmarks`); } catch (e) { /* viewer 降级 */ }
    try { runs = await API.get(`${API_}/evaluations?limit=50`); } catch (e) { runs = []; }

    c.innerHTML = `<div class="page-title">进化（全局队列）</div>
    <p class="muted" style="margin-bottom:12px">生成/评估/策略等动作已收进各 Skill 的详情页；本页是跨实体对象的全局视图。</p>
    <div class="card">
      <h3>BenchmarkSet</h3>
      ${(bms || []).length ? `<table><tr><th>名称</th><th>版本</th><th>用例数</th><th>id</th><th>创建</th></tr>
        ${(bms || []).map(b => `<tr><td><b>${App.esc(b.name)}</b></td><td>v${b.version}</td>
          <td>${(b.cases || []).length}</td><td title="${App.esc(b.id)}">${App.esc(b.id.slice(0, 13))}…</td>
          <td>${App.fmtTime(b.created_at && b.created_at.Valid ? b.created_at.Time : b.created_at)}</td></tr>`).join('')}
      </table>
      <p class="muted">新建/迭代：gs-cloud benchmark create --name N --cases-file F.json（用例 JSON：[{name,prompt,expect_contains,timeout_s}]）</p>`
      : '<span class="muted">暂无基准集（gs-cloud benchmark create 创建）</span>'}
    </div>
    <div class="card"><h3>最近的 EvaluationRun</h3>
      ${(runs || []).length ? `<table><tr><th>时间</th><th>Skill</th><th>版本</th><th>结果</th><th>verdict</th><th>通过率</th></tr>
      ${(runs || []).map(r => `<tr>
        <td>${App.fmtTime(r.created_at)}</td>
        <td><b>${App.esc(r.skill_name)}</b></td><td>v${App.esc(r.skill_version)}</td>
        <td>${r.status === 'passed' ? '<span class="badge green">passed</span>' : r.status === 'running' ? '<span class="badge blue">running</span>' : '<span class="badge red">' + App.esc(r.status) + '</span>'}</td>
        <td>${App.esc(r.verdict || '-')}</td>
        <td>${(r.metrics || {}).pass_rate ?? '-'}</td></tr>`).join('')}
      </table>` : '<span class="muted">暂无评估</span>'}
    </div>`;
  }

  App.register('evo', '进化', render);
})();
