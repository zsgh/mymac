// ------------------------------------------------------------------ dash
// The dashboard uses ring charts: a filled circle with an inner disc that
// covers the middle.

const dashState = { timer: null, sort: 'name', sortDesc: false };

async function initDash() {
  $('#dash-refresh').onclick = () => loadDash();
  await loadDash();
  clearInterval(dashState.timer);
  dashState.timer = setInterval(loadDash, 4000);
  // The poll stops when the user leaves the page.
  return () => clearInterval(dashState.timer);
}

async function loadDash() {
  let o;
  try {
    o = await api('/api/system/overview');
  } catch (e) {
    return;
  }

  const gauges = [
    {
      title: 'CPU',
      pct: clamp(o.cpuPercent || 0, 0, 100),
      text: fmtPct(o.cpuPercent),
      lines: [
        ['核心', (o.cpuCount || '—') + ' 核'],
        ['进程', String(o.procCount || '—')],
        ['负载', o.loadAvg || '—'],
      ],
    },
    {
      title: '内存',
      pct: clamp(o.memPercent || 0, 0, 100),
      text: fmtPct(o.memPercent),
      lines: [
        ['已用', fmtBytes(o.memUsed)],
        ['总量', fmtBytes(o.memTotal)],
        ['压力', o.memPressure || '—'],
      ],
    },
    {
      title: '磁盘',
      pct: clamp(o.diskPercent || 0, 0, 100),
      text: fmtPct(o.diskPercent),
      lines: [
        ['已用', fmtBytes(o.diskUsed)],
        ['可用', fmtBytes(o.diskFree)],
        ['总量', fmtBytes(o.diskTotal)],
      ],
    },
    {
      title: '电池',
      pct: clamp(o.battery === null || o.battery === undefined ? 0 : o.battery, 0, 100),
      text: o.battery === null || o.battery === undefined ? '—' : o.battery + '%',
      // A full battery is good news, so the colour is inverted here.
      color: batteryColor(o.battery, o.charging),
      lines: [
        ['状态', o.charging ? '充电中' : (o.onBattery ? '使用电池' : '已接电')],
        ['循环', String(o.cycleCount || 0) + ' 次'],
        ['运行', o.uptime || '—'],
      ],
    },
  ];

  $('#dash-gauges').innerHTML = gauges.map(g => `
    <div class="gauge-card">
      <div class="gauge" style="--p:${g.pct.toFixed(1)};--gauge:${g.color || gaugeColor(g.pct)}" title="${esc(g.title)}">
        <span>${esc(g.text)}</span>
      </div>
      <div class="gauge-lines">
        <div class="gauge-title">${esc(g.title)}</div>
        ${g.lines.map(l => `<div class="gauge-line"><span>${esc(l[0])}</span><b>${esc(l[1])}</b></div>`).join('')}
      </div>
    </div>`).join('');

  const rows = [
    { name: '网络下行', value: fmtBytes(o.netIn) + '/s', num: o.netIn || 0 },
    { name: '网络上行', value: fmtBytes(o.netOut) + '/s', num: o.netOut || 0 },
    { name: '监听端口', value: String(o.portCount || '—'), num: o.portCount || 0 },
    { name: '运行时间', value: o.uptime || '—', num: 0 },
    { name: '负载', value: o.loadAvg || '—', num: 0 },
  ];
  const sorted = sortRows(rows, dashState, {
    name: r => r.name,
    value: r => r.num,
  });
  $('#dash-table').innerHTML = `<thead><tr>${sortHeader(dashState, [
    { key: 'name', label: '指标' },
    { key: 'value', label: '数值' },
  ])}</tr></thead><tbody>` +
    sorted.map(r => `<tr><td>${esc(r.name)}</td><td class="mono">${esc(r.value)}</td></tr>`).join('') +
    `</tbody>`;
  bindSort('#dash-table', dashState, loadDash);
}

// gaugeColor picks the ring colour by the load.
function gaugeColor(pct) {
  if (pct >= 88) return 'rgb(var(--red))';
  if (pct >= 70) return 'rgb(var(--amber))';
  return 'rgb(var(--accent))';
}

