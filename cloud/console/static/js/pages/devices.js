// devices.js：设备页——列表/标签/吊销/断连 + enroll token 签发。
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
      <div class="card"><h3>设备列表</h3><div id="dev-table">加载中…</div></div>`;

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

    async function refresh() {
      const devs = await API.get(`${API_}/devices`);
      const rows = devs.map(d => `<tr>
        <td title="${App.esc(d.id)}">${App.esc(d.id.slice(0, 13))}…</td>
        <td>${App.esc(d.hostname)}</td>
        <td>${App.esc(d.os)}</td>
        <td>${badge(d.status)}</td>
        <td>${(d.labels || []).map(l => `<span class="badge blue">${App.esc(l)}</span>`).join(' ')}</td>
        <td>${App.esc(d.last_seen || '-')}</td>
        <td>
          ${App.canWrite() ? `
          <button class="small" data-act="disconnect" data-id="${d.id}">断连</button>
          <button class="small danger" data-act="revoke" data-id="${d.id}" ${d.status === 'revoked' ? 'disabled' : ''}>吊销</button>` : ''}
        </td>
      </tr>`).join('');
      document.getElementById('dev-table').innerHTML = `<table>
        <tr><th>ID</th><th>主机名</th><th>OS</th><th>状态</th><th>标签</th><th>最后在线</th><th>操作</th></tr>
        ${rows || '<tr><td colspan="7" class="muted">暂无设备（用 gs-worker login 注册）</td></tr>'}
      </table>`;
      document.querySelectorAll('#dev-table button[data-act]').forEach(b => {
        b.onclick = async () => {
          if (!confirm(`${b.dataset.act === 'revoke' ? '吊销' : '断连'}设备 ${b.dataset.id.slice(0, 13)}…？`)) return;
          try {
            await API.post(`${API_}/devices/${b.dataset.id}/${b.dataset.act}`);
            App.toast(b.dataset.act === 'revoke' ? '已吊销（设备被踢且无法重连）' : '已断连');
            refresh();
          } catch (e) { App.toast(e.message, true); }
        };
      });
    }
    await refresh();
  }

  App.register('devices', '设备', render);
})();
