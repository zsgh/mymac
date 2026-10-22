// The system settings: the model the AI features use, and the door to the
// interface documentation.

const settingsState = { data: null };

// initSettings fills the settings page.
async function initSettings() {
  const d = await api('/api/settings');
  settingsState.data = d;
  $('#set-baseurl').value = d.llm.baseUrl || '';
  $('#set-model').value = d.llm.model || '';
  $('#set-apikey').value = '';
  $('#set-apikey').placeholder = d.llm.hasKey ? '已保存，留空表示保持原样' : '留空表示保持原样';
  renderDocs(d.docs.enabled);
}

// renderDocs shows the state of the documentation.
function renderDocs(enabled) {
  const btn = $('#set-docs-toggle');
  btn.textContent = enabled ? '禁用' : '启用';
  btn.className = 'btn' + (enabled ? ' danger' : ' primary');
  $('#set-docs-state').textContent = enabled
    ? '现在可以访问。关掉之后这个地址会返回 404，接口本身照常工作。'
    : '已经关掉，这个地址返回 404。';
  const link = $('#set-docs-link');
  link.textContent = enabled ? 'http://127.0.0.1:8888/api/docs/' : 'http://127.0.0.1:8888/api/docs/（已禁用）';
  link.classList.toggle('off', !enabled);
}

// saveSettings writes the settings.
async function saveSettings() {
  const body = {
    llm: {
      baseUrl: $('#set-baseurl').value.trim(),
      model: $('#set-model').value.trim(),
      apiKey: $('#set-apikey').value.trim(),
    },
  };
  try {
    const d = await api('/api/settings', { method: 'PUT', body });
    settingsState.data = d;
    $('#set-apikey').value = '';
    $('#set-apikey').placeholder = d.llm.hasKey ? '已保存，留空表示保持原样' : '留空表示保持原样';
    toast('设置已保存', 'ok');
    return d;
  } catch (e) {
    toast(e.message, 'err');
    return null;
  }
}

// testModel asks the model something small, so the user knows it works.
async function testModel() {
  const status = $('#set-status');
  status.textContent = '正在测试…';
  const saved = await saveSettings();
  if (!saved || !saved.llm.baseUrl) {
    status.textContent = '先填接口地址和模型名';
    return;
  }
  try {
    // A tiny question is enough: the point is to know whether the model
    // answers at all.
    const res = await api('/api/settings/test', { method: 'POST' });
    status.textContent = res.ok ? '连上了' : '没连上';
  } catch (e) {
    status.textContent = e.message;
  }
}

// toggleDocs opens or closes the door to the documentation.
async function toggleDocs() {
  const enabled = !settingsState.data?.docs?.enabled;
  try {
    const d = await api('/api/settings/docs', { method: 'POST', body: { enabled } });
    settingsState.data = { ...settingsState.data, docs: d };
    renderDocs(d.enabled);
    toast(d.enabled ? '接口文档已启用' : '接口文档已禁用', 'ok');
  } catch (e) {
    toast(e.message, 'err');
  }
}

// aiReady reports whether a model is set up.
//
// The AI features need a model. Without one they say so instead of
// pretending to work.
async function aiReady() {
  try {
    const d = await api('/api/settings');
    settingsState.data = d;
    if (d.llm.baseUrl && d.llm.model) return true;
  } catch (e) { /* the settings are not reachable: fall through */ }
  const ok = await modal({
    title: '还没有配置大模型',
    body: '<p>AI 功能需要一个模型：填上接口地址和模型名就能用了。<br>' +
      '在「系统设置」里可以配置，支持任何兼容 OpenAI 接口的服务。</p>',
    okText: '去设置',
  });
  if (ok) showView('settings');
  return false;
}

// initSettingsPage wires the buttons of the settings page.
function initSettingsPage() {
  $('#set-save').onclick = () => saveSettings();
  $('#set-test').onclick = () => testModel();
  $('#set-docs-toggle').onclick = () => toggleDocs();
}