// batteryColor picks the ring colour for the battery, where a full battery
// is good and an empty one is bad.
function batteryColor(pct, charging) {
  if (pct === null || pct === undefined) return 'rgb(var(--text-3))';
  if (charging) return 'rgb(var(--green))';
  if (pct <= 15) return 'rgb(var(--red))';
  if (pct <= 30) return 'rgb(var(--amber))';
  return 'rgb(var(--green))';
}

// shortPath shortens a long path to its last few components.
//
// The full path stays in the tooltip, so nothing is lost.
function shortPath(p, parts = 3) {
  if (!p) return '';
  const segs = p.split('/').filter(Boolean);
  if (segs.length <= parts) return p;
  return '…/' + segs.slice(-parts).join('/');
}

function clamp(v, lo, hi) {
  v = Number(v) || 0;
  return v < lo ? lo : (v > hi ? hi : v);
}

// ----------------------------------------------------------------- ports

const portState = { data: null, search: '', filter: 'listen', sort: 'listen', sortDesc: true, system: false };

// portCols are the columns of the port table.
const portCols = [
  { key: 'pid', label: 'PID', cls: 'mono' },
  { key: 'name', label: '进程' },
  { key: 'username', label: '用户' },
  { key: 'listenCount', label: '监听端口' },
  { key: 'connCount', label: '连接数', cls: 'num' },
  { key: 'cpuPercent', label: 'CPU%', cls: 'num' },
  { key: 'memory', label: '内存', cls: 'num' },
  { key: 'system', label: '状态' },
  { key: '', label: '操作' },
];

// portAccessors reads the value of a column for the sorting.
const portAccessors = {
  pid: p => p.pid,
  name: p => (p.name || '').toLowerCase(),
  username: p => (p.username || '').toLowerCase(),
  listenCount: p => p.listenCount || 0,
  connCount: p => (p.connections || []).length,
  cpuPercent: p => p.cpuPercent || 0,
  memory: p => p.memory || 0,
  system: p => (p.decision && p.decision.is_system ? 1 : 0),
};

async function initPorts() {
  $('#ports-search').oninput = (e) => { portState.search = e.target.value; renderPorts(); };
  $('#ports-filter').onchange = (e) => { portState.filter = e.target.value; renderPorts(); };
  $('#ports-system').onchange = (e) => { portState.system = e.target.checked; loadPorts(); };
  $('#ports-rules').onclick = showPortRules;
  await loadPorts();
}

async function loadPorts() {
  showLoading('#ports-table', '正在读取端口与进程…');
  try {
    const d = await api('/api/ports/snapshot' + (portState.system ? '?include_system=1' : ''));
    portState.data = d;
    renderPorts();
  } catch (e) {
    showError('#ports-table', e.message);
  }
}

