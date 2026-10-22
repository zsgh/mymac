/* mymac front-end.
   No build step, no framework: one file, plain ES2020, talking to the local
   JSON API over same-origin fetch. */

'use strict';

// ----------------------------------------------------------------- helpers

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/** JSON fetch with unified error handling. */
async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: {}, credentials: 'same-origin' };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, init);
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = { raw: text }; }
  }
  if (!res.ok) {
    const e = (data && data.error) || { kind: 'internal', message: `HTTP ${res.status}` };
    const err = new Error(e.message || '请求失败');
    err.kind = e.kind;
    err.detail = e.detail;
    throw err;
  }
  return data;
}

function fmtBytes(n) {
  if (n === null || n === undefined) return '—';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

function fmtNum(n) {
  if (n === null || n === undefined) return '—';
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'G';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(n);
}

function fmtPct(n) {
  if (n === null || n === undefined) return '—';
  return n.toFixed(1) + '%';
}

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function toast(msg, kind = '') {
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.textContent = msg;
  $('#toasts').appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; setTimeout(() => el.remove(), 250); }, kind === 'err' ? 6000 : 3200);
}

/** Modal helper. `body` may be an HTML string; returns a promise of boolean. */
function modal({ title, body, okText = '确定', danger = false, collect = null, onOpen = null }) {
  return new Promise(resolve => {
    const back = $('#modal');
    $('#modal-title').textContent = title;
    $('#modal-body').innerHTML = body;
    const ok = $('#modal-ok');
    ok.textContent = okText;
    ok.className = 'btn ' + (danger ? 'danger' : 'primary');
    back.hidden = false;

    // The body exists now, so a form inside it can be wired up.
    if (onOpen) {
      try { onOpen(); } catch (e) { console.error(e); }
    }

    const close = (v) => {
      back.hidden = true;
      ok.removeEventListener('click', onOk);
      $('#modal-cancel').removeEventListener('click', onCancel);
      back.removeEventListener('click', onBack);
      document.removeEventListener('keydown', onKey);
      resolve(v);
    };
    const onOk = () => {
      let value = true;
      if (collect) {
        value = collect();
        // A form that is not valid keeps the dialog open.
        if (value === false || value === null || value === undefined) return;
      }
      close(value);
    };
    const onCancel = () => close(false);
    const onBack = (e) => { if (e.target === back) close(false); };
    const onKey = (e) => { if (e.key === 'Escape') close(false); };

    ok.addEventListener('click', onOk);
    $('#modal-cancel').addEventListener('click', onCancel);
    back.addEventListener('click', onBack);
    document.addEventListener('keydown', onKey);
  });
}

// ------------------------------------------------------------------ router

const VIEWS = {
  dash:  { title: '仪表盘',    init: initDash },
  apps:  { title: '应用卸载',  init: initApps },
  clean: { title: '系统清理',  init: initClean },
  procs: { title: '进程管理',  init: initProcs },
  ports: { title: '端口管理',  init: initPorts },
  login: { title: '启动程序',  init: initLogin },
  env:   { title: '环境变量',  init: initEnv },
  perm:  { title: '权限',      init: initPerm },
};

let currentView = null;

// viewCleanup holds the teardown functions of the current view.
//
// A view that polls in the background returns a function that stops it, so
// leaving the page really stops the work instead of leaving it running.
let viewCleanup = [];

function showView(name) {
  if (!VIEWS[name]) name = 'apps';
  for (const fn of viewCleanup) {
    try { fn(); } catch (e) { /* a broken teardown must not break the view */ }
  }
  viewCleanup = [];
  currentView = name;
  // A detail belongs to the page it was opened from.
  closeDrawer();
  $$('.view').forEach(v => { v.hidden = v.id !== 'view-' + name; });
  $$('.nav-item').forEach(b => b.classList.toggle('active', b.dataset.view === name));
  $('#view-title').textContent = VIEWS[name].title;
  $('#view-actions').innerHTML = '';
  const cleanup = VIEWS[name].init();
  if (typeof cleanup === 'function') viewCleanup.push(cleanup);
  $('#sidebar').classList.remove('open');
}

function init() {
  initTheme();
  initDrawer();
  $$('.nav-item').forEach(b => b.addEventListener('click', () => showView(b.dataset.view)));
  $('#btn-menu').addEventListener('click', () => $('#sidebar').classList.toggle('open'));
  $('#btn-audit').addEventListener('click', showAudit);

  api('/api/health').then(d => {
    $('#brand-version').textContent = 'v' + (d.version || '?');
  }).catch(() => {});

  showView('apps');
}

// ------------------------------------------------------------------ perm

async function initPerm() {
  await refreshPerm();
  $('#perm-refresh').addEventListener('click', refreshPerm, { once: true });
  loadAuditTable();
}

