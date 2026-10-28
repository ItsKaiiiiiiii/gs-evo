// report.js：总览（着陆页）——回答"系统健康吗"，并承载数据之外的运营入口。
(function () {
  const API_ = '/api/v1/admin';
  async function render(c) {
    c.innerHTML = `<div class="page-title">总览</div><div class="card" id="rep">加载中…</div>
    <div class="card" id="upd-card">
      <h3>客户端升级（worker 自更新）</h3>
      <p class="muted">发布二进制请用 CLI：gs-cloud client-update push --version V --file F --os GOOS --arch GOARCH（按平台三元组登记）。下方按标签/版本向设备下发升级指令，云端自动匹配各设备平台的 release。</p>
      ${App.canWrite() ? `
      <div class="form-row">
        <label>版本</label><input id="u-version" placeholder="如 v0.2.0" style="width:120px">
        <label>标签圈定</label><input id="u-label" placeholder="如 env:all（留空=全网）" style="width:180px">
        <button id="u-rollout" class="primary">下发升级</button>
      </div>
      <div id="u-out" class="muted"></div>` : '<span class="muted">viewer 只读</span>'}
    </div>`;
    const rep = await API.get(`${API_}/report/summary`).catch(e => ({ error: e.message }));
    if (rep.error) { document.getElementById('rep').textContent = rep.error; return; }
    const d = rep.devices || {}, sk = rep.skills || {}, cm = rep.commands || {};
    document.getElementById('rep').innerHTML = `
      <p class="muted">生成于 ${App.fmtTime(rep.generated_at)}</p>
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

    if (App.canWrite()) {
      document.getElementById('u-rollout').onclick = async () => {
        const version = document.getElementById('u-version').value.trim();
        if (!version) { App.toast('填版本号', true); return; }
        const body = { version, labels: [] };
        const label = document.getElementById('u-label').value.trim();
        if (label) body.labels = [label];
        try {
          const out = await API.post(`${API_}/client-update/rollout`, body);
          const sent = out.filter(x => !x.skipped);
          const skipped = out.filter(x => x.skipped);
          App.toast(`已下发 ${sent.length} 台${skipped.length ? `，跳过 ${skipped.length} 台（无匹配平台 release）` : ''}`);
          document.getElementById('u-out').innerHTML = out.map(x => `<div>${App.esc(x.device_id.slice(0, 13))}… ${x.os}/${x.arch} ${x.skipped ? '⚠️ ' + App.esc(x.skipped) : '✅'}</div>`).join('');
        } catch (e) { App.toast(e.message, true); }
      };
    }
  }

  App.register('report', '总览', render);
})();
