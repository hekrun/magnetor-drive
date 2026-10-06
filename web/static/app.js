'use strict';

const $ = (sel) => document.querySelector(sel);
let csrf = '';
let cwd = '';
let selected = null;
let entries = [];

async function api(method, url, body, isForm) {
  const opts = { method, headers: {}, credentials: 'same-origin' };
  if (method !== 'GET') opts.headers['X-CSRF-Token'] = csrf;
  if (body !== undefined && !isForm) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  } else if (isForm) {
    opts.body = body;
  }
  const res = await fetch(url, opts);
  let data = null;
  try { data = await res.json(); } catch (e) { /* no body */ }
  if (res.status === 401 && url !== '/api/login') { showLogin(); throw new Error('Please sign in'); }
  if (!res.ok) throw new Error((data && data.error) || 'Request failed');
  return data;
}

function setStatus(msg) { $('#status').textContent = msg || ''; }
function guard(fn) { return async (...a) => { try { setStatus(''); await fn(...a); } catch (e) { setStatus(e.message); } }; }

function showLogin() {
  $('#app').hidden = true;
  $('#login').hidden = false;
}
function showApp() {
  $('#login').hidden = true;
  $('#app').hidden = false;
}

function fmtSize(n) {
  if (n < 1024) return n + ' B';
  const u = ['KiB', 'MiB', 'GiB', 'TiB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return n.toFixed(1) + ' ' + u[i];
}
function join(dir, name) { return dir ? dir + '/' + name : name; }
function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}
const q = (p) => encodeURIComponent(p);

function renderCrumbs() {
  const nav = $('#crumbs');
  nav.replaceChildren();
  const root = el('button', 'Home');
  root.onclick = () => load('');
  nav.append(root);
  let acc = '';
  for (const seg of cwd.split('/').filter(Boolean)) {
    acc = join(acc, seg);
    const target = acc;
    nav.append(document.createTextNode(' / '));
    const b = el('button', seg);
    b.onclick = () => load(target);
    nav.append(b);
  }
}

function select(e) {
  selected = e;
  for (const tr of document.querySelectorAll('#files tbody tr')) {
    tr.classList.toggle('selected', !!e && tr.dataset.path === e.path);
  }
  for (const b of document.querySelectorAll('[data-action]')) {
    const a = b.dataset.action;
    b.disabled = !e || ((a === 'download' || a === 'preview') && e.isDir);
  }
}

async function load(path) {
  const data = await api('GET', '/api/files?path=' + q(path));
  cwd = data.path;
  entries = data.entries;
  selected = null;
  renderCrumbs();
  const tbody = $('#files tbody');
  tbody.replaceChildren();
  for (const e of entries) {
    const tr = el('tr');
    tr.dataset.path = e.path;
    tr.append(el('td', (e.isDir ? '\u{1F4C1} ' : '\u{1F4C4} ') + e.name), el('td', e.isDir ? '' : fmtSize(e.size)), el('td', new Date(e.modTime).toLocaleString()));
    tr.onclick = () => select(e);
    tr.ondblclick = guard(async () => { if (e.isDir) await load(e.path); else preview(e); });
    tbody.append(tr);
  }
  select(null);
}