function renderPorts() {
  const d = portState.data;
  if (!d) return;
  const q = portState.search.trim().toLowerCase();
  let rows = d.processes || [];

  if (q) {
    rows = rows.filter(p =>
      p.name.toLowerCase().includes(q) ||
      String(p.pid) === q ||
      (p.username || '').toLowerCase().includes(q) ||
      p.connections.some(c => String(c.port).includes(q)));
  }
  if (portState.filter === 'listen') {
    rows = rows.filter(p => p.listenCount > 0);
  } else if (portState.filter === 'heavy') {
    rows = rows.filter(p => p.cpuPercent > 1 || p.memory > 50 * 1024 * 1024);
  }

  rows = sortRows(rows, portState, portAccessors);

  $('#ports-summary').innerHTML = [
    card('进程', String(d.stats.total || 0), '持有网络连接'),
    card('监听端口', String(d.stats.listen || 0), 'LISTEN 状态'),
    card('系统服务', String(d.stats.system || 0), '默认隐藏'),
    card('采集耗时', (d.durationMs || 0) + ' ms', '全部连接'),
  ].join('');

  const t = $('#ports-table');
  if (!rows.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有匹配的进程</td></tr></tbody>';
    return;
  }
  t.innerHTML = `<thead><tr>${sortHeader(portState, portCols)}</tr></thead><tbody>` +
    rows.map(p => {
      const listen = p.connections.filter(c => c.isListen);
      const allPorts = listen.map(c => `${c.address}:${c.port}`);
      // A process can listen on many ports; only the first few are shown.
      const shown = allPorts.slice(0, 3).join(', ');
      const more = allPorts.length > 3 ? ` +${allPorts.length - 3}` : '';
      const system = p.decision && p.decision.is_system;
      const self = !!p.self;
      return `<tr data-pid="${p.pid}" data-ct="${p.createTime}">
        <td class="mono">${p.pid}</td>
        <td>${system ? '🔒 ' : ''}${esc(p.name)}${self ? ' <span class="tag info">本进程</span>' : ''}</td>
        <td class="muted">${esc(p.username || '')}</td>
        <td class="mono"><div class="clip" title="${esc(allPorts.join(', '))}">${shown ? esc(shown) : '<span class="muted">—</span>'}${more}</div></td>
        <td class="num">${p.connections.length}</td>
        <td class="num">${(p.cpuPercent || 0).toFixed(1)}</td>
        <td class="num">${fmtBytes(p.memory)}</td>
        <td>${system ? '<span class="tag mute">系统</span>' : '<span class="tag ok">用户</span>'}</td>
        <td>
          <button class="btn tiny" data-act="detail">详情</button>
          <button class="btn tiny" data-act="stop" ${system || self ? 'disabled' : ''}>停止</button>
          <button class="btn tiny danger" data-act="kill" ${system || self ? 'disabled' : ''}>强杀</button>
          <button class="btn tiny" data-act="restart" ${system || self ? 'disabled' : ''}>重启</button>
        </td>
      </tr>`;
    }).join('') + '</tbody>';

  bindSort('#ports-table', portState, renderPorts);

  $$('#ports-table tbody tr').forEach(tr => {
    const pid = +tr.dataset.pid;
    const ct = +tr.dataset.ct;
    tr.classList.add('clickable');
    $$('button[data-act]', tr).forEach(b => b.addEventListener('click', async (e) => {
      e.stopPropagation();
      const act = b.dataset.act;
      if (act === 'detail') return showPortDetail(pid);
      await portAction(act, pid, ct);
    }));
    // Clicking the row opens the detail of the process.
    tr.addEventListener('click', () => showPortDetail(pid));
  });
}

async function portAction(action, pid, createTime) {
  const labels = { stop: '停止', kill: '强制结束', restart: '重启' };
  const ok = await modal({
    title: labels[action] + '进程',
    body: `<p>确认要${labels[action]} PID <b>${pid}</b> 吗？</p>
      <p class="muted">${action === 'restart' ? '重启会用原来的命令行、工作目录和环境变量重新启动它。' : ''}</p>`,
    okText: labels[action], danger: action === 'kill',
  });
  if (!ok) return;
  try {
    const r = await api(`/api/ports/process/${pid}/${action}`, {
      method: 'POST', body: { createTime },
    });
    if (r.timed_out && r.needs_kill) {
      toast(r.message, 'err');
    } else {
      toast(r.message || '完成', 'ok');
    }
    setTimeout(loadPorts, 600);
  } catch (e) {
    toast(e.message, 'err');
  }
}

