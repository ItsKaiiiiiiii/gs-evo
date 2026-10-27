// report.js：报表页——一屏总览。
(function () {
  async function render(c) {
    c.innerHTML = `<div class="page-title">报表</div><div class="card" id="rep">加载中…</div>`;
    const rep = await API.get(`${API_}/report/summary`);
    const d = rep.devices || {}, sk = rep.skills || {}, cm = rep.commands || {};
    document.getElementById('rep').innerHTML = `
      <p class="muted">生成于 ${App.esc(rep.generated_at || '')}</p>
      <div class="grid2">
        <div class="card"><h3>设备</h3>
          <table>
            <tr><th>总计</th><td>${d.total ?? 0}</td></tr>
            <tr><th>在线</th><td><span class="badge green">${d.online ?? 0}</span></td></tr>
            <tr><th>已吊销</th><td><span class="badge red">${d.revoked ?? 0}</span></td></tr>
          </table></div>
        <div class="card"><h3>Skill</h3>
          <table>
            <tr><th>总计</th><td>${sk.total ?? 0}</td></tr>
            <tr><th>有 stable 版本</th><td><span class="badge green">${sk.with_stable ?? 0}</span></td></tr>
          </table></div>
      </div>
      <div class="card"><h3>指令</h3>
        <table>
          <tr><th>总计</th><td>${cm.total ?? 0}</td></tr>
          <tr><th>已回执</th><td><span class="badge green">${cm.acked ?? 0}</span></td></tr>
          <tr><th>已过期</th><td><span class="badge yellow">${cm.expired ?? 0}</span></td></tr>
        </table></div>
      <div class="card"><h3>近期失败指令</h3>
        ${(cm.recent_failed || []).length ? `<table>
          <tr><th>设备</th><th>类型</th><th>错误</th></tr>
          ${cm.recent_failed.map(f => `<tr><td>${App.esc((f.device_id || '').slice(0, 13))}…</td><td>${App.esc(f.cmd_type)}</td><td class="err">${App.esc(f.error || '')}</td></tr>`).join('')}
        </table>` : '<span class="muted">无失败记录 🎉</span>'}
      </div>`;
  }

  App.register('report', '报表', render);
})();
