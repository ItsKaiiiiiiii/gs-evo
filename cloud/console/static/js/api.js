// api.js：REST 封装（token 管理 + 统一错误提示）。所有页面模块都经此调用后端。
(function () {
  const TOKEN_KEY = 'gsevo_token';
  const API_ = '/api/v1/admin';

  function token() { return localStorage.getItem(TOKEN_KEY) || ''; }
  function setToken(t) { localStorage.setItem(TOKEN_KEY, t); }
  function clearToken() { localStorage.removeItem(TOKEN_KEY); }

  async function call(method, path, body, raw) {
    const opts = { method, headers: { 'Authorization': 'Bearer ' + token() } };
    if (body !== undefined && body !== null) {
      if (body instanceof FormData || body instanceof Blob || typeof body === 'string') {
        opts.body = body;
      } else {
        opts.headers['Content-Type'] = 'application/json';
        opts.body = JSON.stringify(body);
      }
    }
    const resp = await fetch(path, opts);
    if (resp.status === 401) {
      App.onAuthFailed();
      throw new Error('token 无效或已过期，请重新登录');
    }
    let data = null;
    const text = await resp.text();
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = text; }
    if (!resp.ok) {
      const msg = (data && data.error) ? data.error : ('HTTP ' + resp.status);
      if (resp.status === 403) throw new Error('权限不足（viewer 只读）：' + msg);
      throw new Error(msg);
    }
    return raw ? text : data;
  }

  // pollCmd 轮询指令回执直到 acked/超时；resolve 统一形态 {status, ok, data, error}。
  async function pollCmd(cmdID, timeoutMs = 60000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      const rows = await call('GET', `${API_}/commands?cmd_id=${cmdID}`);
      const r = (rows || [])[0];
      if (r && (r.status === 'acked' || r.status === 'expired')) {
        const res = r.result || {};
        return { status: r.status, ok: !!res.ok, data: res.data || {}, error: res.error, envelope: res };
      }
      await new Promise(res => setTimeout(res, 500));
    }
    return { status: 'timeout', ok: false, data: {}, error: '等待回执超时' };
  }

  window.API = {
    token, setToken, clearToken, pollCmd,
    get: (p) => call('GET', p),
    post: (p, b) => call('POST', p, b || {}),
    put: (p, b) => call('PUT', p, b),
    // 原始文本响应（报表等已格式化场景可用）
    getRaw: (p) => call('GET', p, undefined, true),
  };
})();