async function showPortDetail(pid) {
  const p = (portState.data?.processes || []).find(x => x.pid === pid);
  if (!p) { openDrawer('端口详情', '<div class="empty">进程已不在列表中</div>'); return; }

  const conns = p.connections.map(c => `
    <div class="path-item">
      <span class="sp">${esc(c.protocol.toUpperCase())} ${esc(c.address)}:${c.port}${c.remoteAddress ? ' → ' + esc(c.remoteAddress) + ':' + c.remotePort : ''}</span>
      <span class="tag mute">${esc(c.state)}</span>
    </div>`).join('');

  openDrawer(`${p.name} · pid ${p.pid}`, `<div class="detail-head">
      ${p.decision?.is_system ? '<span class="tag bad">系统服务</span>' : ''}
      ${p.self ? '<span class="tag info">这是 mymac 自己</span>' : ''}
      <div class="detail-actions">
        ${p.self ? '' : `<button class="btn tiny" data-act="stop">停止</button>
        <button class="btn tiny danger" data-act="kill">强杀</button>
        <button class="btn tiny" data-act="restart">重启</button>`}
        <button class="btn tiny" id="port-add-rule">加入黑名单</button>
      </div>
    </div>
    ${p.self ? '<div class="hint warn-banner">这是 mymac 自己，不能停止或强杀：停掉它就等于把自己的界面关掉了。</div>' : ''}
    <dl class="kv">
      <dt>可执行路径</dt><dd>${esc(p.exe || '—')}</dd>
      <dt>命令行</dt><dd>${esc((p.cmdline || []).join(' '))}</dd>
      <dt>工作目录</dt><dd>${esc(p.cwd || '—')}</dd>
      <dt>用户</dt><dd>${esc(p.username || '—')}</dd>
      <dt>父进程</dt><dd>${p.ppid}</dd>
      <dt>状态</dt><dd>${esc(p.state || '—')}</dd>
      <dt>创建时间</dt><dd>${p.createTime ? esc(new Date(p.createTime).toLocaleString()) : '—'}</dd>
      <dt>CPU / 内存</dt><dd>${(p.cpuPercent || 0).toFixed(1)}% / ${fmtBytes(p.memory)}</dd>
      <dt>线程 / fd</dt><dd>${p.threads || '—'} / ${p.fds || '—'}</dd>
      <dt>判定</dt><dd>${esc(p.decision?.system_reason || (p.decision?.system_check === 'not_matched' ? '普通用户进程' : '未知'))}</dd>
    </dl>
    <h3 style="font-size:13px;margin:14px 0 6px">网络连接 (${p.connections.length})</h3>
    <div class="path-list">${conns || '<div class="path-item muted">无</div>'}</div>`);

  $$('#drawer-body button[data-act]').forEach(b => b.addEventListener('click', () => {
    portAction(b.dataset.act, pid, p.createTime);
  }));
  $('#port-add-rule').onclick = async () => {
    try {
      await api('/api/ports/rules/override', {
        method: 'POST',
        body: { mode: 'blacklist', kind: 'process', value: p.name },
      });
      toast('已加入用户黑名单', 'ok');
      loadPorts();
    } catch (e) { toast(e.message, 'err'); }
  };
}

async function showPortRules() {
  let d;
  try { d = await api('/api/ports/rules'); } catch (e) { toast(e.message, 'err'); return; }
  const wl = d.user?.whitelist || {};
  const bl = d.user?.blacklist || {};
  const line = (arr) => (arr && arr.length) ? arr.map(esc).join(', ') : '<span class="muted">（空）</span>';
  const body = `
    <p>内置规则命中 ${d.builtin?.ports?.length || 0} 个端口、${d.builtin?.processes?.length || 0} 个进程，
       用户白名单 ${(wl.ports || []).length + (wl.processes || []).length} 条，黑名单 ${(bl.ports || []).length + (bl.processes || []).length} 条。</p>
    <p class="muted">规则文件：<code>${esc(d.userPath || '')}</code>${d.loadError ? '<br>⚠️ ' + esc(d.loadError) : ''}</p>
    <div class="chip-row"><b style="font-size:12px">白名单端口</b> ${line(wl.ports)}</div>
    <div class="chip-row"><b style="font-size:12px">白名单进程</b> ${line(wl.processes)}</div>
    <div class="chip-row"><b style="font-size:12px">黑名单端口</b> ${line(bl.ports)}</div>
    <div class="chip-row"><b style="font-size:12px">黑名单进程</b> ${line(bl.processes)}</div>
    <div class="path-list" id="rule-inputs" style="margin-top:10px">
      <input class="input" id="rule-port" placeholder="端口，例如 8080">
      <input class="input" id="rule-process" placeholder="进程名，例如 my-server">
    </div>`;
  const ok = await modal({ title: '规则', body, okText: '添加白名单端口' });
  if (!ok) return;
  const port = $('#rule-port')?.value.trim();
  const proc = $('#rule-process')?.value.trim();
  try {
    if (port) await api('/api/ports/rules/override', { method: 'POST', body: { mode: 'whitelist', kind: 'port', value: port } });
    if (proc) await api('/api/ports/rules/override', { method: 'POST', body: { mode: 'whitelist', kind: 'process', value: proc } });
    toast('规则已更新', 'ok');
    loadPorts();
  } catch (e) { toast(e.message, 'err'); }
}

