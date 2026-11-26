// IsoGate 独立登录页逻辑。
const API_BASE = window.GATEWAY_API_BASE || location.origin + '/api/v1';
const params = new URLSearchParams(location.search);
const rawNext = params.get('next');
const NEXT = rawNext && (rawNext.startsWith('/') || rawNext.startsWith('./')) && !rawNext.startsWith('//') ? rawNext : './';

const $ = (id) => document.getElementById(id);
let mode = 'login';

function setMode(next) {
  mode = next;
  const isLogin = mode === 'login';
  $('tab-login').className = isLogin
    ? 'px-4 py-1.5 rounded-lg font-medium bg-indigo-500/25 text-white border border-indigo-400/30'
    : 'px-4 py-1.5 rounded-lg font-medium text-slate-400 border border-transparent hover:text-white';
  $('tab-register').className = !isLogin
    ? 'px-4 py-1.5 rounded-lg font-medium bg-indigo-500/25 text-white border border-indigo-400/30'
    : 'px-4 py-1.5 rounded-lg font-medium text-slate-400 border border-transparent hover:text-white';
  $('submit-label').textContent = isLogin ? '登录控制台' : '注册并进入控制台';
  $('password').autocomplete = isLogin ? 'current-password' : 'new-password';
  hideError();
}

function showError(msg) {
  const el = $('auth-error');
  el.textContent = msg;
  el.classList.remove('hidden');
}

function hideError() {
  $('auth-error').classList.add('hidden');
}

async function api(path, body) {
  const r = await fetch(API_BASE + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  let data = {};
  try { data = await r.json(); } catch { /* ignore */ }
  if (!r.ok) throw new Error(data.error || ('HTTP ' + r.status));
  return data;
}

async function verifyExistingToken(token) {
  try {
    const r = await fetch(API_BASE + '/auth/me', {
      headers: { Authorization: 'Bearer ' + token },
    });
    if (r.ok) {
      location.replace(NEXT);
      return true;
    }
  } catch { /* 网络异常时留在登录页重试 */ }
  localStorage.removeItem('gw_token');
  return false;
}

async function submit(e) {
  e.preventDefault();
  hideError();
  const email = $('email').value.trim();
  const password = $('password').value;
  if (!email || password.length < 8) {
    showError('请填写邮箱和至少 8 位密码');
    return;
  }
  const btn = $('submit-btn');
  btn.disabled = true;
  const original = $('submit-label').textContent;
  $('submit-label').textContent = '处理中...';
  try {
    const res = await api('/auth/' + mode, { email, password });
    localStorage.setItem('gw_token', res.token);
    location.replace(NEXT);
  } catch (err) {
    showError(err.message || '登录失败，请重试');
    btn.disabled = false;
    $('submit-label').textContent = original;
  }
}

document.addEventListener('DOMContentLoaded', async () => {
  $('tab-login').addEventListener('click', () => setMode('login'));
  $('tab-register').addEventListener('click', () => setMode('register'));
  $('auth-form').addEventListener('submit', submit);

  const token = localStorage.getItem('gw_token');
  if (token) await verifyExistingToken(token);
  if (window.lucide) window.lucide.createIcons();
});
