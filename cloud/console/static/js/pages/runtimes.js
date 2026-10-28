// runtimes.js：运行时模块——创建与生命周期管理（multica 产品思路）。
// 左侧设备列表（选中即切换），右侧该设备的运行时列表 + 「添加运行时」对话框
// （选类型即可，无需路径——worker 在设备 PATH 定位二进制并建专属 workspace）。
// 生命周期：创建（runtime.register）→ 状态（连通性自检上报）→ 删除（runtime.remove）。
(function () {
  const API_ = '/api/v1/admin';
  let selectedDevice = null;
  let typeDefs = []; // [{type, display, binaries}]

  async function loadTypes() {
    try {
      const r = await API.get(`${API_}/runtime-types`);
      typeDefs = r.types || [];
    } catch (e) { typeDefs = []; }
  }

  function typeDisplay(t) {
    const def = typeDefs.find(x => x.type === t);
    return def ? def.display : t;
  }

  async function render(c) {
    const [devices, runtimes] = await Promise.all([
      API.get(`${API_}/devices`),
      API.get(`${API_}/runtimes`).catch(() => []),
    ]);
    if (!selectedDevice || !devices.find(d => d.id === selectedDevice)) {
      selectedDevice = devices.find(d => d.status === 'online') ? (devices.find(d => d.status === 'online')).id : (devices[0] ? devices[0].id : null);
    }

    const devRows = devices.map(d => {
      const count = (runtimes || []).filter(r => r.device_id === d.id).length;
      return `<tr style="cursor:pointer" data-id="${d.id}" class="${d.id === selectedDevice ? 'active-row' : ''}">
        <td><b>${App.esc(d.hostname)}</b></td>
        <td>${App.esc(d.os)}/${App.esc(d.arch)}</td>
        <td>${badge(d.status)}</td>
        <td>${count} 个</td>
      </tr>`;
    }).join('');

    c.innerHTML = `<div class="page-title">运行时</div>
    <p class="muted" style="margin-bottom:12px">运行时 = 设备上的一个 agent 实例。选中设备后可为其添加运行时（worker 自动定位二进制并创建专属 workspace，无需填写路径）；创建后自动做连通性自检并上报在线状态。</p>
    <div class="grid2">
      <div class="card">
        <h3>设备</h3>
        <table><tr><th>主机名</th><th>平台</th><th>状态</th><th>运行时</th></tr>${devRows}</table>
      </div>
      <div class="card">
        <h3>运行时（${App.esc(devName(devices, selectedDevice))}）</h3>
        <div id="rt-list">加载中…</div>
      </div>
    </div>`;

    c.querySelectorAll('tr[data-id]').forEach(tr => {
      tr.onclick = () => { selectedDevice = tr.dataset.id; render(c); };
    });

    if (selectedDevice) await loadRuntimesOf(c, devices, runtimes);
  }

  function devName(devices, id) {
    const d = (devices || []).find(x => x.id === id);
    return d ? d.hostname : (id ? id.slice(0, 13) + '…' : '（未选设备）');
  }

  async function loadRuntimesOf(c, devices, runtimes) {
    const el = document.getElementById('rt-list');
    if (!selectedDevice) { el.innerHTML = '<span class="muted">先选择设备</span>'; return; }
    const mine = (runtimes || []).filter(r => r.device_id === selectedDevice);
    el.innerHTML = `
      ${App.canWrite() ? `<div class="form-row"><button id="rt-add" class="primary">添加运行时</button>
        <span class="muted">支持类型：${typeDefs.map(t => App.esc(t.display)).join(' / ')}（可扩展）</span></div>
      <div id="rt-form" class="hidden">
        <div class="form-row">
          <label>类型</label>
          <select id="nrt-type">${typeDefs.map(t => `<option value="${App.esc(t.type)}">${App.esc(t.display)}（设备 PATH 查找: ${App.esc(t.binaries.join('/'))}）</option>`).join('')}</select>
        </div>
        <div class="form-row">
          <label>参数</label><input id="nrt-args" style="flex:1" placeholder="启动参数，逗号分隔（可选，不同类型参数不同）">
          <button class="small primary" id="nrt-create">创建</button>
        </div>
        <div id="nrt-out" class="muted"></div>
      </div>` : '<span class="muted">viewer 只读</span>'}
      <table style="margin-top:10px"><tr><th>类型</th><th>版本</th><th>状态</th><th>来源</th><th>workspace</th><th>操作</th></tr>
      ${mine.map(r => `<tr>
        <td><b>${typeDisplay(r.type)}</b></td>
        <td>${App.esc(r.version || '-')}</td>
        <td>${r.ok ? '<span class="badge green">在线</span>' : '<span class="badge red">离线</span>'}</td>
        <td>${r.source === 'registered' ? '<span class="badge blue">已创建</span>' : '<span class="badge gray">自动发现</span>'}</td>
        <td title="${App.esc(r.workspace || '')}">${App.esc(r.workspace ? '✓' : '-')}</td>
        <td>${App.canWrite() ? `<button class="small danger" data-del="${App.esc(r.runtime_id)}">删除</button>` : ''}</td>
      </tr>`).join('') || '<tr><td colspan="6" class="muted">该设备尚无运行时</td></tr>'}
      </table>`;

    const addBtn = document.getElementById('rt-add');
    if (addBtn) addBtn.onclick = () => document.getElementById('rt-form').classList.toggle('hidden');
    const createBtn = document.getElementById('nrt-create');
    if (createBtn) createBtn.onclick = () => createRuntime(c);
    c.querySelectorAll('[data-del]').forEach(b => {
      b.onclick = async () => {
        if (!confirm('删除该运行时？（清理 workspace 并从设备移除）')) return;
        try {
          const r0 = await API.post(`${API_}/commands`, { device_id: selectedDevice,
            cmd_type: 'runtime.remove', payload: { runtime_id: b.dataset.del }, ttl_seconds: 120 });
          const r = await API.pollCmd(r0.cmd_id);
          if (!r.ok) { App.toast('删除失败：' + r.error, true); return; }
          App.toast('运行时已删除');
          render(c);
        } catch (e) { App.toast(e.message, true); }
      };
    });
  }

  async function createRuntime(c) {
    const type = document.getElementById('nrt-type').value;
    const args = document.getElementById('nrt-args').value.split(',').map(x => x.trim()).filter(Boolean);
    const outEl = document.getElementById('nrt-out');
    outEl.textContent = '下发创建指令：worker 定位二进制 → 创建专属 workspace → 连通性自检…';
    try {
      const r0 = await API.post(`${API_}/commands`, { device_id: selectedDevice,
        cmd_type: 'runtime.register', payload: { type, args }, ttl_seconds: 120 });
      const r = await API.pollCmd(r0.cmd_id, 30000);
      if (!r.ok) {
        outEl.textContent = '创建失败：' + (r.error || '未知错误');
        App.toast('创建失败', true);
        return;
      }
      outEl.innerHTML = `已创建：<b>${typeDisplay(r.data.type)}</b>（${App.esc(r.data.runtime_id)}）状态 <b>${App.esc(r.data.status)}</b>，workspace 已建立`;
      App.toast('运行时已创建并绑定设备');
      render(c);
    } catch (e) { outEl.textContent = e.message; App.toast(e.message, true); }
  }

  function badge(status) {
    const map = { online: 'green', offline: 'gray', revoked: 'red' };
    return `<span class="badge ${map[status] || 'gray'}">${App.esc(status)}</span>`;
  }

  App.register('runtimes', '运行时', render);
})();