// showLoginDetail opens one startup item in the drawer.
function showLoginDetail(label) {
  const i = (loginState.data?.items || []).find(x => x.label === label);
  if (!i) return;
  const kindLabel = { launchAgent: 'LaunchAgent', launchDaemon: 'LaunchDaemon', loginItem: '登录项', service: '服务' };
  openDrawer(i.label, `<dl class="kv">
    <dt>类型</dt><dd>${esc(kindLabel[i.kind] || i.kind)}</dd>
    <dt>程序</dt><dd>${esc(i.program || '—')}</dd>
    <dt>参数</dt><dd>${esc((i.args || []).join(' ')) || '—'}</dd>
    <dt>来源</dt><dd>${esc(i.source || '—')}</dd>
    <dt>plist</dt><dd>${esc(i.plistPath || '—')}</dd>
    <dt>状态</dt><dd>${i.enabled ? '已启用' : '已禁用'}${i.loaded ? ' · 运行中' : ''}${i.pid ? ' · pid ' + i.pid : ''}</dd>
    <dt>禁用</dt><dd>${i.disableable
        ? '只影响下次启动：正在运行的进程不会停，登录后不再启动'
        : '旧式登录项不能禁用，请到「系统设置 → 通用 → 登录项」中移除'}</dd>
    <dt>可删除</dt><dd>${i.removable ? '可以' : '系统项，不提供删除'}</dd>
  </dl>`);
}

// ------------------------------------------------------------ login items

const loginState = { data: null, search: '', system: false, sort: 'label', sortDesc: false };

// loginCols are the columns of the startup item table.
const loginCols = [
  { key: 'label', label: '名称', cls: 'mono' },
  { key: 'kind', label: '类型' },
  { key: 'program', label: '程序' },
  { key: 'enabled', label: '状态' },
  { key: 'source', label: '来源' },
  { key: '', label: '操作' },
];

// loginAccessors reads the value of a column for the sorting.
const loginAccessors = {
  label: i => (i.label || '').toLowerCase(),
  kind: i => (i.kind || '').toLowerCase(),
  program: i => (i.program || '').toLowerCase(),
  enabled: i => (i.enabled ? 1 : 0),
  source: i => (i.source || '').toLowerCase(),
};

async function initLogin() {
  $('#login-search').oninput = (e) => { loginState.search = e.target.value; renderLogin(); };
  $('#login-system').onchange = (e) => { loginState.system = e.target.checked; renderLogin(); };
  $('#login-refresh').onclick = () => loadLogin();
  await loadLogin();
}

async function loadLogin() {
  showLoading('#login-table', '正在读取启动项…');
  try {
    const d = await api('/api/loginitems');
    loginState.data = d;
    renderLogin();
  } catch (e) {
    showError('#login-table', e.message);
  }
}