async function refreshPerm() {
  const box = $('#perm-list');
  box.innerHTML = '<div class="empty">检测中…</div>';
  let d;
  try {
    d = await api('/api/perm/status');
  } catch (e) {
    box.innerHTML = `<div class="empty">检测失败：${esc(e.message)}</div>`;
    return;
  }
  const icons = { granted: '✅', denied: '⛔', unknown: '❔', not_applicable: 'ℹ️' };
  box.innerHTML = (d.items || []).map(it => {
    const cls = it.state === 'granted' ? 'ok' : it.state === 'denied' ? 'bad' : it.state === 'not_applicable' ? 'info' : 'warn';
    return `<div class="perm-item">
      <div class="pi">${icons[it.state] || '•'}</div>
      <div class="pb">
        <div class="pt">${esc(it.label)} <span class="tag ${cls}">${esc(it.state)}</span></div>
        <div class="pd">${esc(it.detail || '')}</div>
        <div class="ph">开启方式：${esc(it.howTo || '')}</div>
      </div>
      <button class="btn tiny" data-target="${esc(it.target)}">打开设置</button>
    </div>`;
  }).join('');
  $$('#perm-list .btn[data-target]').forEach(b => b.addEventListener('click', async () => {
    try {
      await api('/api/perm/open', { method: 'POST', body: { target: b.dataset.target } });
      toast('已打开系统设置', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
}

// auditState holds the sort order of the audit table.
const auditState = { sort: 'ts', sortDesc: true };

// auditCols are the columns of the audit table.
const auditCols = [
  { key: 'ts', label: '时间', cls: 'mono' },
  { key: 'action', label: '操作', cls: 'mono' },
  { key: 'target', label: '目标', cls: 'mono' },
  { key: 'detail', label: '详情' },
  { key: 'result', label: '结果' },
];

// auditAccessors reads the value of a column for the sorting.
const auditAccessors = {
  ts: a => a.ts || 0,
  action: a => (a.action || '').toLowerCase(),
  target: a => (a.target || '').toLowerCase(),
  detail: a => (a.detail || '').toLowerCase(),
  result: a => (a.result || '').toLowerCase(),
};

async function loadAuditTable() {
  const t = $('#audit-table');
  try {
    const d = await api('/api/audit?limit=200');
    const items = sortRows(d.items || [], auditState, auditAccessors);
    t.innerHTML = `<thead><tr>${sortHeader(auditState, auditCols)}</tr></thead><tbody>` +
      items.map(a => `<tr data-ts="${a.ts}">
        <td class="mono muted">${esc(new Date(a.ts).toLocaleString())}</td>
        <td class="mono">${esc(a.action)}</td>
        <td class="mono wrap">${esc(a.target)}</td>
        <td class="wrap muted">${esc(a.detail || '')}</td>
        <td><span class="tag ${a.result === 'ok' ? 'ok' : a.result === 'denied' ? 'warn' : 'bad'}">${esc(a.result)}</span></td>
      </tr>`).join('') + `</tbody>`;
    bindSort('#audit-table', auditState, loadAuditTable);
    // Clicking a row shows the whole entry.
    $$('#audit-table tbody tr').forEach(tr => {
      tr.classList.add('clickable');
      tr.addEventListener('click', () => {
        const a = items.find(x => String(x.ts) === tr.dataset.ts);
        if (!a) return;
        openDrawer(`${a.action} · ${a.target}`, `<dl class="kv">
          <dt>时间</dt><dd>${esc(new Date(a.ts).toLocaleString())}</dd>
          <dt>操作</dt><dd>${esc(a.action)}</dd>
          <dt>目标</dt><dd>${esc(a.target)}</dd>
          <dt>详情</dt><dd>${esc(a.detail || '—')}</dd>
          <dt>结果</dt><dd>${esc(a.result)}</dd>
        </dl>`);
      });
    });
  } catch (e) {
    t.innerHTML = `<tbody><tr><td class="empty">${esc(e.message)}</td></tr></tbody>`;
  }
}

async function showAudit() {
  const ok = await modal({
    title: '审计日志',
    body: '<p>最近的操作记录同时写入 <code>~/.config/mymac/audit.log</code>。</p>',
    okText: '在权限页查看',
  });
  if (ok) showView('perm');
}

// checkTrash shows the Finder permission state once per view.
let trashChecked = false;
async function checkTrash() {
  if (trashChecked) return;
  trashChecked = true;
  let d;
  try { d = await api('/api/trash/status'); } catch { return; }
  if (d.available) return;
  const el = document.createElement('div');
  el.className = 'hint warn-banner';
  el.innerHTML = `⚠️ ${esc(d.detail || '未获得 Finder 自动化权限')}。${esc(d.hint || '')}
    删除仍可正常进行：文件会被移入 <code>~/.Trash</code>，可随时恢复。`;
  $('#view-apps').prepend(el);
}

// ------------------------------------------------------------------ apps

const appState = { list: [], search: '', sort: 'name', sortDesc: false, sizeTimer: null, sig: '', uninstall: new Map() };

// appCols are the columns of the application table.
const appCols = [
  { key: 'name', label: '名称' },
  { key: '', label: '操作' },
  { key: 'bundleId', label: 'Bundle ID' },
  { key: 'version', label: '版本' },
  { key: 'bundleSize', label: '应用本体', cls: 'num' },
  { key: 'dataSize', label: '用户数据', cls: 'num' },
  { key: 'size', label: '合计', cls: 'num' },
  { key: 'kind', label: '类型' },
  { key: 'signer', label: '签名' },
  { key: 'status', label: '状态' },
];

// appAccessors reads the value of a column for the sorting.
const appAccessors = {
  name: a => (a.name || '').toLowerCase(),
  bundleId: a => (a.bundleId || '').toLowerCase(),
  version: a => a.version || '',
  bundleSize: a => a.bundleSize || 0,
  dataSize: a => a.dataSize || 0,
  size: a => a.size || 0,
  kind: a => appKindRank(a),
  signer: a => (a.developer || a.teamId || '').toLowerCase(),
  status: a => appStatusRank(a),
};

// appKindRank orders the kind column: normal, iOS, MAS.
function appKindRank(a) {
  if (a.isMAS) return 2;
  if (a.isIOS) return 1;
  return 0;
}

// appStatusRank orders the status column: running, busy, idle, system.
function appStatusRank(a) {
  if (a.running) return 0;
  if (a.isSystem) return 2;
  return 1;
}

async function initApps() {
  checkTrash();
  $('#apps-search').value = appState.search;
  $('#apps-search').oninput = (e) => { appState.search = e.target.value; renderApps(); };
  $('#apps-rescan').onclick = () => loadApps(true);

  // The uninstall list is a plan: the buttons only change the plan, the
  // single 移到废纸篓 button at the top of the panel is what deletes.
  $('#un-check-all').onclick = () => {
    $$('#un-body .un-chk').forEach(c => { c.checked = true; });
    updateUninstallCount();
  };
  $('#un-select-all').onclick = () => {
    $$('#un-body .un-chk').forEach(c => { c.checked = false; });
    updateUninstallCount();
  };
  $('#un-select-safe').onclick = () => {
    // "Safe" means everything that is not high risk. The risk travels with
    // the checkbox, so the filter does not have to read the table.
    $$('#un-body .un-chk').forEach(c => { c.checked = c.dataset.risk !== 'high'; });
    updateUninstallCount();
  };
  $('#un-clear').onclick = () => {
    appState.uninstall.clear();
    renderUninstall();
  };
  $('#un-trash').onclick = executeUninstall;

  renderUninstall();
  await loadApps();
}

async function loadApps(rescan) {
  showLoading('#apps-table', rescan ? '正在扫描应用并统计大小…' : '加载中…');
  try {
    const d = await api('/api/apps' + (rescan ? '?rescan=1' : ''));
    const items = d.items || [];
    appState.list = items;
    // The table is polled while the sizes are still being measured. Rebuilding
    // it every time would replace the whole DOM for nothing, so it is only
    // redrawn when the numbers actually moved.
    const sig = appsSignature(items);
    if (rescan || sig !== appState.sig) {
      appState.sig = sig;
      renderApps();
    }
  } catch (e) {
    showError('#apps-table', e.message);
  }
}

// appsSignature summarises the application list.
//
// It changes when an application appears or disappears, or when a size has
// been measured. Everything else the table shows is derived from these.
function appsSignature(list) {
  let known = 0, total = 0;
  for (const a of list) {
    if (a.sizeKnown) {
      known++;
      total += a.size || 0;
    }
  }
  return [list.length, known, total].join(':');
}

function renderApps() {
  const q = appState.search.trim().toLowerCase();
  let items = appState.list.filter(a =>
    !q || a.name.toLowerCase().includes(q) || (a.bundleId || '').toLowerCase().includes(q));

  items = sortRows(items, appState, appAccessors);

  const totalBundle = appState.list.reduce((a, b) => a + (b.bundleSize || 0), 0);
  const totalData = appState.list.reduce((a, b) => a + (b.dataSize || 0), 0);
  const pending = appState.list.filter(a => !a.sizeKnown).length;
  $('#apps-summary').innerHTML = [
    card('已安装应用', String(appState.list.length), '可卸载候选'),
    card('应用本体', fmtBytes(totalBundle), '不含用户数据'),
    card('用户数据', fmtBytes(totalData), 'Application Support 等'),
    card('合计', fmtBytes(totalBundle + totalData), '本体 + 数据'),
  ].join('');

  const t = $('#apps-table');
  if (!items.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有匹配的应用</td></tr></tbody>';
    return;
  }
  t.innerHTML = `<thead><tr>${sortHeader(appState, appCols)}</tr></thead><tbody>` +
    items.map(a => `<tr data-bundle="${esc(a.bundleId)}">
      <td>${a.icon ? `<img src="/api/apps/icon?id=${encodeURIComponent(a.bundleId)}" width="18" height="18" style="vertical-align:-3px;margin-right:6px;border-radius:4px" alt="">` : ''}${esc(a.name)}</td>
      <td><button class="btn tiny primary" data-act="uninstall" ${a.isSystem ? 'disabled' : ''}>卸载</button></td>
      <td class="mono muted"><div class="clip-nowrap">${esc(a.bundleId)}</div></td>
      <td class="mono">${esc(a.version || '—')}</td>
      <td class="num">${a.sizeKnown ? fmtBytes(a.bundleSize) : '<span class="muted">计算中…</span>'}</td>
      <td class="num">${a.sizeKnown ? fmtBytes(a.dataSize) : '<span class="muted">—</span>'}</td>
      <td class="num">${a.sizeKnown ? fmtBytes(a.size) : '<span class="muted">—</span>'}</td>
      <td>${a.isMAS ? '<span class="tag info">MAS</span> ' : ''}${a.isIOS ? '<span class="tag warn">iOS</span>' : ''}${!a.isMAS && !a.isIOS ? '<span class="tag mute">普通</span>' : ''}</td>
      <td class="muted"><div class="clip-nowrap">${esc(a.developer || a.teamId || '—')}</div></td>
      <td>${a.running ? '<span class="tag ok">运行中</span>' : (a.isSystem ? '<span class="tag bad">系统</span>' : '<span class="tag mute">空闲</span>')}</td>
    </tr>`).join('') + '</tbody>';

  // Keep polling until the sizes have been measured.
  if (pending > 0) {
    clearTimeout(appState.sizeTimer);
    appState.sizeTimer = setTimeout(() => loadApps(false), 1500);
  } else {
    clearTimeout(appState.sizeTimer);
  }

  // Sorting.
  bindSort('#apps-table', appState, renderApps);

  $$('#apps-table tbody tr').forEach(tr => {
    tr.classList.add('clickable');
    $$('button[data-act]', tr).forEach(b => b.addEventListener('click', (e) => {
      e.stopPropagation();
      addToUninstall(tr.dataset.bundle);
    }));
    // Clicking the row opens the detail of the application.
    tr.addEventListener('click', () => showAppDetail(tr.dataset.bundle));
  });
}

function card(k, v, s) {
  return `<div class="card"><div class="k">${esc(k)}</div><div class="v">${esc(v)}</div><div class="s">${esc(s || '')}</div></div>`;
}

// showLoading puts a placeholder into a container.
//
// Every view that loads data shows one of these, so an empty panel always
// says what is happening instead of looking broken. A table gets the
// placeholder as a row, because a div is not valid inside a table.
function showLoading(sel, msg) {
  const el = $(sel);
  if (!el) return;
  const body = `<div class="loading"><span class="spinner"></span>${esc(msg || '加载中…')}</div>`;
  if (el.tagName === 'TABLE') {
    el.innerHTML = `<tbody><tr><td colspan="99">${body}</td></tr></tbody>`;
  } else {
    el.innerHTML = body;
  }
}

// showError puts an error into a container.
function showError(sel, msg) {
  const el = $(sel);
  if (!el) return;
  const body = `<div class="empty err">${esc(msg)}</div>`;
  if (el.tagName === 'TABLE') {
    el.innerHTML = `<tbody><tr><td colspan="99">${body}</td></tr></tbody>`;
  } else {
    el.innerHTML = body;
  }
}

// addToUninstall puts an application into the uninstall list.
//
// Nothing is deleted here. The list is a plan the user reviews, and the
// single 移到废纸篓 button at the top of the panel is what actually moves
// things to the Trash.
async function addToUninstall(bundleID) {
  if (appState.uninstall.has(bundleID)) {
    toast('这个应用已经在卸载列表里了', 'err');
    $('#apps-uninstall').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    return;
  }

  let d;
  try {
    d = await api(`/api/apps/residuals?ids=${encodeURIComponent(bundleID)}`);
  } catch (e) {
    toast(e.message, 'err');
    return;
  }
  const groups = d.groups || [];
  if (!groups.length) {
    toast('找不到这个应用，可能它已经被移走了', 'err');
    return;
  }

  const g = groups[0];
  const rows = [];
  // The bundle itself is the first row; the leftovers follow.
  rows.push({
    path: g.path, kind: 'appBundle', size: g.bundleSize, matchedBy: g.bundleId,
    risk: g.isSystem ? 'high' : 'medium', selected: !g.isSystem, note: '应用程序本体',
  });
  for (const it of (g.items || [])) {
    rows.push({
      path: it.path, kind: it.kind, size: it.size,
      matchedBy: it.matchedBy || it.matchKind, risk: it.risk,
      selected: it.selected, note: it.note,
    });
  }

  appState.uninstall.set(bundleID, { group: g, rows });
  renderUninstall();
  $('#apps-uninstall').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  toast(`已加入卸载列表：${g.name}（${rows.length} 项）`, 'ok');
}

// showAppDetail opens the detail of one application in the drawer.
//
// It shows what the application left behind, because that is what the
// uninstall list is about; the list itself stays in the page below.
async function showAppDetail(bundleID) {
  const app = appState.list.find(a => a.bundleId === bundleID);
  if (!app) return;

  let d = { groups: [] };
  try {
    d = await api(`/api/apps/residuals?ids=${encodeURIComponent(bundleID)}`);
  } catch (e) {
    // The application info is still worth showing.
  }
  const g = (d.groups || [])[0] || {};
  const items = g.items || [];
  const residualSize = items.reduce((a, b) => a + (b.size || 0), 0);

  openDrawer(app.name || bundleID, `
    <dl class="kv">
      <dt>Bundle ID</dt><dd>${esc(app.bundleId || '—')}</dd>
      <dt>版本</dt><dd>${esc(app.version || '—')}</dd>
      <dt>路径</dt><dd>${esc(app.path || '—')}</dd>
      <dt>开发者</dt><dd>${esc(app.developer || app.teamId || '—')}</dd>
      <dt>应用本体</dt><dd>${app.sizeKnown ? fmtBytes(app.bundleSize) : '计算中…'}</dd>
      <dt>用户数据</dt><dd>${app.sizeKnown ? fmtBytes(app.dataSize) : '计算中…'}</dd>
      <dt>残留文件</dt><dd>${items.length} 项 · ${fmtBytes(residualSize)}</dd>
      <dt>状态</dt><dd>${app.running ? '运行中' : (app.isSystem ? '系统应用' : '空闲')}</dd>
    </dl>
    <div class="detail-actions" style="margin:14px 0 6px">
      <button class="btn primary" id="app-add-uninstall" ${app.isSystem ? 'disabled' : ''}>加入卸载列表</button>
    </div>
    ${items.length ? `<h3 style="font-size:13px;margin:14px 0 0">留下的文件</h3>
      <div class="path-list">${items.map(x => `<div class="path-item">
        <span class="sp">${esc(x.path)}</span>
        <span class="tag mute">${esc(x.note || x.kind)}</span>
      </div>`).join('')}</div>`
      : '<div class="empty">没有发现残留文件</div>'}`);

  const add = $('#app-add-uninstall');
  if (add) {
    add.onclick = async () => {
      await addToUninstall(bundleID);
      closeDrawer();
    };
  }
}

// renderUninstall draws the whole uninstall list.
//
// Every application keeps its own group, the way a cleaner shows it: the
// bundle itself and everything it left behind in one place.
function renderUninstall() {
  const body = $('#un-body');
  const entries = [...appState.uninstall.values()];

  if (!entries.length) {
    body.innerHTML = '<div class="empty">还没有待卸载的应用。点上面应用行的「卸载」把它加进来。</div>';
    updateUninstallCount();
    return;
  }

  let html = '';
  for (const e of entries) {
    const g = e.group;
    const gsize = e.rows.reduce((a, r) => a + (r.size || 0), 0);
    html += `<div class="un-group">
      <div class="un-group-head">
        <b>${esc(g.name)}</b>
        <span class="mono muted">${esc(g.bundleId)}</span>
        <span class="tag info">${fmtBytes(gsize)}</span>
        ${g.running ? '<span class="tag warn">运行中，建议先退出</span>' : ''}
        ${g.isSystem ? '<span class="tag bad">系统应用</span>' : ''}
        <button class="btn tiny" data-un-remove="${esc(g.bundleId)}">移出列表</button>
      </div>
      <table class="table un-table"><tbody>` +
      e.rows.map((r, i) => `<tr>
        <td class="pick"><input type="checkbox" class="un-chk" data-app="${esc(g.bundleId)}" data-i="${i}" data-risk="${esc(r.risk)}" ${r.selected ? 'checked' : ''}></td>
        <td class="mono"><div class="clip">${esc(r.path)}</div></td>
        <td class="muted">${esc(kindLabel(r.kind))}</td>
        <td class="num">${fmtBytes(r.size)}</td>
        <td class="muted">${esc(r.matchedBy || '')}</td>
        <td><span class="tag ${r.risk === 'high' ? 'bad' : r.risk === 'medium' ? 'warn' : 'mute'}">${esc(riskLabel(r.risk))}</span></td>
      </tr>`).join('') + `</tbody></table></div>`;
  }
  body.innerHTML = html;

  $$('#un-body .un-chk').forEach(c => c.addEventListener('change', updateUninstallCount));
  $$('#un-body [data-un-remove]').forEach(b => b.addEventListener('click', () => {
    appState.uninstall.delete(b.dataset.unRemove);
    renderUninstall();
  }));

  updateUninstallCount();
}

// updateUninstallCount refreshes the counter on the 移到废纸篓 button.
function updateUninstallCount() {
  const n = $$('#un-body .un-chk').filter(c => c.checked).length;
  const btn = $('#un-trash');
  btn.textContent = n ? `移到废纸篓 (${n})` : '移到废纸篓';
  btn.disabled = n === 0;
}

// executeUninstall moves everything the user selected to the Trash.
//
// The whole list goes in one request, so the Finder automation is asked for
// once instead of once per application.
async function executeUninstall() {
  const picked = [];
  for (const c of $$('#un-body .un-chk')) {
    if (!c.checked) continue;
    const e = appState.uninstall.get(c.dataset.app);
    if (!e) continue;
    const r = e.rows[+c.dataset.i];
    if (r) picked.push({ app: e.group, row: r });
  }
  if (!picked.length) { toast('没有选中任何项目', 'err'); return; }

  const size = picked.reduce((a, p) => a + (p.row.size || 0), 0);
  const byApp = new Map();
  for (const p of picked) {
    if (!byApp.has(p.app.bundleId)) byApp.set(p.app.bundleId, { name: p.app.name, paths: [] });
    byApp.get(p.app.bundleId).paths.push(p.row.path);
  }
  const detail = [...byApp.values()].map(a =>
    `<b>${esc(a.name)}</b>\n${a.paths.map(p => '  ' + esc(p)).join('\n')}`).join('\n\n');

  const ok = await modal({
    title: '确认卸载',
    body: `<p>将把 ${picked.length} 项（约 ${fmtBytes(size)}）从 ${byApp.size} 个应用移入废纸篓：</p>
      <pre>${detail}</pre>
      <p class="muted">移入废纸篓后可以随时恢复。</p>`,
    okText: '移到废纸篓', danger: true,
  });
  if (!ok) return;

  const bundleIDs = [...byApp.keys()];
  try {
    const r = await api('/api/apps/uninstall', {
      method: 'POST',
      body: { bundleIDs, paths: picked.map(p => p.row.path), dryRun: false },
    });
    toast(`已处理 ${r.moved ?? 0} 项${r.failed?.length ? '，' + r.failed.length + ' 项失败' : ''}`, r.failed?.length ? 'err' : 'ok');
    if (r.note) toast(r.note, 'err');

    // Applications whose items are all gone leave the list; the ones that
    // failed stay so the user can retry.
    const moved = new Set(r.moved || []);
    const selected = new Set(picked.map(p => p.row.path));
    for (const [id, e] of [...appState.uninstall]) {
      const done = e.rows.every(x => !selected.has(x.path) || moved.has(x.path));
      if (done) appState.uninstall.delete(id);
    }
    renderUninstall();
    await loadApps(true);
  } catch (e) { toast(e.message, 'err'); }
}

// kindLabel names the kind of an item in the uninstall list.
function kindLabel(kind) {
  return {
    appBundle: '应用本体',
    appSupport: '应用数据',
    cache: '缓存',
    prefs: '偏好设置',
    logs: '日志',
    savedState: '窗口状态',
    webkit: 'WebKit 数据',
    httpStorage: 'HTTP 存储',
    cookies: 'Cookie',
    appScript: '应用脚本',
    container: '沙盒容器',
    groupContainer: '共享容器',
    launchAgent: '启动项',
    launchDaemon: '系统后台服务',
    helper: '特权辅助工具',
  }[kind] || kind;
}

// riskLabel names a risk level.
function riskLabel(risk) {
  return { high: '高', medium: '中', low: '低' }[risk] || risk;
}

// ----------------------------------------------------------------- clean

// cleanCols are the columns of the cleanup table, one set per tab.
const cleanColsCache = [
  { key: '', label: '' },
  { key: 'name', label: '名称' },
  { key: 'category', label: '类别' },
  { key: 'size', label: '大小', cls: 'num' },
  { key: 'path', label: '路径' },
  { key: 'note', label: '说明' },
];
const cleanColsLarge = [
  { key: '', label: '' },
  { key: 'size', label: '大小', cls: 'num' },
  { key: 'path', label: '路径' },
  { key: 'mod', label: '修改时间' },
];
const cleanColsDup = [
  { key: '', label: '' },
  { key: 'size', label: '大小', cls: 'num' },
  { key: 'path', label: '路径' },
  { key: 'note', label: '说明' },
];

// cleanAccessors reads the value of a column for the sorting.
const cleanAccessors = {
  name: it => (it.name || '').toLowerCase(),
  category: it => (it.category || '').toLowerCase(),
  size: it => it.size || 0,
  path: it => (it.path || '').toLowerCase(),
  note: it => (it.note || '').toLowerCase(),
  mod: it => it.mod || 0,
};

const cleanState = { items: [], mode: 'cache', large: [], dupes: [], job: null, jobTimer: null, sort: 'size', sortDesc: true, summary: {} };

async function initClean() {
  $$('#clean-tabs .tab').forEach(t => t.addEventListener('click', () => {
    $$('#clean-tabs .tab').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    cleanState.mode = t.dataset.mode;
    loadClean();
  }));
  $('#clean-scan').onclick = () => loadClean();
  $('#clean-execute').onclick = () => executeClean();
  await loadClean();
}

async function loadClean() {
  if (cleanState.mode === 'large') return loadLargeFiles();
  if (cleanState.mode === 'dup') return loadDuplicates();

  const t = $('#clean-table');
  t.innerHTML = '<tbody><tr><td class="empty">扫描中…</td></tr></tbody>';
  $('#clean-hint').textContent = '扫描需要遍历缓存目录并统计实际占用（按 APFS 已分配空间计算，不含克隆），首次可能需要几秒。删除一律移入废纸篓，可随时恢复。';
  try {
    const d = await api('/api/clean/scan');
    cleanState.items = d.items || [];
    renderClean(d.summary || {});
  } catch (e) {
    t.innerHTML = `<tbody><tr><td class="empty">${esc(e.message)}</td></tr></tbody>`;
  }
}

// loadLargeFiles starts the large file scan.
//
// The scan takes tens of seconds, so it runs in the background and the page
// polls the job.
async function loadLargeFiles() {
  const t = $('#clean-table');
  t.innerHTML = '<tbody><tr><td class="empty">正在启动扫描…</td></tr></tbody>';
  $('#clean-hint').textContent = '大文件扫描会遍历整个主目录（跳过 .git、node_modules 和废纸篓），按实际占用空间排序。扫描在后台进行，可以继续使用其它页面。';
  let job;
  try {
    job = await api('/api/clean/large');
  } catch (e) {
    t.innerHTML = `<tbody><tr><td class="empty">${esc(e.message)}</td></tr></tbody>`;
    return;
  }
  cleanState.job = job;
  pollCleanJob(job);
}

// loadDuplicates starts the duplicate file scan.
async function loadDuplicates() {
  const t = $('#clean-table');
  t.innerHTML = '<tbody><tr><td class="empty">正在启动扫描…</td></tr></tbody>';
  $('#clean-hint').innerHTML = '重复文件检测分三步：先比大小，再比头尾 4 KiB 的部分哈希，最后比完整 SHA-256。' +
    '<br><b>内容相同不等于可以删。</b>路径可能被脚本或配置引用；标记为「硬链接」的是同一个 inode 的多个名字，删任何一个都不省空间；' +
    'APFS 克隆同理，共享的数据块不会因为删掉一份而释放。所以重复文件<b>默认不勾选</b>，请逐条确认后再删。';
  let job;
  try {
    job = await api('/api/clean/duplicates');
  } catch (e) {
    t.innerHTML = `<tbody><tr><td class="empty">${esc(e.message)}</td></tr></tbody>`;
    return;
  }
  cleanState.job = job;
  pollCleanJob(job);
}

// pollCleanJob waits for a scan job to finish.
function pollCleanJob(job) {
  clearTimeout(cleanState.jobTimer);
  const tick = async () => {
    let d;
    try {
      d = await api('/api/clean/job?id=' + encodeURIComponent(job.id));
    } catch (e) {
      return;
    }
    cleanState.job = d;
    if (d.state === 'running') {
      const msg = d.message || '扫描中…';
      const rows = d.kind === 'large' ? (d.files || []) : [];
      $('#clean-table').innerHTML = `<tbody><tr><td class="empty">${esc(msg)}，已找到 ${rows.length} 项</td></tr></tbody>`;
      cleanState.jobTimer = setTimeout(tick, 1000);
      return;
    }
    if (d.state === 'failed') {
      $('#clean-table').innerHTML = `<tbody><tr><td class="empty">扫描失败：${esc(d.error || '')}</td></tr></tbody>`;
      return;
    }
    if (d.kind === 'large') {
      cleanState.large = d.files || [];
      renderClean();
    } else {
      cleanState.dupes = d.groups || [];
      renderClean();
    }
  };
  cleanState.jobTimer = setTimeout(tick, 700);
}

function renderClean(summary) {
  if (cleanState.mode === 'large') return renderLargeFiles();
  if (cleanState.mode === 'dup') return renderDuplicates();
  // The summary is kept so a re-render after a sort still has it.
  cleanState.summary = summary || cleanState.summary;
  summary = cleanState.summary;

  const total = cleanState.items.reduce((a, b) => a + (b.size || 0), 0);
  $('#clean-summary').innerHTML = [
    card('可回收', fmtBytes(total), `${cleanState.items.length} 个项目`),
    card('用户缓存', fmtBytes(summary.userCaches || 0), '~/Library/Caches'),
    card('用户日志', fmtBytes(summary.userLogs || 0), '~/Library/Logs'),
    card('开发者缓存', fmtBytes(summary.devCaches || 0), 'Xcode / 包管理器'),
  ].join('');

  const t = $('#clean-table');
  if (!cleanState.items.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有可清理项</td></tr></tbody>';
    return;
  }
  const items = sortRows(cleanState.items, cleanState, cleanAccessors);
  t.innerHTML = `<thead><tr>${sortHeader(cleanState, cleanColsCache)}</tr></thead><tbody>` +
    items.map(it => `<tr>
      <td><input type="checkbox" class="clean-chk" data-path="${esc(it.path)}" ${it.selected ? 'checked' : ''}></td>
      <td>${esc(it.name)}</td>
      <td><span class="tag mute">${esc(it.category)}</span></td>
      <td class="num">${fmtBytes(it.size)}</td>
      <td class="mono wrap muted">${esc(it.path)}</td>
      <td class="muted wrap">${esc(it.note || '')}</td>
    </tr>`).join('') + '</tbody>';

  bindSort('#clean-table', cleanState, () => renderClean(cleanState.summary));
  bindCleanChecks();
  bindCleanRows(tr => items.find(it => it.path === tr.querySelector('input')?.dataset.path));
}

// showCleanDetail opens one cleanup item in the drawer.
function showCleanDetail(it) {
  const rows = [
    ['名称', it.name || '—'],
    ['类别', it.category || '—'],
    ['大小', fmtBytes(it.size || 0)],
    ['路径', it.path || '—'],
    ['说明', it.note || '—'],
  ];
  if (it.mod) rows.push(['修改时间', new Date(it.mod * 1000).toLocaleString()]);
  openDrawer(it.name || it.path || '清理项', `<dl class="kv">${rows.map(r =>
    `<dt>${esc(r[0])}</dt><dd>${esc(r[1])}</dd>`).join('')}</dl>`);
}

// bindCleanRows makes the rows of the cleanup table open the detail.
function bindCleanRows(get) {
  $$('#clean-table tbody tr').forEach(tr => {
    tr.classList.add('clickable');
    tr.addEventListener('click', () => {
      const it = get(tr);
      if (it) showCleanDetail(it);
    });
  });
}

// renderLargeFiles draws the big file list.
function renderLargeFiles() {
  const items = cleanState.large;
  $('#clean-summary').innerHTML = [
    card('大文件', String(items.length), '超过 500 MB'),
    card('合计', fmtBytes(items.reduce((a, b) => a + b.size, 0)), '按实际占用'),
    card('最大', items.length ? fmtBytes(items[0].size) : '—', items.length ? items[0].path.split('/').pop() : ''),
  ].join('');

  const t = $('#clean-table');
  if (!items.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有找到超过 500 MB 的文件</td></tr></tbody>';
    $('#clean-execute').disabled = true;
    return;
  }
  const sorted = sortRows(items, cleanState, cleanAccessors);
  t.innerHTML = `<thead><tr>${sortHeader(cleanState, cleanColsLarge)}</tr></thead><tbody>` +
    sorted.map(f => `<tr>
      <td><input type="checkbox" class="clean-chk" data-path="${esc(f.path)}"></td>
      <td class="num">${fmtBytes(f.size)}</td>
      <td class="mono wrap">${esc(f.path)}</td>
      <td class="muted mono">${esc(new Date(f.mod * 1000).toLocaleString())}</td>
    </tr>`).join('') + '</tbody>';
  bindSort('#clean-table', cleanState, renderLargeFiles);
  bindCleanChecks();
  bindCleanRows(tr => items.find(f => f.path === tr.querySelector('input')?.dataset.path));
}

// renderDuplicates draws the duplicate groups.
//
// Nothing is checked by default. Two files with the same content are not
// necessarily redundant: a path may be referenced from a script, the files
// may be hard links to the same inode, and on APFS they may be clones that
// share their blocks. Deleting them can break a reference and may free no
// space at all.
function renderDuplicates() {
  const groups = cleanState.dupes;
  const files = groups.reduce((a, g) => a + g.paths.length, 0);
  const theoretical = groups.reduce((a, g) => a + g.size * (g.paths.length - 1), 0);
  const hard = groups.reduce((a, g) => a + g.paths.filter(p => (g.links || {})[p] > 1).length, 0);

  $('#clean-summary').innerHTML = [
    card('重复组', String(groups.length), '内容完全相同'),
    card('文件数', String(files), '超过 1 MB'),
    card('理论可回收', fmtBytes(theoretical), '实际通常远小于此'),
  ].join('');

  const t = $('#clean-table');
  if (!groups.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有找到重复文件</td></tr></tbody>';
    $('#clean-execute').disabled = true;
    return;
  }

  const rows = [];
  for (const g of groups) {
    g.paths.forEach((p, i) => {
      const links = (g.links || {})[p] || 1;
      const note = links > 1
        ? '<span class="tag warn" title="这些路径是同一个 inode 的硬链接，删除任何一个都不会释放空间">硬链接 ×' + links + '</span>'
        : (i === 0 ? '<span class="tag mute">建议保留</span>' : '<span class="tag mute">内容相同</span>');
      rows.push({ size: g.size, path: p, note: links > 1 ? '硬链接' : (i === 0 ? '建议保留' : '内容相同'), html: note });
    });
  }
  const sorted = sortRows(rows, cleanState, cleanAccessors);
  t.innerHTML = `<thead><tr>${sortHeader(cleanState, cleanColsDup)}</tr></thead><tbody>` +
    sorted.map(r => `<tr>
      <td><input type="checkbox" class="clean-chk" data-path="${esc(r.path)}"></td>
      <td class="num">${fmtBytes(r.size)}</td>
      <td class="mono"><div class="clip">${esc(r.path)}</div></td>
      <td>${r.html}</td>
    </tr>`).join('') + '</tbody>';

  bindSort('#clean-table', cleanState, renderDuplicates);
  bindCleanChecks();
  bindCleanRows(tr => {
    const path = tr.querySelector('input')?.dataset.path;
    const r = rows.find(x => x.path === path);
    return r ? { name: '重复文件', size: r.size, path: r.path, note: r.note } : null;
  });
}

// bindCleanChecks wires the checkboxes and the delete button.
function bindCleanChecks() {
  const update = () => {
    const n = $$('#clean-table .clean-chk').filter(c => c.checked).length;
    $('#clean-execute').disabled = n === 0;
    $('#clean-execute').textContent = n ? `移到废纸篓 (${n})` : '移到废纸篓';
  };
  $$('#clean-table .clean-chk').forEach(c => {
    c.addEventListener('change', update);
    // Ticking a box must not open the detail of the row.
    c.addEventListener('click', (e) => e.stopPropagation());
  });
  update();
}

// selectedCleanPaths returns the paths of the checked rows.
//
// The checkbox carries the path itself, so sorting the table never breaks
// the mapping back to the scan.
function selectedCleanPaths() {
  return $$('#clean-table .clean-chk').filter(c => c.checked).map(c => c.dataset.path);
}

async function executeClean() {
  const paths = selectedCleanPaths();
  if (!paths.length) return;
  const total = paths.reduce((a, b) => a + (b.size || 0), 0);
  const ok = await modal({
    title: '确认清理',
    body: `<p>将把 ${paths.length} 项（约 ${fmtBytes(total)}）移入废纸篓：</p><pre>${esc(paths.join('\n'))}</pre>`,
    okText: '移入废纸篓', danger: true,
  });
  if (!ok) return;
  try {
    const r = await api('/api/clean/execute', { method: 'POST', body: { paths, dryRun: false } });
    toast(`已移入废纸篓 ${r.moved ?? 0} 项${r.failed?.length ? '，' + r.failed.length + ' 项失败' : ''}`, r.failed?.length ? 'err' : 'ok');
    if (r.note) toast(r.note, 'err');
    await loadClean();
  } catch (e) { toast(e.message, 'err'); }
}

// ------------------------------------------------------------------ procs

const procState = { rows: [], filter: '', sort: 'cpu', sortDesc: true, paused: false, es: null, interval: 2000 };
const ROW_H = 30;
const OVERSCAN = 8;

// procCols are the columns of the process table.
//
// Every column but the actions can be sorted, so the header does the work
// the old sort dropdown used to do.
const procCols = [
  { key: 'pid', label: 'PID', cls: 'mono' },
  { key: 'ppid', label: 'PPID', cls: 'mono' },
  { key: 'name', label: '名称' },
  { key: 'user', label: '用户' },
  { key: 'cpu', label: 'CPU%', cls: 'num' },
  { key: 'mem', label: '内存', cls: 'num' },
  { key: 'threads', label: '线程', cls: 'num' },
  { key: 'fds', label: 'fd', cls: 'num' },
  { key: 'arch', label: '架构' },
  { key: 'state', label: '状态' },
  { key: 'appName', label: '来源' },
  { key: '', label: '操作' },
];

// procAccessors reads the value of a column for the sorting.
const procAccessors = {
  pid: p => p.pid,
  ppid: p => p.ppid,
  name: p => p.name,
  user: p => p.user || '',
  cpu: p => p.cpu || 0,
  mem: p => p.mem || 0,
  threads: p => p.threads || 0,
  fds: p => p.fds || 0,
  arch: p => p.arch || '',
  state: p => p.state || '',
  appName: p => p.appName || '',
};

async function initProcs() {
  $('#procs-search').oninput = (e) => { procState.filter = e.target.value; renderProcs(); };
  $('#procs-interval').onchange = (e) => { procState.interval = +e.target.value; restartStream(); };
  $('#procs-pause').onclick = () => {
    procState.paused = !procState.paused;
    $('#procs-pause').textContent = procState.paused ? '继续' : '暂停';
    if (procState.paused) procState.es?.close();
    else restartStream();
  };
  $('#procs-snapshot').onclick = exportSnapshot;
  const wrap = $('#procs-table').closest('.table-wrap');
  wrap.addEventListener('scroll', () => renderProcRows());
  restartStream();
  // The table is filled by the stream; until the first frame arrives it
  // shows a placeholder instead of an empty box.
  showLoading('#procs-table', '正在连接实时数据…');
  // The stream stops when the user leaves the page.
  return () => {
    try { procState.es?.close(); } catch (e) { /* already closed */ }
    procState.es = null;
  };
}

function restartStream() {
  procState.es?.close();
  const es = new EventSource(`/api/procs/stream?interval=${procState.interval}`);
  procState.es = es;
  es.onmessage = (ev) => {
    $('#conn-status').classList.remove('off');
    try {
      const d = JSON.parse(ev.data);
      procState.rows = d.processes || [];
      renderProcs();
    } catch { /* ignore malformed frames */ }
  };
  es.onerror = () => { $('#conn-status').classList.add('off'); };
}

// renderProcStats fills the cards above the process table.
//
// The system overview (CPU / 内存 / 磁盘) lives on the dashboard; repeating
// it here would say nothing about the processes, so these numbers are all
// about the process table itself.
function renderProcStats() {
  const rows = procState.rows || [];
  let threads = 0, fds = 0, user = 0, system = 0, zombie = 0, rosetta = 0;
  for (const p of rows) {
    threads += p.threads || 0;
    fds += p.fds || 0;
    if (p.protected) system++; else user++;
    if (p.state === 'zombie') zombie++;
    if (p.rosetta) rosetta++;
  }
  const avg = rows.length ? Math.round(threads / rows.length) : 0;
  $('#procs-overview').innerHTML = [
    card('进程', fmtNum(rows.length), `用户 ${user} / 系统 ${system}`),
    card('线程', fmtNum(threads), `平均 ${avg} 个`),
    card('文件描述符', fmtNum(fds), '合计占用'),
    card('僵尸进程', fmtNum(zombie), zombie ? '父进程已退出' : '没有僵尸'),
    card('Rosetta', fmtNum(rosetta), 'x86 转译进程'),
  ].join('');
}

// renderProcs sorts and filters the rows, then draws the visible window.
//
// The table is virtualised: only the rows inside the viewport are in the
// DOM, so a machine with a thousand processes still scrolls smoothly.
function renderProcs() {
  const q = procState.filter.trim().toLowerCase();
  let rows = procState.rows;
  if (q) {
    rows = rows.filter(p =>
      p.name.toLowerCase().includes(q) ||
      String(p.pid) === q ||
      (p.user || '').toLowerCase().includes(q) ||
      (p.appName || '').toLowerCase().includes(q));
  }
  rows = sortRows(rows, procState, procAccessors);
  procState.sorted = rows;

  renderProcStats();

  const t = $('#procs-table');
  if (!rows.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有匹配的进程</td></tr></tbody>';
    return;
  }
  if (!t.querySelector('thead')) {
    t.innerHTML = '<thead><tr></tr></thead><tbody id="procs-body"></tbody>';
  }
  // The header is redrawn on every sort so the arrow follows the order.
  t.querySelector('thead tr').innerHTML = sortHeader(procState, procCols);
  bindSort('#procs-table', procState, renderProcs);
  renderProcRows();
}

// renderProcRows paints the rows that are inside the viewport.
function renderProcRows() {
  const rows = procState.sorted || [];
  const body = $('#procs-body');
  if (!body) return;
  const wrap = $('#procs-table').closest('.table-wrap');
  const viewH = wrap.clientHeight;
  const top = wrap.scrollTop;
  const rowH = procState.rowH || ROW_H;
  const first = Math.max(0, Math.floor(top / rowH) - OVERSCAN);
  const visible = Math.ceil(viewH / rowH) + OVERSCAN * 2;
  const last = Math.min(rows.length, first + visible);

  const padTop = first * rowH;
  const padBottom = (rows.length - last) * rowH;

  let html = `<tr class="vpad" style="height:${padTop}px"></tr>`;
  for (let i = first; i < last; i++) {
    html += procRow(rows[i]);
  }
  html += `<tr class="vpad" style="height:${padBottom}px"></tr>`;
  body.innerHTML = html;

  // The real row height is only known after the first render; measuring it
  // keeps the scroll maths exact.
  if (!procState.rowH) {
    const sample = body.querySelector('tr[data-pid]');
    if (sample) {
      const h = sample.getBoundingClientRect().height;
      if (h > 0) procState.rowH = h;
    }
  }

  body.querySelectorAll('button[data-act]').forEach(b => {
    b.addEventListener('click', (e) => {
      e.stopPropagation();
      showProcDetail(+b.closest('tr').dataset.pid);
    });
  });
  // Clicking the row opens the detail, the same way everywhere else.
  body.querySelectorAll('tr[data-pid]').forEach(tr => {
    tr.classList.add('clickable');
    tr.addEventListener('click', () => showProcDetail(+tr.dataset.pid));
  });
}

// procRow renders one process row.
function procRow(p) {
  return `<tr data-pid="${p.pid}">
    <td class="mono">${p.pid}</td>
    <td class="mono muted">${p.ppid}</td>
    <td>${p.protected ? '🔒 ' : ''}${'·'.repeat(Math.min(p.depth || 0, 6))}${esc(p.name)}${p.self ? ' <span class="tag info">本进程</span>' : ''}</td>
    <td class="muted">${esc(p.user || '')}</td>
    <td class="num">${(p.cpu ?? 0).toFixed(1)}</td>
    <td class="num">${fmtBytes(p.mem)}</td>
    <td class="num">${p.threads || '—'}</td>
    <td class="num">${p.fds || '—'}</td>
    <td class="muted">${esc(p.arch || '—')}${p.rosetta ? ' <span class="tag warn">Rosetta</span>' : ''}</td>
    <td>${p.state ? `<span class="tag ${p.state === 'running' ? 'ok' : p.state === 'zombie' ? 'bad' : 'mute'}">${esc(p.state)}</span>` : ''}</td>
    <td class="muted">${esc(p.appName || '')}</td>
    <td><button class="btn tiny" data-act="detail">详情</button></td>
  </tr>`;
}

// exportSnapshot downloads the current process table as JSON.
async function exportSnapshot() {
  const rows = procState.sorted || procState.rows;
  const blob = new Blob([JSON.stringify({ at: new Date().toISOString(), processes: rows }, null, 2)],
    { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'mymac-processes-' + Date.now() + '.json';
  a.click();
  URL.revokeObjectURL(a.href);
  toast('已导出 ' + rows.length + ' 个进程', 'ok');
}

async function showProcDetail(pid) {
  let d;
  try { d = await api('/api/procs/' + pid); } catch (e) {
    openDrawer('进程详情', `<div class="empty err">${esc(e.message)}</div>`);
    return;
  }
  const p = d.process || {};
  const env = d.environment || [];
  const ports = d.ports || [];
  openDrawer(`${p.name || pid} · pid ${pid}`, `<div class="detail-head">
      ${p.protected ? '<span class="tag bad">受保护</span>' : ''}
      ${p.rosetta ? '<span class="tag warn">Rosetta</span>' : ''}
      ${p.self ? '<span class="tag info">这是 mymac 自己</span>' : ''}
      <div class="detail-actions">
        ${p.self ? '' : `<button class="btn tiny" data-sig="TERM">SIGTERM</button>
        <button class="btn tiny" data-sig="KILL">SIGKILL</button>
        <button class="btn tiny" data-sig="STOP">SIGSTOP</button>
        <button class="btn tiny" data-sig="CONT">SIGCONT</button>`}
        <button class="btn tiny" id="proc-sample">采样</button>
      </div>
    </div>
    ${p.self ? '<div class="hint warn-banner">mymac 不能对自己发信号：被冻住后端口还占着、菜单栏图标还在，但服务已经不响应了，而且单实例锁仍然有效，起第二个实例也会被挡住。</div>' : ''}
    <dl class="kv">
      <dt>路径</dt><dd>${esc(p.path || '—')}</dd>
      <dt>参数</dt><dd>${esc((p.args || []).join(' '))}</dd>
      <dt>用户</dt><dd>${esc(p.user || '—')}</dd>
      <dt>CPU</dt><dd>${fmtPct(p.cpu)}</dd>
      <dt>内存</dt><dd>${fmtBytes(p.mem)}${p.footprint ? ' / footprint ' + fmtBytes(p.footprint) : ''}</dd>
      <dt>线程 / fd</dt><dd>${p.threads || '—'} / ${p.fds || '—'}</dd>
      <dt>状态</dt><dd>${esc(p.state || '—')}</dd>
      <dt>架构</dt><dd>${esc(p.arch || '—')}${p.rosetta ? ' (Rosetta)' : ''}</dd>
      <dt>启动时间</dt><dd>${p.startTime ? esc(new Date(p.startTime * 1000).toLocaleString()) : '—'}</dd>
      <dt>签名</dt><dd>${esc(p.signer || '—')}</dd>
    </dl>
    ${ports.length ? `<h3 style="font-size:13px;margin:14px 0 0">网络连接</h3><div class="path-list">${ports.map(x => `<div class="path-item"><span class="sp">${esc(x)}</span></div>`).join('')}</div>` : ''}
    ${env.length ? `<h3 style="font-size:13px;margin:14px 0 0">启动时环境变量</h3><div class="path-list">${env.map(x => `<div class="path-item"><span class="sp">${esc(x.name)}=${x.sensitive ? '••••••' : esc(x.value)}</span></div>`).join('')}</div>` : ''}
    <div id="sample-out"></div>`);

  $$('#drawer-body button[data-sig]').forEach(b => b.addEventListener('click', async () => {
    const sig = b.dataset.sig;
    const ok = await modal({
      title: '发送信号',
      body: `<p>向 <b>${esc(p.name)}</b> (pid ${pid}) 发送 <code>${sig}</code>？</p>`,
      okText: '发送', danger: sig === 'KILL',
    });
    if (!ok) return;
    try {
      await api(`/api/procs/${pid}/signal`, { method: 'POST', body: { sig } });
      toast(`已发送 ${sig}`, 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
  $('#proc-sample').onclick = async () => {
    $('#sample-out').innerHTML = '<div class="empty">采样中…</div>';
    try {
      const d2 = await api(`/api/procs/${pid}/sample`);
      $('#sample-out').innerHTML = `<h3 style="font-size:13px;margin:14px 0 0">线程栈采样</h3><pre class="mono" style="white-space:pre-wrap;font-size:11px;max-height:320px;overflow:auto">${esc(d2.output || '')}</pre>`;
    } catch (e) { $('#sample-out').innerHTML = `<div class="empty">${esc(e.message)}</div>`; }
  };
}

// ------------------------------------------------------------------- env

// envCols are the columns of the environment table, one set per tab.
const envCols7 = [
  { key: 'name', label: '名称' },
  { key: 'value', label: '值' },
  { key: 'note', label: '备注' },
  { key: 'shell', label: 'Shell' },
  { key: 'launchd', label: 'launchd' },
  { key: 'enabled', label: '启用' },
  { key: '', label: '操作' },
];
const envCols2 = [
  { key: 'name', label: '名称' },
  { key: 'value', label: '值' },
];
const envCols4 = [
  { key: 'name', label: '变量' },
  { key: 'value', label: '值' },
  { key: 'file', label: '来源文件' },
  { key: 'line', label: '行号', cls: 'num' },
];

// envAccessors reads the value of a column for the sorting.
const envAccessors = {
  name: v => (v.name || '').toLowerCase(),
  value: v => (v.value || '').toLowerCase(),
  note: v => (v.note || '').toLowerCase(),
  shell: v => (v.shellApplied ? 1 : 0),
  launchd: v => (v.launchdSet ? (v.launchdOk ? 2 : 1) : 0),
  enabled: v => (v.enabled ? 1 : 0),
  file: v => (v.file || '').toLowerCase(),
  line: v => v.line || 0,
};

const envState = { data: null, source: 'managed', reveal: new Set(), sort: 'name', sortDesc: false };

async function initEnv() {
  $$('#env-tabs .tab').forEach(t => t.addEventListener('click', () => {
    $$('#env-tabs .tab').forEach(x => x.classList.remove('active'));
    t.classList.add('active');
    envState.source = t.dataset.src;
    renderEnv();
  }));
  $('#env-add').onclick = () => envEditor(null);
  $('#env-import').onclick = () => envImport();
  $('#env-export').onclick = () => envExport();
  $('#env-path').onclick = () => pathEditor();
  $('#env-agent').onclick = () => envAgent();
  $('#env-backups').onclick = () => envBackups();
  await loadEnv();
}

// envAgent shows the login agent state and lets the user install it.
async function envAgent() {
  const d = envState.data || {};
  const agent = d.agent || {};
  const ok = await modal({
    title: '登录代理',
    body: `<p>macOS 重启后 <code>launchctl setenv</code> 设置的变量会丢失。登录代理会在每次登录时重新写入。</p>
      <pre>状态：${agent.installed ? '已安装' : '未安装'}${agent.loaded ? '（已加载）' : '（未加载）'}
路径：${esc(agent.path || '—')}
${esc(agent.detail || '')}</pre>
      <p class="muted">安装后会在 <code>~/Library/LaunchAgents/com.mymac.env.plist</code> 创建一个登录项。</p>`,
    okText: agent.installed ? '卸载' : '安装',
  });
  if (!ok) return;
  const action = agent.installed ? 'uninstall' : 'install';
  try {
    await api('/api/env/agent/' + action, { method: 'POST' });
    toast(action === 'install' ? '已安装登录代理' : '已卸载登录代理', 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

// envBackups lists the backups and offers a restore.
async function envBackups() {
  let d;
  try { d = await api('/api/env/backups'); } catch (e) { toast(e.message, 'err'); return; }
  const items = d.items || [];
  if (!items.length) {
    await modal({ title: '备份', body: '<p>还没有备份。每次修改 <code>~/.zshenv</code> 前会自动备份一份。</p>', okText: '知道了' });
    return;
  }
  const body = `<p>共 ${items.length} 份备份，选择一份恢复：</p>` +
    items.map((b, i) => `<label style="display:block;margin:4px 0;font-size:12.5px">
      <input type="radio" name="bk" value="${esc(b.name)}" ${i === 0 ? 'checked' : ''}>
      ${esc(b.name)} <span class="muted">(${new Date(b.created).toLocaleString()}, ${fmtBytes(b.size)})</span>
    </label>`).join('');
  const ok = await modal({ title: '备份', body, okText: '恢复' });
  if (!ok) return;
  const name = $('input[name=bk]:checked')?.value;
  if (!name) return;
  try {
    await api('/api/env/restore', { method: 'POST', body: { name } });
    toast('已恢复 ' + name, 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

async function loadEnv() {
  try {
    envState.data = await api('/api/env/vars');
    renderEnv();
  } catch (e) {
    toast(e.message, 'err');
  }
}

function renderEnv() {
  const d = envState.data || {};
  const hint = $('#env-hint');
  const marker = d.marker || {};
  const parts = [];
  if (marker.state && marker.state !== 'ok' && marker.state !== 'missing') {
    parts.push(`⚠️ <code>~/.zshenv</code> 的 mymac 标记块异常：${esc(marker.detail || '')} ` +
      `<button class="btn tiny" id="env-fix-marker">修复标记块</button>`);
  }
  parts.push('保存后：<b>新开的终端</b>和<b>新启动的应用</b>立即生效；已打开的终端需 <code>source ~/.config/mymac/env.sh</code>，已运行的应用需重启。');
  hint.innerHTML = parts.join('<br>');
  const fix = $('#env-fix-marker');
  if (fix) fix.onclick = async () => {
    const ok = await modal({
      title: '修复标记块',
      body: '<p>会用正确的标记块替换 <code>~/.zshenv</code> 中损坏的部分，<b>标记块之外的内容保持不变</b>。</p>',
      okText: '修复',
    });
    if (!ok) return;
    try {
      await api('/api/env/marker/repair', { method: 'POST' });
      toast('标记块已修复', 'ok');
      await loadEnv();
    } catch (e) { toast(e.message, 'err'); }
  };

  const t = $('#env-table');
  if (envState.source === 'managed') {
    const items = sortRows(d.managed || [], envState, envAccessors);
    t.innerHTML = `<thead><tr>${sortHeader(envState, envCols7)}</tr></thead><tbody>` +
      (items.length ? items.map(v => {
        const sensitive = v.sensitive;
        const shown = !sensitive || envState.reveal.has(v.name);
        const conflict = (v.conflicts || []).length;
        const stale = v.needsRestart || [];
        return `<tr data-name="${esc(v.name)}">
          <td class="mono">${esc(v.name)}${conflict ? ` <span class="tag warn" title="${esc((v.conflicts || []).map(c => c.source).join(', '))}">冲突</span>` : ''}</td>
          <td class="mono">${shown ? esc(v.value) : '<span class="masked">••••••••</span>'}</td>
          <td class="muted wrap">${esc(v.note || '')}${stale.length ? `<br><span class="tag mute" title="${esc(stale.join(', '))}">${stale.length} 个应用需重启</span>` : ''}</td>
          <td>${v.shellApplied ? '<span class="tag ok">已写入</span>' : '<span class="tag mute">未写入</span>'}</td>
          <td>${v.launchdSet ? (v.launchdOk ? '<span class="tag ok">已设置</span>' : '<span class="tag warn">值不一致</span>') : '<span class="tag mute">未设置</span>'}</td>
          <td><label class="tag ${v.enabled ? 'ok' : 'mute'}"><input type="checkbox" ${v.enabled ? 'checked' : ''} data-toggle> ${v.enabled ? '启用' : '停用'}</label></td>
          <td>
            <button class="btn tiny" data-act="reveal">${sensitive ? (shown ? '隐藏' : '显示') : ''}</button>
            <button class="btn tiny" data-act="edit">编辑</button>
            <button class="btn tiny danger" data-act="del">删除</button>
          </td>
        </tr>`;
      }).join('') : '<tr><td class="empty" colspan="7">还没有管理的环境变量</td></tr>') + '</tbody>';
    bindManagedRows();
  } else if (envState.source === 'launchd') {
    const items = sortRows(d.launchd || [], envState, envAccessors);
    t.innerHTML = `<thead><tr>${sortHeader(envState, envCols2)}</tr></thead><tbody>` +
      (items.length ? items.map(v => `<tr data-name="${esc(v.name)}"><td class="mono">${esc(v.name)}</td><td class="mono wrap">${esc(v.value)}</td></tr>`).join('')
        : '<tr><td class="empty" colspan="2">launchd 环境中没有变量</td></tr>') + '</tbody>';
  } else if (envState.source === 'shellFiles') {
    const files = sortRows(d.shellFiles || [], envState, envAccessors);
    t.innerHTML = `<thead><tr>${sortHeader(envState, envCols4)}</tr></thead><tbody>` +
      (files.length ? files.map(v => `<tr data-name="${esc(v.name)}"><td class="mono">${esc(v.name)}</td><td class="mono wrap">${esc(v.value)}</td><td class="mono muted">${esc(v.file)}</td><td class="num">${v.line}</td></tr>`).join('')
        : '<tr><td class="empty" colspan="4">没有在 shell 配置中找到 export</td></tr>') + '</tbody>';
  } else {
    const items = sortRows(d.self || [], envState, envAccessors);
    t.innerHTML = `<thead><tr>${sortHeader(envState, envCols2)}</tr></thead><tbody>` +
      (items.length ? items.map(v => `<tr data-name="${esc(v.name)}"><td class="mono">${esc(v.name)}</td><td class="mono wrap">${esc(v.value)}</td></tr>`).join('')
        : '<tr><td class="empty" colspan="2">无法读取</td></tr>') + '</tbody>';
  }

  // Sorting.
  bindSort('#env-table', envState, renderEnv);

  // Clicking a row opens the variable in the drawer.
  $$('#env-table tbody tr[data-name]').forEach(tr => {
    tr.classList.add('clickable');
    tr.addEventListener('click', () => {
      const list = envState.source === 'managed' ? (d.managed || [])
        : envState.source === 'launchd' ? (d.launchd || [])
        : envState.source === 'shellFiles' ? (d.shellFiles || [])
        : (d.self || []);
      const v = list.find(x => x.name === tr.dataset.name);
      if (v) showEnvDetail(envState.source, v);
    });
  });
}

// showEnvDetail opens one environment variable in the drawer.
function showEnvDetail(source, v) {
  const shown = !v.sensitive || envState.reveal.has(v.name);
  const rows = [
    ['名称', v.name],
    ['值', shown ? v.value : '••••••••'],
  ];
  if (source === 'managed') {
    rows.push(['备注', v.note || '—']);
    rows.push(['Shell 层', v.shellApplied ? '已写入 ~/.config/mymac/env.sh' : '未写入']);
    rows.push(['launchd 层', v.launchdSet ? (v.launchdOk ? '已设置' : '值不一致') : '未设置']);
    rows.push(['启用', v.enabled ? '是' : '否']);
    if ((v.needsRestart || []).length) rows.push(['需重启', v.needsRestart.join('、')]);
  }
  if (v.file) rows.push(['来源文件', v.file + (v.line ? ':' + v.line : '')]);

  const conflicts = (v.conflicts || []).map(c =>
    `<div class="path-item"><span class="sp">${esc(c.source)}</span><span class="tag warn">${esc(c.value || '')}</span></div>`).join('');

  openDrawer(v.name, `<dl class="kv">${rows.map(r =>
    `<dt>${esc(r[0])}</dt><dd>${esc(r[1])}</dd>`).join('')}</dl>` +
    (conflicts ? `<h3 style="font-size:13px;margin:14px 0 0">冲突</h3><div class="path-list">${conflicts}</div>` : ''));
}

function bindManagedRows() {
  $$('#env-table tbody tr[data-name]').forEach(tr => {
    const name = tr.dataset.name;
    $$('button[data-act]', tr).forEach(b => b.addEventListener('click', async () => {
      const act = b.dataset.act;
      if (act === 'reveal') {
        envState.reveal.has(name) ? envState.reveal.delete(name) : envState.reveal.add(name);
        renderEnv();
      } else if (act === 'edit') {
        const v = (envState.data.managed || []).find(x => x.name === name);
        envEditor(v);
      } else if (act === 'del') {
        deleteVar(name);
      }
    }));
    const tg = $('input[data-toggle]', tr);
    if (tg) tg.addEventListener('change', async () => {
      try {
        await api('/api/env/vars/' + encodeURIComponent(name), { method: 'PATCH', body: { enabled: tg.checked } });
        toast('已更新', 'ok');
        await loadEnv();
      } catch (e) { toast(e.message, 'err'); tg.checked = !tg.checked; }
    });
  });
}

async function envEditor(v) {
  const isNew = !v;
  const body = `
    <p>${isNew ? '新增一个由 mymac 管理的环境变量。' : '修改 <code>' + esc(v.name) + '</code> 的值。'}</p>
    <input class="input" id="f-name" placeholder="变量名，例如 FOO" value="${esc(v ? v.name : '')}" ${isNew ? '' : 'readonly'}>
    <input class="input" id="f-value" placeholder="值" value="${esc(v ? v.value : '')}">
    <input class="input" id="f-note" placeholder="备注（可选）" value="${esc(v ? (v.note || '') : '')}">
    <p class="muted">保存后会立即写入 <code>~/.config/mymac/env.sh</code> 并执行 <code>launchctl setenv</code>。</p>`;
  const ok = await modal({ title: isNew ? '新增环境变量' : '编辑环境变量', body, okText: '保存' });
  if (!ok) return;
  const name = $('#f-name').value.trim();
  const value = $('#f-value').value;
  const note = $('#f-note').value.trim();
  try {
    if (isNew) {
      await api('/api/env/vars', { method: 'POST', body: { name, value, note } });
    } else {
      await api('/api/env/vars/' + encodeURIComponent(name), { method: 'PATCH', body: { value, note } });
    }
    toast('已保存', 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

async function deleteVar(name) {
  const d = envState.data || {};
  const v = (d.managed || []).find(x => x.name === name);
  const layers = [];
  if (v?.shellApplied) layers.push('从 ~/.config/mymac/env.sh 移除');
  if (v?.launchdSet) layers.push('launchctl unsetenv ' + name);
  layers.push('从 ~/.config/mymac/env.json 移除');
  const ok = await modal({
    title: '删除环境变量',
    body: `<p>确认删除 <code>${esc(name)}</code>？将同时：</p><pre>${esc(layers.join('\n'))}</pre>` +
      `<p>已打开的终端需要执行 <code>unset ${esc(name)}</code>。</p>`,
    okText: '删除', danger: true,
  });
  if (!ok) return;
  try {
    await api('/api/env/vars/' + encodeURIComponent(name), { method: 'DELETE' });
    toast('已删除', 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

async function envExport() {
  const ok = await modal({
    title: '导出环境变量',
    body: `<p>选择导出格式：</p>
      <pre>.env 格式
export KEY='value'

JSON 格式
{"vars":[{"name":"KEY","value":"value"}]}</pre>`,
    okText: '下载 JSON',
  });
  if (!ok) return;
  try {
    const d = await api('/api/env/export?format=json');
    const blob = new Blob([JSON.stringify(d, null, 2)], { type: 'application/json' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'mymac-env.json';
    a.click();
    URL.revokeObjectURL(a.href);
  } catch (e) { toast(e.message, 'err'); }
}

async function envImport() {
  const ok = await modal({
    title: '导入环境变量',
    body: `<p>粘贴 JSON 或 .env 内容，导入前会先预览差异。</p>
      <textarea class="input" id="f-import" placeholder='{"vars":[{"name":"FOO","value":"bar"}]}'></textarea>`,
    okText: '预览',
  });
  if (!ok) return;
  const content = $('#f-import').value;
  if (!content.trim()) return;
  try {
    const d = await api('/api/env/import/preview', { method: 'POST', body: { content } });
    const lines = (d.add || []).map(x => '+ ' + x.name + '=' + x.value)
      .concat((d.update || []).map(x => '~ ' + x.name + '=' + x.oldValue + ' → ' + x.value));
    const ok2 = await modal({
      title: '导入预览',
      body: `<p>将新增 ${(d.add || []).length} 项，更新 ${(d.update || []).length} 项。</p><pre>${esc(lines.join('\n') || '（无变化）')}</pre>`,
      okText: '应用',
    });
    if (!ok2) return;
    await api('/api/env/import', { method: 'POST', body: { content } });
    toast('导入完成', 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

async function pathEditor() {
  let d;
  try { d = await api('/api/env/path'); } catch (e) { toast(e.message, 'err'); return; }

  // The list is edited in place; the collect callback reads the state when
  // the user confirms.
  const state = {
    prepend: (d.prepend || []).slice(),
    append: (d.append || []).slice(),
  };

  const line = (list) => list.length
    ? list.map((p, i) => `<div class="path-item">
        <span class="sp">${esc(p)}</span>
        <button class="btn tiny" data-mv="up" data-i="${i}">↑</button>
        <button class="btn tiny" data-mv="down" data-i="${i}">↓</button>
        <button class="btn tiny danger" data-mv="del" data-i="${i}">×</button>
      </div>`).join('')
    : '<div class="path-item muted">（空）</div>';

  // The list is drawn again after every change. The buttons are bound when
  // the dialog opens, because the markup does not exist before that.
  const render = () => {
    const pre = $('#p-prepend');
    const app = $('#p-append');
    if (!pre || !app) return;
    pre.innerHTML = line(state.prepend);
    app.innerHTML = line(state.append);
    $$('#p-prepend [data-mv], #p-append [data-mv]').forEach(b => {
      b.addEventListener('click', () => {
        const list = b.closest('#p-prepend') ? state.prepend : state.append;
        const i = +b.dataset.i;
        const mv = b.dataset.mv;
        if (mv === 'up' && i > 0) {
          [list[i - 1], list[i]] = [list[i], list[i - 1]];
        } else if (mv === 'down' && i < list.length - 1) {
          [list[i + 1], list[i]] = [list[i], list[i + 1]];
        } else if (mv === 'del') {
          list.splice(i, 1);
        }
        render();
      });
    });
  };

  const body = `
    <p>PATH 不会被整体覆盖。mymac 只把下面这些目录加到系统 PATH 的前面或后面，
       而且用的是 <code>export PATH='片段':"$PATH"</code> 这种动态写法，
       所以以后你自己再改 PATH 也不会被写死。</p>
    <div class="clip-row"><b style="font-size:12px">前置（优先使用）</b></div>
    <div class="path-list" id="p-prepend">${line(state.prepend)}</div>
    <div class="clip-row"><b style="font-size:12px">后置（兜底）</b></div>
    <div class="path-list" id="p-append">${line(state.append)}</div>
    <input class="input" id="p-new" placeholder="要添加的目录，例如 /opt/homebrew/bin" style="margin-top:10px">
    ${(d.missing || []).length ? `<div class="hint warn-banner" style="margin-top:8px">这些目录不存在：${esc(d.missing.join('、'))}</div>` : ''}
    <div class="hint" style="margin-top:8px">
      当前系统的 PATH（仅供参考，共 ${(d.current || '').split(':').filter(Boolean).length} 项）：<br>
      <span class="mono" style="font-size:11px;word-break:break-all">${esc(d.current || '—')}</span>
    </div>`;

  const ok = await modal({
    title: 'PATH 编辑器',
    body,
    okText: '保存',
    onOpen: render,
    collect: () => {
      const add = $('#p-new').value.trim();
      if (add) state.prepend.push(add);
      return { prepend: state.prepend, append: state.append };
    },
  });
  if (!ok) return;

  try {
    await api('/api/env/path', { method: 'POST', body: { prepend: ok.prepend, append: ok.append } });
    toast('PATH 已保存，新的终端立即生效', 'ok');
    await loadEnv();
  } catch (e) { toast(e.message, 'err'); }
}

// ------------------------------------------------------------------ boot

document.addEventListener('DOMContentLoaded', init);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    $('#conn-status').classList.add('off');
  }
});