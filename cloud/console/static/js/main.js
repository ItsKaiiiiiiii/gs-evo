// main.js：应用壳——页面注册、导航、登录流程。
// 扩展方式：新页面 = 新建 js/pages/xxx.js 并 App.register('id','名称',render)（加载顺序在 main.js 之前）。
(function () {
  const pages = []; // {id, title, render(container), nav}

  window.App = {
    // nav:false = 架构预留页（不经导航露出，hash 直达仍可用）
    register(id, title, render, opts) {
      pages.push({ id, title, render, nav: !opts || opts.nav !== false });
    },
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
    fmtTime(iso) {
      if (!iso) return '-';
      const d = new Date(iso);
      if (isNaN(d)) return App.esc(iso);
      const p = n => String(n).padStart(2, '0');
      return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
    },
    esc(s) {
      const d = document.createElement('div');
      d.textContent = s == null ? '' : String(s);
      return d.innerHTML.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
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
    // 探测角色：whoami（无副作用）
    fetch('/api/v1/admin/whoami', { headers: { 'Authorization': 'Bearer ' + API.token() } })
      .then(r => r.json())
      .then(r => {
        App.role = r.role || 'viewer';
        document.getElementById('role-badge').textContent = App.role;
        document.getElementById('role-badge').className = 'badge ' + (App.role === 'admin' ? 'green' : 'yellow');
      })
      .catch(() => { document.getElementById('role-badge').textContent = '?'; });
  }

  window.addEventListener('DOMContentLoaded', () => {
    // 导航渲染
    const nav = document.getElementById('nav');
    pages.filter(p => p.nav).forEach(p => {
      const a = document.createElement('a');
      a.className = 'nav-item';
      a.dataset.id = p.id;
      a.textContent = p.title;
      a.onclick = () => { location.hash = '#' + p.id; };
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

    // hash 单一事实源：导航点击只改 hash，路由统一由 hashchange 驱动（前进/后退可用）
    window.addEventListener('hashchange', () => {
      const target = (location.hash || '').replace('#', '');
      if (pages.some(p => p.id === target && p.nav)) App.go(target);
    });

    // 已有 token 直接进入；恢复 hash 定位的页面（N-10）
    if (API.token()) {
      tryLogin(API.token()).then(() => {
        enterApp();
        const target = (location.hash || '').replace('#', '');
        const pg = pages.find(p => p.id === target && p.nav);
        App.go(pg ? pg.id : (pages.find(p => p.nav) || pages[0]).id);
      }).catch(() => App.onAuthFailed());
    } else {
      App.onAuthFailed();
    }
  });
})();
