let session;
let pendingCreation;
const status = document.querySelector('#status');
async function request(path, options = {}) {
  const response = await fetch(path, { ...options, headers: { ...(options.method ? { 'X-CSRF-Token': session.csrfToken } : {}), ...options.headers } });
  if (!response.ok) {
    if (response.status === 401) { session = undefined; document.querySelector('#account').hidden = true; document.querySelector('#login').hidden = false; document.querySelector('#new-token').textContent = ''; }
    const error = new Error(response.status === 401 ? '登录已失效，请重新登录。' : '操作未完成，请稍后重试。'); error.status = response.status; throw error;
  }
  return response.status === 204 ? undefined : response.json();
}
async function listTokens(cursor = '') {
  const page = await request(`/api/me/mcp-tokens?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
  const list = document.querySelector('#tokens');
  if (!cursor) list.replaceChildren();
  list.querySelector('[data-more]')?.remove();
  for (const token of page.items) {
    const item = document.createElement('li'); item.append(document.createTextNode(`${token.name} · ${{active:'有效',revoked:'已撤销',expired:'已过期'}[token.state]} `));
    if (token.state === 'active') {
      const button = document.createElement('button'); button.textContent = '撤销';
      button.onclick = async () => { try { await request(`/api/me/mcp-tokens/${encodeURIComponent(token.id)}`, { method: 'DELETE' }); await listTokens(); } catch (error) { status.textContent = error.message; } };
      item.append(button);
    }
    list.append(item);
  }
  if (page.nextCursor) {
    const item = document.createElement('li'); item.dataset.more = '';
    const more = document.createElement('button'); more.textContent = '加载更多';
    more.onclick = async () => { more.disabled = true; try { await listTokens(page.nextCursor); } catch (error) { status.textContent = error.message; more.disabled = false; } };
    item.append(more); list.append(item);
  }
}
async function revokeLostCreation(requestId) {
  let cursor = '';
  do {
    const page = await request(`/api/me/mcp-tokens?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    const token = page.items.find(item => item.createRequestId === requestId);
    if (token) { await request(`/api/me/mcp-tokens/${encodeURIComponent(token.id)}`, { method: 'DELETE' }); return; }
    cursor = page.nextCursor;
  } while (cursor);
  throw new Error('暂时无法确认上次创建的结果，请稍后重试。');
}
async function load() {
  const response = await fetch('/api/me');
  if (response.status === 401) { status.textContent = new URLSearchParams(location.search).get('login') === 'cancelled' ? '已取消登录。' : '登录后管理你的账号与访问凭据。'; document.querySelector('#login').hidden = false; return; }
  if (!response.ok) { status.textContent = '登录服务暂不可用，请稍后重试。'; return; }
  session = await response.json(); status.textContent = `你好，${session.account.displayName}`; document.querySelector('#account').hidden = false; await listTokens();
}
document.querySelector('#logout').onclick = async () => { try { await request('/api/auth/logout', { method: 'POST' }); location.replace('/'); } catch (error) { status.textContent = error.message; } };
document.querySelector('#token-form').onsubmit = async event => {
  event.preventDefault(); const button = event.currentTarget.querySelector('button'); button.disabled = true;
  pendingCreation ??= { name: document.querySelector('#token-name').value, createRequestId: crypto.randomUUID() };
  try { const result = await request('/api/me/mcp-tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(pendingCreation) }); pendingCreation = undefined; document.querySelector('#new-token').textContent = result.token; await listTokens(); }
  catch (error) {
    if (error.status === 409 && pendingCreation) {
      try { await revokeLostCreation(pendingCreation.createRequestId); pendingCreation = undefined; status.textContent = '上次创建的凭据无法恢复，已撤销，请重新创建。'; await listTokens(); }
      catch (recoveryError) { status.textContent = recoveryError.message; }
    } else { if (error.status === 400) pendingCreation = undefined; status.textContent = error.message; }
  } finally { button.disabled = false; }
};
load().catch(() => { status.textContent = '服务暂不可用，请稍后重试。'; });
