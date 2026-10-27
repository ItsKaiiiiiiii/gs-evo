// 控制台前端冒烟检查：按 index.html 的加载顺序在桩 DOM 里执行全部脚本，
// 断言每个页面都成功注册到 App（可捕获"App 未定义 / 漏注册 / 加载顺序"类回归）。
// 用法：node scripts/check_console.mjs
import { readFileSync } from 'fs';
import path from 'path';
import vm from 'vm';

const root = path.resolve('cloud/console/static');
const html = readFileSync(path.join(root, 'index.html'), 'utf8');

// 从 index.html 解析脚本加载顺序（保证测的就是页面真实的加载顺序）
const scripts = [...html.matchAll(/<script src="([^"]+)"><\/script>/g)].map(m => m[1]);
if (scripts.length === 0) { console.error('FAIL: index.html 未解析到 script'); process.exit(1); }

// ---- 桩 DOM / 浏览器环境 ----
function makeElement(id = '') {
  const el = {
    id, children: [], dataset: {}, style: {},
    innerHTML: '', textContent: '', value: '',
    _classes: new Set(),
    classList: {
      add(c) { el._classes.add(c); },
      remove(c) { el._classes.delete(c); },
      toggle(c, force) { (force === undefined ? !el._classes.has(c) : force) ? el._classes.add(c) : el._classes.delete(c); },
      contains(c) { return el._classes.has(c); },
    },
    addEventListener() {}, appendChild(child) { el.children.push(child); },
    scrollIntoView() {}, onclick: null, onload: null,
  };
  return el;
}

const elements = {};
const domReadyHandlers = [];
const sandbox = {
  console,
  setTimeout, clearTimeout, setInterval, clearInterval,
  fetch: async () => ({ status: 200, ok: true, text: async () => '[]' }),
  localStorage: { _m: {}, getItem(k) { return this._m[k] ?? null; }, setItem(k, v) { this._m[k] = String(v); }, removeItem(k) { delete this._m[k]; } },
  confirm: () => true,
  alert: () => {},
  location: { reload() {} },
};
sandbox.window = sandbox; // window.X = ... 即全局（与浏览器一致）
sandbox.addEventListener = (_t, fn) => { domReadyHandlers.push(fn); }; // window.addEventListener
sandbox.document = {
  getElementById(id) {
    if (!elements[id]) elements[id] = makeElement(id);
    return elements[id];
  },
  createElement(tag) { return makeElement('<' + tag + '>'); },
  querySelectorAll() { return []; },
  addEventListener(_t, fn) { domReadyHandlers.push(fn); },
};
sandbox.localStorage = sandbox.localStorage;
vm.createContext(sandbox);

// ---- 按序执行 ----
let failed = false;
for (const src of scripts) {
  const file = path.join(root, src);
  try {
    vm.runInContext(readFileSync(file, 'utf8'), sandbox, { filename: src });
  } catch (e) {
    console.error(`FAIL: ${src} 执行异常: ${e.message}`);
    failed = true;
  }
}

// ---- 断言 ----
if (typeof sandbox.API !== 'object') { console.error('FAIL: window.API 未定义（api.js 未生效）'); failed = true; }
if (typeof sandbox.App !== 'object') { console.error('FAIL: window.App 未定义——检查加载顺序（main.js 必须先于 pages/*.js）'); failed = true; }

// 触发 DOMContentLoaded（boot：导航渲染 + 登录流程）
for (const fn of domReadyHandlers) { try { fn(); } catch (e) { console.error(`FAIL: boot 异常: ${e.message}`); failed = true; } }

const navItems = (elements['nav']?.children || []).length;
if (navItems !== 5) { console.error(`FAIL: 导航项 ${navItems} 个，期望 5（设备/Skill/任务/进化/报表）`); failed = true; }

if (!failed) {
  console.log(`console smoke OK: ${scripts.length} 个脚本按序执行，App 注册 ${navItems} 个页面`);
}
process.exit(failed ? 1 : 0);
