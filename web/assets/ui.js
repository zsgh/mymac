// ------------------------------------------------------------------ sorting
//
// Every table in mymac can be sorted by clicking a column header. The
// current order lives in the view state, so a re-render keeps it and the
// arrow in the header shows which column is active.

// sortHeader renders the header cells of a table.
//
// cols is a list of {key, label, cls}. A column without a key is not
// sortable: it gets no arrow and no click handler.
function sortHeader(state, cols) {
  return cols.map(c => {
    const cls = [c.key ? 'sortable' : '', c.cls || ''].filter(Boolean).join(' ');
    const arrow = state.sort === c.key ? (state.sortDesc ? ' ▾' : ' ▴') : '';
    const attr = c.key ? ` data-sort="${esc(c.key)}"` : '';
    return `<th class="${cls}"${attr}>${esc(c.label)}${arrow}</th>`;
  }).join('');
}

// sortRows sorts the rows by the current sort key.
//
// accessors maps a key to a function that reads the value of a row. Text is
// compared as text, everything else as a number.
function sortRows(rows, state, accessors) {
  const get = accessors[state.sort];
  if (!get) return rows;
  const dir = state.sortDesc ? -1 : 1;
  return rows.slice().sort((a, b) => {
    const va = get(a);
    const vb = get(b);
    if (typeof va === 'string' || typeof vb === 'string') {
      return String(va ?? '').localeCompare(String(vb ?? ''), 'zh-Hans-CN') * dir;
    }
    return ((va ?? 0) - (vb ?? 0)) * dir;
  });
}

// bindSort wires the header clicks of a table.
//
// The render callback runs after every change, so the table is redrawn in
// the new order.
function bindSort(tableSel, state, render) {
  $$(`${tableSel} th.sortable`).forEach(th => th.addEventListener('click', () => {
    const k = th.dataset.sort;
    if (!k) return;
    if (state.sort === k) state.sortDesc = !state.sortDesc;
    else { state.sort = k; state.sortDesc = false; }
    render();
  }));
}

// ------------------------------------------------------------------ drawer
//
// Details open in a drawer that slides in from the right. Every table that
// shows details uses the same one, so a detail always appears in the same
// place and never pushes the table around.

let drawerAfterClose = null;

// openDrawer shows a detail in the drawer.
//
// The title is plain text; the body is HTML. The body is filled first and
// the drawer shown afterwards, so the content is never seen half-rendered.
function openDrawer(title, html) {
  $('#drawer-title').textContent = title;
  $('#drawer-body').innerHTML = html;
  $('#drawer').hidden = false;
  $('#drawer-backdrop').hidden = false;
  document.body.classList.add('drawer-open');
}

// closeDrawer hides the drawer again.
function closeDrawer() {
  const d = $('#drawer');
  if (!d || d.hidden) return;
  d.hidden = true;
  $('#drawer-backdrop').hidden = true;
  document.body.classList.remove('drawer-open');
  const f = drawerAfterClose;
  drawerAfterClose = null;
  if (f) f();
}

// onDrawerClose registers a callback that runs when the drawer closes.
//
// The process table uses it to drop the selection of a row that is gone.
function onDrawerClose(fn) { drawerAfterClose = fn; }

// initDrawer wires the drawer chrome.
function initDrawer() {
  $('#drawer-close').onclick = closeDrawer;
  $('#drawer-backdrop').onclick = closeDrawer;
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeDrawer();
  });
}

// ------------------------------------------------------------------- theme
//
// The interface follows the system appearance by default. The user can
// force light or dark; the choice is remembered.

const THEME_KEY = 'mymac.theme';

// initTheme applies the stored appearance and wires the switcher.
function initTheme() {
  const sel = $('#theme-select');
  if (!sel) return;
  const saved = localStorage.getItem(THEME_KEY) || 'system';
  sel.value = saved;
  applyTheme(saved);
  sel.onchange = () => {
    localStorage.setItem(THEME_KEY, sel.value);
    applyTheme(sel.value);
  };
  // The system appearance can change while the page is open.
  const mq = matchMedia('(prefers-color-scheme: dark)');
  if (mq.addEventListener) {
    mq.addEventListener('change', () => {
      if ((localStorage.getItem(THEME_KEY) || 'system') === 'system') applyTheme('system');
    });
  }
}

// applyTheme sets the appearance.
//
// The CSS only knows "light" and "dark"; "system" is resolved here so the
// page follows the system without the stylesheet doing the work twice.
function applyTheme(mode) {
  const dark = mode === 'dark' ||
    (mode !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
}
