// devices.js：设备实体页——列表 + 详情（装机清单 / 指令历史 / 任务历史）。
// 评审问题 5：「机器 X 上装了哪些 skill、什么版本」→ 详情页 2 跳内回答。
(function () {
  const API_ = '/api/v1/admin';

  function badge(status) {
    const map = { online: 'green', offline: 'gray', revoked: 'red' };
    return `<span class="badge ${map[status] || 'gray'}">${App.esc(status)}</span>`;
  }

  async function render(c) {
    c.innerHTML = `<div class="page-title">设备</div>
      <div class="card">
        <h3>签发 enroll token（一次性，24h；label 会成为设备标签）</h3>
        ${App.canWrite() ? `
        <div class="form-row">
          <input id="enroll-label" placeholder="label，如 env:machineA" style="flex:1">
          <button id="enroll-create" class="primary">签发 token</button>
        </div>
        <pre id="enroll-out" class="hidden"></pre>` : '<span class="muted">viewer 只读</span>'}
      </div>
      <div id="dev-view"><div class="card"><div id="dev-table">加载中…</div></div></div>`;

    if (App.canWrite()) {
      document.getElementById('enroll-create').onclick = async () => {
        try {
          const r = await API.post(`${API_}/enroll-tokens`, { label: document.getElementById('enroll-label').value });
          const out = document.getElementById('enroll-out');
          out.classList.remove('hidden');
          out.textContent = 'token（仅显示一次，24h 有效）：\n' + r.token;
          App.toast('enroll token 已签发');
        } catch (e) { App.toast(e.message, true); }
      };
    }

    async function list() {
      const devs = await API.get(`${API_}/devices`);
      const rows = devs.map(d => `<tr style="cursor:pointer" data-id="${d.id}">
        <td title="${App.esc(d.id)}">${App.esc(d.id.slice(0, 13))}…</td>
        <td><b>${App.esc(d.hostname)}</b></td>
        <td>${App.esc(d.os)}/${App.esc(d.arch)}</td>
        <td>${App.esc(d.worker_version || '-')}</td>
        <td>${badge(d.status)}</td>
        <td>${(d.labels || []).map(l => `<span class="badge blue">${App.esc(l)}</span>`).join(' ')}</td>
        <td>${App.esc(d.last_seen || '-')}</td>
      </tr>`).join('');
      document.getElementById('dev-table').innerHTML = `<table>
        <tr><th>ID</th><th>主机名</th><th>平台</th><th>worker</th><th>状态</th><th>标签</th><th>最后在线</th></tr>
        ${rows || '<tr><td colspan="7" class="muted">暂无设备（用 gs-worker login 注册）</td></tr>'}
      </table>`;
      document.querySelectorAll('#dev-table tr[data-id]').forEach(tr => {
        tr.onclick = () => detail(tr.dataset.id);
      });
    }

    let runtimeNotice = ''; // 运行时登记结果（跨重渲染保留）

    // ---- 设备详情：装机清单 / 指令历史 / 任务历史 ----
    async function detail(id) {
      const view = document.getElementById('dev-view');
      view.innerHTML = '<div class="card"><button class="small" id="dev-back">← 返回列表</button> 加载中…</div>';
      document.getElementById('dev-back').onclick = () => render(view.parentElement || view);

      const [devs, installs, runtimes, cmds, tasks] = await Promise.all([
        API.get(`${API_}/devices`),
        API.get(`${API_}/devices/${id}/installs`).catch(() => []),
        API.get(`${API_}/devices/${id}/runtimes`).catch(() => []),
        API.get(`${API_}/commands?device_id=${id}&limit=50`).catch(() => null),
        API.get(`${API_}/tasks?device_id=${id}&limit=50`).catch(() => null),
      ]);
      const d = devs.find(x => x.id === id) || {};

      const instRows = (installs || []).map(i => `<tr>
        <td><b>${App.esc(i.skill)}</b></td><td>v${App.esc(i.version)}</td>
        <td>${i.result === 'ok' ? '<span class="badge green">已安装</span>' : i.result === 'conflict_skipped' ? '<span class="badge yellow">跳过</span>' : '<span class="badge red">' + App.esc(i.result) + '</span>'}
            ${i.signed ? '<span class="badge blue">已签名</span>' : ''}</td>
        <td>${App.esc((i.installed_at || '').replace('T', ' ').slice(0, 19))}</td></tr>`).join('') ||
        '<tr><td colspan="4" class="muted">无装机记录</td></tr>';

      const cmdRows = (cmds === null)
        ? '<tr><td colspan="5" class="muted">无权限查看指令历史（需要 admin token）</td></tr>'
        : (cmds || []).map(cmd => `<tr>
        <td>${App.esc((cmd.issued_at || '').replace('T', ' ').slice(0, 19))}</td>
        <td>${App.esc(cmd.cmd_type)}</td>
        <td>${cmd.status === 'acked' ? (cmd.result && cmd.result.ok === false ? '<span class="badge red">失败</span>' : '<span class="badge green">成功</span>') : statusCmd(cmd.status)}</td>
        <td>${App.esc(cmd.created_by || '-')}</td>
        <td title="${App.esc((cmd.result && cmd.result.error) || '')}">${App.esc((cmd.result && (cmd.result.error || '')) ? String(cmd.result.error).slice(0, 60) : '-')}</td></tr>`).join('') ||
        '<tr><td colspan="5" class="muted">无指令</td></tr>';

      const taskRows = (tasks === null)
        ? '<tr><td colspan="4" class="muted">无权限查看任务历史（需要 admin token）</td></tr>'
        : (tasks || []).map(t => `<tr>
        <td title="${App.esc(t.id)}">${App.esc(t.id.slice(0, 13))}…</td>
        <td title="${App.esc(t.prompt)}">${App.esc(t.prompt.slice(0, 30))}</td>
        <td>${statusBadge(t.status)}</td>
        <td>${App.esc((t.created_at || '').replace('T', ' ').slice(0, 19))}</td></tr>`).join('') ||
        '<tr><td colspan="4" class="muted">无任务</td></tr>';

      view.innerHTML = `
      <div class="card">
        <button class="small" id="dev-back">← 返回列表</button>
        <h3 style="margin-top:10px">${App.esc(d.hostname || id)} ${badge(d.status || '')}</h3>
        <p class="muted">${App.esc(d.os)}/${App.esc(d.arch)} · worker ${App.esc(d.worker_version || '-')} · 最后在线 ${App.esc(d.last_seen || '-')}</p>
        <div class="form-row">
          ${App.canWrite() ? `
          <button class="small" id="dev-disconnect">断连</button>
          <button class="small danger" id="dev-revoke" ${d.status === 'revoked' ? 'disabled' : ''}>吊销</button>
          <input id="dev-labels" value="${App.esc((d.labels || []).join(','))}" placeholder="标签，逗号分隔" style="width:240px">
          <button class="small" id="dev-labels-save">保存标签</button>` : ''}
        </div>
      </div>
      <div class="card"><h3>运行时（本机 agent 实例）</h3>
        ${App.canWrite() ? `
        <div class="form-row">
          <button class="small primary" id="rt-add">添加运行时（绑定本机）</button>
          <span class="muted">云端下发 runtime.register 指令，本机校验二进制并探测版本后登记</span>
        </div>
        <div id="rt-form" class="hidden">
          <div class="form-row">
            <label>类型</label>
            <select id="rt-type"><option value="gsagent">gsagent（自定义二进制，gsagent 协议）</option><option value="claude-code">claude-code</option></select>
            <label>路径</label><input id="rt-path" style="flex:1" placeholder="设备上的二进制绝对路径，如 /usr/local/bin/my-agent">
          </div>
          <div class="form-row">
            <label>参数</label><input id="rt-args" style="flex:1" placeholder="启动参数，逗号分隔（可选）">
            <label>环境</label><input id="rt-env" style="flex:1" placeholder="K=V，逗号分隔（可选）">
            <button class="small primary" id="rt-create">创建</button>
          </div>
          <div id="rt-out" class="muted"></div>
        </div>` : ''}
        <table><tr><th>运行时 ID</th><th>类型</th><th>版本</th><th>状态</th><th>来源</th><th>路径</th></tr>
        ${(runtimes || []).map(r => `<tr>
          <td title="${App.esc(r.runtime_id)}">${App.esc(r.runtime_id.slice(0, 15))}…</td>
          <td><b>${App.esc(r.type)}</b></td><td title="${App.esc(r.version || '')}">${App.esc((r.version || '-').length > 24 ? (r.version || '').slice(0, 24) + '…' : (r.version || '-'))}</td>
          <td>${r.ok ? '<span class="badge green">可用</span>' : '<span class="badge red">不可用</span>'}</td>
          <td>${App.esc(r.source)}</td><td title="${App.esc(r.path || '')}">${App.esc((r.path || '-').slice(0, 40))}</td></tr>`).join('') || '<tr><td colspan="6" class="muted">未上报运行时</td></tr>'}
        </table></div>
      <div class="card"><h3>已装 skill（装机记录）</h3>
        <table><tr><th>Skill</th><th>版本</th><th>结果</th><th>时间</th></tr>${instRows}</table></div>
      <div class="card"><h3>指令历史（最近 50）</h3>
        <table><tr><th>时间</th><th>类型</th><th>结果</th><th>操作者</th><th>错误</th></tr>${cmdRows}</table></div>
      <div class="card"><h3>任务历史</h3>
        <table><tr><th>ID</th><th>Prompt</th><th>状态</th><th>创建时间</th></tr>${taskRows}</table></div>`;

      if (runtimeNotice) {
        const nEl = document.getElementById('rt-out');
        if (nEl) nEl.textContent = runtimeNotice;
      }
      document.getElementById('dev-back').onclick = () => render(view);
      if (App.canWrite()) {
        document.getElementById('dev-disconnect').onclick = async () => {
          if (!confirm('断连该设备？（不吊销，worker 会自动重连）')) return;
          try { await API.post(`${API_}/devices/${id}/disconnect`); App.toast('已断连'); detail(id); } catch (e) { App.toast(e.message, true); }
        };
        document.getElementById('dev-revoke').onclick = async () => {
          if (!confirm('吊销该设备？（立即踢线并拒绝重连，不可逆）')) return;
          try { await API.post(`${API_}/devices/${id}/revoke`); App.toast('已吊销'); detail(id); } catch (e) { App.toast(e.message, true); }
        };
        document.getElementById('dev-labels-save').onclick = async () => {
          const labels = document.getElementById('dev-labels').value.split(',').map(x => x.trim()).filter(Boolean);
          try { await API.post(`${API_}/devices/${id}/labels`, { labels }); App.toast('标签已保存'); detail(id); } catch (e) { App.toast(e.message, true); }
        };
        // 添加运行时（创建即绑定本机）：runtime.register 指令 + 回执等待
        const addBtn = document.getElementById('rt-add');
        if (addBtn) {
          addBtn.onclick = () => document.getElementById('rt-form').classList.toggle('hidden');
          document.getElementById('rt-create').onclick = async () => {
            const type = document.getElementById('rt-type').value;
            const path = document.getElementById('rt-path').value.trim();
            if (!path) { App.toast('填运行时二进制路径', true); return; }
            const args = document.getElementById('rt-args').value.split(',').map(x => x.trim()).filter(Boolean);
            const env = {};
            document.getElementById('rt-env').value.split(',').map(x => x.trim()).filter(Boolean).forEach(kv => {
              const [k, ...rest] = kv.split('='); if (k) env[k] = rest.join('=');
            });
            const outEl = document.getElementById('rt-out');
            outEl.textContent = '下发 runtime.register 指令，等待本机校验…';
            try {
              const r0 = await API.post(`${API_}/commands`, { device_id: id, cmd_type: 'runtime.register',
                payload: { type, path, args, env }, ttl_seconds: 120 });
              const r = await API.pollCmd(r0.cmd_id);
              if (r.ok) {
                runtimeNotice = `已登记：${r.data.runtime_id}（版本探测 ${r.data.ok ? '成功' : '失败'}）`;
                App.toast('运行时已创建并绑定本机');
                detail(id);
              } else {
                runtimeNotice = '登记失败：' + r.error;
                outEl.textContent = runtimeNotice;
                App.toast('登记失败', true);
              }
            } catch (e) { outEl.textContent = e.message; App.toast(e.message, true); }
          };
        }
      }
    }

    function statusCmd(s) {
      const map = { pending: 'gray', sent: 'blue', acked: 'green', expired: 'yellow' };
      return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
    }
    function statusBadge(s) {
      const map = { queued: 'gray', running: 'blue', completed: 'green', failed: 'red', cancelled: 'yellow', timeout: 'red', expired: 'gray' };
      return `<span class="badge ${map[s] || 'gray'}">${App.esc(s)}</span>`;
    }

    await list();
  }

  App.register('devices', '设备', render);
})();