function renderLogin() {
  const d = loginState.data;
  if (!d) return;
  const q = loginState.search.trim().toLowerCase();
  // The counts describe the whole list, the search only narrows the table.
  let all = d.items || [];
  if (!loginState.system) all = all.filter(i => !i.isSystem);
  let rows = all;
  if (q) {
    rows = rows.filter(i =>
      i.label.toLowerCase().includes(q) ||
      (i.program || '').toLowerCase().includes(q) ||
      (i.groupName || '').toLowerCase().includes(q));
  }

  rows = sortRows(rows, loginState, loginAccessors);

  const kinds = {};
  for (const i of all) kinds[i.kind] = (kinds[i.kind] || 0) + 1;
  const disabled = all.filter(i => !i.enabled).length;

  $('#login-summary').innerHTML = [
    card('启动项', String(all.length), 'launchd 服务与代理'),
    card('LaunchAgent', String(kinds.launchAgent || 0), '用户级'),
    card('LaunchDaemon', String(kinds.launchDaemon || 0), '系统级'),
    card('已禁用', String(disabled), '下次登录不再启动'),
  ].join('');

  const t = $('#login-table');
  if (!rows.length) {
    t.innerHTML = '<tbody><tr><td class="empty">没有匹配的启动项</td></tr></tbody>';
    return;
  }
  const kindLabel = { launchAgent: 'LaunchAgent', launchDaemon: 'LaunchDaemon', loginItem: '登录项', service: '服务' };
  t.innerHTML = `<thead><tr>${sortHeader(loginState, loginCols)}</tr></thead><tbody>` +
    rows.map(i => `<tr data-label="${esc(i.label)}">
      <td class="mono"><div class="clip">${esc(i.label)}</div>${i.isSystem ? ' <span class="tag mute">系统</span>' : ''}</td>
      <td><span class="tag mute">${esc(kindLabel[i.kind] || i.kind)}</span></td>
      <td class="mono muted" title="${esc(i.program || i.groupName || '')}">
        <div class="clip clip-nowrap">${esc(i.groupName || shortPath(i.program) || '—')}</div>
        ${groupButton(i)}
      </td>
      <td>${i.enabled
          ? '<span class="tag ok">已启用</span>'
          : '<span class="tag bad">已禁用</span>'}${i.loaded ? ' <span class="tag info">运行中</span>' : ''}${i.pid ? ' <span class="muted mono">pid ' + i.pid + '</span>' : ''}</td>
      <td class="muted"><div class="clip">${esc(i.source || '—')}</div></td>
      <td>
        <button class="btn tiny" data-act="toggle" data-enabled="${i.enabled}"${i.disableable ? '' : ' disabled title="旧式登录项不能禁用，请到「系统设置 → 通用 → 登录项」中移除"'}>${i.enabled ? '禁用' : '启用'}</button>
        ${i.removable ? `<button class="btn tiny danger" data-act="remove" data-path="${esc(i.plistPath || '')}">删除</button>` : ''}
      </td>
    </tr>`).join('') + '</tbody>';

  bindSort('#login-table', loginState, renderLogin);

  $$('#login-table tbody tr').forEach(tr => {
    const label = tr.dataset.label;
    tr.classList.add('clickable');
    // Clicking the row opens the details of the startup item.
    tr.addEventListener('click', () => showLoginDetail(label));
    $$('button[data-act]', tr).forEach(b => b.addEventListener('click', async (e) => {
      e.stopPropagation();
      if (b.dataset.act === 'toggle') {
        const enabled = b.dataset.enabled !== 'true';
        try {
          await api('/api/loginitems/state', { method: 'POST', body: { label, enabled } });
          toast(enabled ? '已启用' : '已禁用', 'ok');
          loadLogin();
        } catch (e) { toast(e.message, 'err'); }
      } else if (b.dataset.act === 'group') {
        await toggleGroup(b.dataset.group, b.dataset.enable === 'true');
      } else if (b.dataset.act === 'remove') {
        const ok = await modal({
          title: '删除启动项',
          body: `<p>确认删除 <code>${esc(b.dataset.path)}</code>？文件会移入废纸篓，可随时恢复。</p>`,
          okText: '删除', danger: true,
        });
        if (!ok) return;
        try {
          await api('/api/loginitems/remove', { method: 'POST', body: { path: b.dataset.path } });
          toast('已移入废纸篓', 'ok');
          loadLogin();
        } catch (e) { toast(e.message, 'err'); }
      }
    }));
  });
}

// groupButton renders the button that turns every startup item of one
// program on or off at once.
//
// One app often brings several startup items with it, and the user thinks
// of the program, not of the individual labels.
function groupButton(i) {
  if (!i.groupKey || i.groupSize < 2) return '';
  const disable = i.groupOn > 0;
  return `<button class="btn tiny" data-act="group" data-group="${esc(i.groupKey)}" data-enable="${!disable}">` +
    `${disable ? '全部禁用' : '全部启用'} <span class="muted">${i.groupSize}</span></button>`;
}

// toggleGroup turns every startup item of one program on or off.
async function toggleGroup(group, enabled) {
  try {
    const r = await api('/api/loginitems/group/state', { method: 'POST', body: { group, enabled } });
    const name = r.name || '这个程序';
    if (r.failed && r.failed.length) {
      toast(`${name}：改了 ${r.changed} 项，失败 ${r.failed.length} 项`, 'err');
    } else if (r.skipped && r.skipped.length) {
      toast(`${name}：改了 ${r.changed} 项，跳过 ${r.skipped.length} 项旧式登录项`, 'ok');
    } else {
      toast(`${name}：${enabled ? '已启用' : '已禁用'} ${r.changed} 项`, 'ok');
    }
    loadLogin();
  } catch (e) { toast(e.message, 'err'); }
}