function preview(e) {
  const url = '/api/preview?path=' + q(e.path);
  const body = $('#preview-body');
  body.replaceChildren();
  const ext = e.name.split('.').pop().toLowerCase();
  let node;
  if (['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext)) { node = el('img'); node.src = url; node.alt = e.name; }
  else if (['mp4', 'webm'].includes(ext)) { node = el('video'); node.src = url; node.controls = true; }
  else if (['mp3', 'wav', 'ogg'].includes(ext)) { node = el('audio'); node.src = url; node.controls = true; }
  else if (['pdf', 'txt', 'md', 'json', 'log', 'csv'].includes(ext)) { node = el('iframe'); node.src = url; node.title = e.name; }
  else { setStatus('No preview available for this file type; use Download.'); return; }
  $('#preview-name').textContent = e.name;
  body.append(node);
  $('#preview').hidden = false;
}

const actions = {
  download: (e) => { window.location.href = '/api/download?path=' + q(e.path); },
  preview,
  async rename(e) {
    const name = prompt('New name', e.name);
    if (!name || name === e.name) return;
    await api('POST', '/api/rename', { path: e.path, name });
    await load(cwd); await loadTorrents();
  },
  async move(e) {
    const dest = prompt('Move to folder (path, empty for Home)', cwd);
    if (dest === null) return;
    await api('POST', '/api/move', { path: e.path, dest });
    await load(cwd); await loadTorrents();
  },
  async copy(e) {
    const dest = prompt('Copy to folder (path, empty for Home)', cwd);
    if (dest === null) return;
    await api('POST', '/api/copy', { path: e.path, dest });
    await load(cwd); await loadTorrents();
  },
  async delete(e) {
    if (!confirm('Delete "' + e.name + '"' + (e.isDir ? ' and everything inside it' : '') + '?')) return;
    await api('POST', '/api/delete', { path: e.path });
    await load(cwd); await loadTorrents();
  },
  async torrent(e) {
    setStatus('Creating torrent (hashing files)\u2026');
    await api('POST', '/api/torrents', { path: e.path });
    setStatus('');
    await loadTorrents();
  },
};

async function loadTorrents() {
  const data = await api('GET', '/api/torrents');
  const box = $('#torrent-list');
  box.replaceChildren();
  if (!data.torrents.length) { box.append(el('p', 'No torrents yet.', 'hint')); return; }
  for (const t of data.torrents) {
    const card = el('div', undefined, 'torrent');
    const head = el('div');
    head.append(el('strong', t.name + ' '), el('span', t.state, 'badge ' + t.state));
    card.append(head);
    card.append(el('div', '/' + t.path + ' \u00b7 ' + fmtSize(t.size) + (t.state === 'seeding' ? ' \u00b7 peers: ' + t.peers + ' \u00b7 uploaded: ' + fmtSize(t.uploaded) : ''), 'hint'));
    if (t.state === 'invalid') card.append(el('div', 'Source changed. Select the item and choose "Create torrent" to regenerate.', 'error'));
    card.append(el('code', t.magnet));
    const row = el('div', undefined, 'actions');
    const btn = (label, fn) => { const b = el('button', label); b.onclick = guard(async () => { await fn(); await loadTorrents(); }); row.append(b); };
    btn('Copy magnet', () => navigator.clipboard.writeText(t.magnet));
    btn('Download .torrent', async () => { window.location.href = '/api/torrents/' + t.id + '/file'; });
    if (t.state === 'seeding' || t.state === 'verifying') btn('Stop seeding', () => api('POST', '/api/torrents/' + t.id + '/stop', {}));
    if (t.state === 'stopped') btn('Start seeding', () => api('POST', '/api/torrents/' + t.id + '/start', {}));
    btn('Remove', () => api('DELETE', '/api/torrents/' + t.id));
    card.append(row);
    box.append(card);
  }
}

async function start() {
  const s = await api('GET', '/api/session');
  csrf = s.csrf;
  showApp();
  await load('');
  await loadTorrents();
}

$('#login-form').onsubmit = async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  try {
    const s = await api('POST', '/api/login', { username: f.get('username'), password: f.get('password') });
    csrf = s.csrf;
    ev.target.reset();
    $('#login-error').textContent = '';
    await start();
  } catch (e) { $('#login-error').textContent = e.message; }
};
$('#logout').onclick = guard(async () => { await api('POST', '/api/logout', {}); csrf = ''; showLogin(); });
$('#new-folder').onclick = guard(async () => {
  const name = prompt('Folder name');
  if (!name) return;
  await api('POST', '/api/folders', { path: cwd, name });
  await load(cwd);
});
$('#upload-btn').onclick = () => $('#upload-input').click();
$('#upload-input').onchange = guard(async (ev) => {
  for (const file of ev.target.files) {
    const fd = new FormData();
    fd.append('file', file, file.name);
    setStatus('Uploading ' + file.name + '\u2026');
    await api('POST', '/api/upload?path=' + q(cwd), fd, true);
  }
  ev.target.value = '';
  setStatus('');
  await load(cwd); await loadTorrents();
});
for (const b of document.querySelectorAll('[data-action]')) {
  b.onclick = guard(async () => { if (selected) await actions[b.dataset.action](selected); });
}
$('#preview-close').onclick = () => { $('#preview').hidden = true; $('#preview-body').replaceChildren(); };

setInterval(() => { if (!$('#app').hidden) loadTorrents().catch(() => {}); }, 4000);
start().catch(showLogin);
