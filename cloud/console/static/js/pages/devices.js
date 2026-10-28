// devices.js：设备页——只回答"系统连接了哪些设备、状态如何"。
// 每台设备可：断连 / 吊销 / 打标签（用户定义的最小管理面）。
// 运行时创建与生命周期 → 「运行时」页；指令审计 → 「审计」页；任务 → 「任务与操作」页。
(function () {
  const API_ = '/api/v1/admin';

  function badge(status) {
    const map = { online: 'green', offline: 'gray', revoked: 'red' };
    return `<span class="badge ${map[status] || 'gray'}">${App.esc(status)}</span>`;
  }

  async function render(c) {
    const devices = await API.get(API_ + '/devices').catch(() => []);
    const rows = (devices || []).map(d => `<tr>
      <td title="${App.esc(d.id)}">${App.esc(d.id.slice(0, 13))}…</td>
      <td><b>${App.esc(d.hostname)}</b></td>
      <td>${App.esc(d.os)}/${App.esc(d.arch)}</td>
      <td>${App.esc(d.worker_version || '-')}</td>
      <td>${badge(d.status)}</td>
      <td>${(d.labels || []).map(l => `<span class="badge blue">${App.esc(l)}</span>`).join(' ')}</td>
      <td>${App.fmtTime(d.last_seen)}</td>
      <td>
        ${App.canWrite() ? `
        <button class="small" data-act="disconnect" data-id="${d.id}" data-host="${App.esc(d.hostname)}">断连</button>
        <button class="small danger" data-act="revoke" data-id="${d.id}" data-host="${App.esc(d.hostname)}" ${d.status === 'revoked' ? 'disabled' : ''}>吊销</button>
        <button class="small" data-act="label" data-id="${d.id}" data-host="${App.esc(d.hostname)}" data-labels="${App.esc((d.labels || []).join(','))}">标签</button>` : ''}
      </td>
    </tr>`).join('');

    c.innerHTML = `<div class="page-title">设备</div>
    <div class="card">
      <table>
        <tr><th>ID</th><th>主机名</th><th>平台</th><th>worker</th><th>状态</th><th>标签</th><th>最后在线</th><th>操作</th></tr>
        ${rows || '<tr><td colspan="8" class="muted">暂无设备——在机器上安装 gs-worker 并执行 gs-worker login --server <云端地址> 即自动接入</td></tr>'}
      </table>
    </div>`;

    c.querySelectorAll('button[data-act]').forEach(b => {
      b.onclick = async () => {
        const id = b.dataset.id, host = b.dataset.host, act = b.dataset.act;
        if (act === 'disconnect') {
          if (!confirm('断连 ' + host + '？（不吊销，worker 会自动重连）')) return;
          try { await API.post(API_ + '/devices/' + id + '/disconnect'); App.toast('已断连'); render(c); } catch (e) { App.toast(e.message, true); }
        }
        if (act === 'revoke') {
          if (!confirm('吊销 ' + host + '？（立即踢线并拒绝重连，不可逆）')) return;
          try { await API.post(API_ + '/devices/' + id + '/revoke'); App.toast('已吊销'); render(c); } catch (e) { App.toast(e.message, true); }
        }
        if (act === 'label') {
          const cur = b.dataset.labels || '';
          const input = prompt('设置 ' + host + ' 的标签（逗号分隔，覆盖式）', cur);
          if (input === null) return;
          const labels = input.split(',').map(x => x.trim()).filter(Boolean);
          try { await API.post(API_ + '/devices/' + id + '/labels', { labels: labels }); App.toast('标签已保存'); render(c); } catch (e) { App.toast(e.message, true); }
        }
      };
    });
  }

  App.register('devices', '设备', render);
})();
