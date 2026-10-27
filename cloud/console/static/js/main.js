// main.js：应用壳——页面注册、导航、登录流程。
// 扩展方式：新页面 = 新建 js/pages/xxx.js 并 App.register('id','名称',render)（加载顺序在 main.js 之前）。
(function () {
  const pages = []; // {id, title, render(container)}

  window.App = {
    register(id, title, render) { pages.push({ id, title, render }); },
    go(id) {
      const p = pages.find(x => x.id === id);
      if (!p) return;
      document.querySelectorAll('.nav-item').forEach(n => n.classList.toggle('active', n.dataset.id === id));
      const c = document.getElementById('content');
      c.innerHTML = '';
      p.render(c);
    },
    toast(msg, isErr) {
      const t = document.getElementById('toast');
      t.textContent = msg;
      t.className = 'toast' + (isErr ? ' err' : '');
      clearTimeout(t._timer);
      t._timer = setTimeout(() => t.classList.add('hidden'), 4000);
    },
    onAuthFailed() {
      document.getElementById('login-mask').classList.remove('hidden');
      document.getElementById('app').classList.add('hidden');
    },
    esc(s) {
      const d = document.createElement('div');
      d.textContent = s == null ? '' : String(s);
      return d.innerHTML;
    },
  };

  // 角色探测：admin 可写，viewer 只读（隐藏写按钮用）
  window.App.role = 'admin';
  window.App.canWrite = () => App.role === 'admin';

  // ---- 登录 ----
  async function tryLogin(t) {
    API.setToken(t);
    await API.get('/api/v1/admin/report/summary'); // 401 → 抛错；403 → viewer
    return true;
  }

  function enterApp() {
    document.getElementById('login-mask').classList.add('hidden');
    document.getElementById('app').classList.remove('hidden');
    // 探测角色：尝试一个写端点（无副作用的签名校验路径不存在，用 tokens 端点 403 判 viewer）
    fetch('/api/v1/admin/enroll-tokens', {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + API.token(), 'Content-Type': 'application/json' },
      body: '{}',
    }).then(r => {
      App.role = (r.status === 403) ? 'viewer' : 'admin';
      document.getElementById('role-badge').textContent = App.role;
      document.getElementById('role-badge').className = 'badge ' + (App.role === 'admin' ? 'green' : 'yellow');
    }).catch(() => { document.getElementById('role-badge').textContent = '?'; });
  }

  window.addEventListener('DOMContentLoaded', () => {
    // 导航渲染
    const nav = document.getElementById('nav');
    pages.forEach(p => {
      const a = document.createElement('a');
      a.className = 'nav-item';
      a.dataset.id = p.id;
      a.textContent = p.title;
      a.onclick = () => App.go(p.id);
      nav.appendChild(a);
    });

    document.getElementById('login-btn').onclick = async () => {
      const err = document.getElementById('login-err');
      err.textContent = '';
      try {
        await tryLogin(document.getElementById('login-token').value.trim());
        enterApp();
        App.go(pages[0] ? pages[0].id : 'report');
      } catch (e) { err.textContent = e.message; }
    };
    document.getElementById('login-token').addEventListener('keydown', e => {
      if (e.key === 'Enter') document.getElementById('login-btn').click();
    });
    document.getElementById('logout-btn').onclick = () => {
      API.clearToken();
      location.reload();
    };

    // 已有 token 直接进入
    if (API.token()) {
      tryLogin(API.token()).then(enterApp).catch(() => App.onAuthFailed());
    } else {
      App.onAuthFailed();
    }
  });
})();
