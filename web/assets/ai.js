// The AI features: one button per page, one cache on disk.
//
// Every page has its own prompt: the same model answers a completely
// different question about a cache folder than about a login item.

/** aiBadge renders the verdict of the AI about one item. */
function aiBadge(v) {
  if (!v) return '';
  const cls = v.level === 'high' ? 'bad' : v.level === 'medium' ? 'warn' : 'ok';
  const tip = v.reason + (v.advice ? '\n' + v.advice : '');
  return `<span class="tag ${cls}" title="${esc(tip)}">${esc(v.label || 'AI')}</span>`;
}

/** aiHint renders the full verdict of the AI, for a drawer or a panel. */
function aiHint(v) {
  if (!v) return '';
  const cls = v.level === 'high' ? 'bad' : v.level === 'medium' ? 'warn' : 'ok';
  let out = `<div class="ai-hint"><span class="tag ${cls}">${esc(v.label || 'AI')}</span>`;
  if (v.reason) out += `<div>${esc(v.reason)}</div>`;
  if (v.advice) out += `<div class="muted">${esc(v.advice)}</div>`;
  out += '</div>';
  return out;
}

/**
 * askAI lets the user choose how much the model is asked.
 *
 * A full rebuild asks about everything; the cache only asks about the items
 * that have no verdict yet, which is much faster.
 */
function askAI(analysis) {
  let chosen = null;
  return modal({
    title: 'AI 分析',
    body: `<p>让 AI 评估这一页的内容。分析在后台跑，结论会存在本机，
      下次直接看缓存就行。</p>
      <div class="ai-modes">
        <button class="btn" data-mode="cache"><b>利用缓存</b><span class="muted">只问还没分析过的项，快</span></button>
        <button class="btn" data-mode="full"><b>全量重建</b><span class="muted">重新问一遍所有项，慢</span></button>
      </div>`,
    okText: '关闭',
    onOpen() {
      // The dialog is closed by picking a mode or by cancelling; the
      // ordinary confirm button has nothing to confirm.
      $('#modal-ok').style.display = 'none';
      $$('#modal-body .ai-modes button').forEach(b => b.addEventListener('click', () => {
        chosen = b.dataset.mode;
        $('#modal-ok').click();
      }));
    },
    collect() {
      // The dialog stays open until a mode is picked.
      return chosen || false;
    },
  }).then(ok => (ok ? chosen : null));
}

/** waitForAI waits until an analysis is done. */
async function waitForAI(analysis, btn, reload) {
  const started = Date.now();
  let last = -1;
  for (let i = 0; i < 300; i++) {
    await new Promise(r => setTimeout(r, 2000));
    let st = {};
    try {
      const res = await api('/api/ai/status?analysis=' + encodeURIComponent(analysis));
      st = (res.analyses || [])[0] || {};
    } catch (e) { return 'error'; }
    if (st.error) {
      toast(st.error, 'err');
      return 'error';
    }
    // The analysis goes in batches: the verdicts show up as they come in.
    if (st.count !== last) {
      last = st.count;
      if (reload) await reload();
    }
    if (!st.running) {
      return st.count > 0 ? 'done' : 'error';
    }
    const sec = Math.round((Date.now() - started) / 1000);
    if (btn) btn.textContent = `分析中… ${sec}s`;
  }
  return 'timeout';
}

/**
 * runAI starts an analysis of a page and waits for it.
 *
 * The button shows how long the analysis has been running: a thinking model
 * needs a couple of minutes for a long list, and a silent spinner is worse
 * than a slow one.
 */
async function runAI(analysis, btn, reload) {
  if (!await aiReady()) return;
  const mode = await askAI(analysis);
  if (!mode) return;
  btn.disabled = true;
  btn.textContent = '分析中…';
  try {
    await api('/api/ai/analyze', { method: 'POST', body: { analysis, mode } });
    toast('AI 正在看这一页，稍等一下', 'ok');
    const done = await waitForAI(analysis, btn, reload);
    if (done === 'error') {
      toast('AI 分析没成功', 'err');
    } else if (done === 'timeout') {
      toast('AI 还在跑，可以再等等', 'err');
    } else {
      toast('AI 分析完成', 'ok');
    }
  } catch (e) {
    toast(e.message, 'err');
  }
  btn.disabled = false;
  btn.textContent = 'AI 分析';
  if (reload) await reload();
}
