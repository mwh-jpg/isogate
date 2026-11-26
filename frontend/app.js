// IsoGate 租户 / 管理员控制台。静态部署，直接调用网关 API。
const API_BASE = window.GATEWAY_API_BASE || location.origin + '/api/v1';
const PUBLIC_BASE = window.GATEWAY_PUBLIC_URL || location.origin;
const AUDIT_URL = window.GATEWAY_AUDIT_URL || location.origin + '/api/audit/verify';

const $ = (id) => document.getElementById(id);
let token = localStorage.getItem('gw_token') || '';
let overviewChart = null;

const state = {
  currentPage: 'overview',
  chartMode: 'tokens',
  snippetTab: 'claude-code',
  selectedSnippetKeyId: '',
  logFilter: { query: '', status: 'all' },
  user: null,
  proxies: [],
  accounts: [],
  keys: [],
  logs: [],
  stats: { requests: 0, tokens: 0, avg_ttft_ms: 0, model_mismatch: 0 },
  chain: { ok: null, total: 0 },
  admin: { overview: null, users: [], accounts: [], proxies: [], logs: [], ipPool: [], poolStats: {} },
};

const PAGE_META = {
  overview: { title: '总览看板', subtitle: '单租户链路健康、Prompt Cache 命中率与官方转发审计日志实时监控' },
  proxies: { title: '专属静态 IP', subtitle: '每个上游账号独占一条系统分配的静态出口 IP，账号之间不共用，用户无需也无法自行配置' },
  accounts: { title: '上游账号与隔离绑定', subtitle: 'AES-256-GCM 加密存储凭证；创建账号时自动分配独立静态 IP 并强制 Fail-Closed' },
  keys: { title: '网关 API Key 与快速接入', subtitle: '签发绑定单租户上游的 sk-iso 密钥，一键生成 Claude Code / SDK 配置' },
  logs: { title: '官方转发审计日志', subtitle: '逐笔记录请求模型、上游实际模型、request-id 与出口 IP，可完整核验' },
  admin: { title: '管理后台', subtitle: '用户套餐、静态 IP 分配、全局审计与网关健康统一管理' },
};

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------
function esc(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function isAdmin() {
  return state.user && state.user.role === 'admin';
}

function fmtTime(value) {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleString('zh-CN', { hour12: false });
}

function shortTime(value) {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleTimeString('zh-CN', { hour12: false });
}

function icons(nodes) {
  if (window.lucide) window.lucide.createIcons({ nodes });
}

function showToast(message, type = 'info') {
  const container = $('toast-container');
  const styles = {
    success: 'bg-[#0a1914]/95 text-emerald-200 border-emerald-500/35 shadow-emerald-950/50',
    error: 'bg-[#1c0c12]/95 text-rose-200 border-rose-500/35 shadow-rose-950/50',
    warning: 'bg-[#1c150a]/95 text-amber-200 border-amber-500/35 shadow-amber-950/50',
    info: 'bg-[#0f1320]/95 text-slate-200 border-indigo-500/35 shadow-black/60',
  };
  const icon = { success: 'check-circle-2', error: 'alert-octagon', warning: 'alert-triangle', info: 'info' }[type] || 'info';
  const el = document.createElement('div');
  el.className = `pointer-events-auto backdrop-blur-xl flex items-start gap-2.5 px-4 py-3 rounded-xl border shadow-2xl text-xs font-medium transition-all duration-200 ${styles[type] || styles.info}`;
  el.innerHTML = `<i data-lucide="${icon}" class="w-4 h-4 shrink-0 mt-0.5"></i><span class="leading-relaxed">${esc(message)}</span>`;
  container.appendChild(el);
  icons([el]);
  setTimeout(() => {
    el.style.opacity = '0';
    el.style.transform = 'translateY(8px)';
    setTimeout(() => el.remove(), 220);
  }, 3600);
}

function copyText(text, label = '内容') {
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text)
      .then(() => showToast(`已复制${label}`, 'success'))
      .catch(() => fallbackCopy(text, label));
  } else {
    fallbackCopy(text, label);
  }
}

function fallbackCopy(text, label) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try {
    document.execCommand('copy');
    showToast(`已复制${label}`, 'success');
  } catch {
    showToast('复制失败，请手动选中复制', 'error');
  }
  ta.remove();
}

async function api(path, opts = {}) {
  const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
  if (token) headers.Authorization = 'Bearer ' + token;
  const r = await fetch(API_BASE + path, { ...opts, headers });
  let data = {};
  try { data = await r.json(); } catch { /* empty */ }
  if (!r.ok) {
    const err = new Error(data.error || ('HTTP ' + r.status));
    err.status = r.status;
    throw err;
  }
  return data;
}

async function apiPublic(url) {
  const r = await fetch(url);
  return r.json().catch(() => ({ ok: null }));
}

function bindSpotlightEffect(scope) {
  scope.querySelectorAll('.spotlight-card').forEach((card) => {
    if (card.dataset.spotlightBound) return;
    card.dataset.spotlightBound = '1';
    card.addEventListener('mousemove', (e) => {
      const rect = card.getBoundingClientRect();
      card.style.setProperty('--mx', `${e.clientX - rect.left}px`);
      card.style.setProperty('--my', `${e.clientY - rect.top}px`);
    });
  });
}

function providerMeta(provider) {
  return {
    anthropic_api: { label: 'Anthropic Official API', badge: 'bg-indigo-500/15 text-indigo-300 border-indigo-400/25' },
    aws_bedrock: { label: 'AWS Bedrock', badge: 'bg-amber-500/15 text-amber-300 border-amber-400/25' },
    vertex_ai: { label: 'Google Vertex AI', badge: 'bg-sky-500/15 text-sky-300 border-sky-400/25' },
    custom_anthropic: { label: 'Custom Anthropic', badge: 'bg-white/[0.06] text-slate-300 border-white/[0.12]' },
  }[provider] || { label: provider || '-', badge: 'bg-white/[0.06] text-slate-300 border-white/[0.12]' };
}

function healthBadge(status) {
  if (status === 'healthy') {
    return '<span class="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[11px] font-medium bg-emerald-500/12 text-emerald-300 border border-emerald-500/30"><span class="w-1.5 h-1.5 rounded-full bg-emerald-400 shadow-[0_0_8px_#34d399]"></span>健康</span>';
  }
  if (status === 'unhealthy') {
    return '<span class="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[11px] font-medium bg-rose-500/12 text-rose-300 border border-rose-500/30"><span class="w-1.5 h-1.5 rounded-full bg-rose-400 shadow-[0_0_8px_#fb7185]"></span>不可达</span>';
  }
  return '<span class="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-[11px] font-medium bg-white/[0.05] text-slate-400 border border-white/[0.1]"><span class="w-1.5 h-1.5 rounded-full bg-slate-400"></span>未检测</span>';
}

// 一账号一 IP：优先按 proxy.upstream_account_id 精确匹配；
// legacy 出口（尚无 account_id）按 account.proxy_id 兜底。
function proxyForAccount(accountId) {
  const byOwner = state.proxies.find((p) => p.upstream_account_id === accountId);
  if (byOwner) return byOwner;
  const acc = getAccount(accountId);
  return acc?.proxy_id ? state.proxies.find((p) => p.id === acc.proxy_id) || null : null;
}

function getAccount(id) { return state.accounts.find((a) => a.id === id); }
function getKey(id) { return state.keys.find((k) => k.id === id); }

function openModal({ title, bodyHtml, footerHtml }) {
  $('modal-title').textContent = title;
  $('modal-body').innerHTML = bodyHtml;
  $('modal-footer').innerHTML = footerHtml;
  const root = $('modal-root');
  root.classList.remove('hidden');
  root.classList.add('flex');
  icons([root]);
}

function closeModal() {
  const root = $('modal-root');
  root.classList.add('hidden');
  root.classList.remove('flex');
}

// ---------------------------------------------------------------------------
// Auth & data
// ---------------------------------------------------------------------------
function goLogin() {
  const next = encodeURIComponent(location.pathname + location.search);
  location.replace('login.html?next=' + next);
}

function logout() {
  token = '';
  localStorage.removeItem('gw_token');
  state.user = null;
  state.proxies = [];
  state.accounts = [];
  state.keys = [];
  state.logs = [];
  state.admin = { overview: null, users: [], accounts: [], proxies: [], logs: [], ipPool: [], poolStats: {} };
  location.replace('login.html');
}

async function loadData() {
  if (!token) { goLogin(); return; }
  try {
    const [me, proxies, accounts, keys, logs, stats] = await Promise.all([
      api('/auth/me'),
      api('/proxies'),
      api('/accounts'),
      api('/keys'),
      api('/logs?limit=200'),
      api('/stats/overview'),
    ]);
    state.user = me;
    state.proxies = Array.isArray(proxies) ? proxies : [];
    state.accounts = Array.isArray(accounts) ? accounts : [];
    state.keys = Array.isArray(keys) ? keys : [];
    state.logs = Array.isArray(logs) ? logs : [];
    state.stats = stats || state.stats;
    state.chain = await apiPublic(AUDIT_URL).catch(() => ({ ok: null }));

    if (isAdmin()) {
      const [overview, users, adminProxies, adminLogs, pool, adminAccounts] = await Promise.all([
        api('/admin/overview').catch(() => null),
        api('/admin/users').catch(() => []),
        api('/admin/proxies').catch(() => []),
        api('/admin/logs?limit=200').catch(() => []),
        api('/admin/ip-pool').catch(() => ({ items: [], stats: {} })),
        api('/admin/accounts').catch(() => []),
      ]);
      state.admin = {
        overview,
        users: Array.isArray(users) ? users : [],
        accounts: Array.isArray(adminAccounts) ? adminAccounts : [],
        proxies: Array.isArray(adminProxies) ? adminProxies : [],
        logs: Array.isArray(adminLogs) ? adminLogs : [],
        ipPool: Array.isArray(pool?.items) ? pool.items : [],
        poolStats: pool?.stats || {},
      };
    }
    const active = state.keys.find((k) => k.status === 'active');
    if (active && !state.keys.some((k) => k.id === state.selectedSnippetKeyId && k.status === 'active')) {
      state.selectedSnippetKeyId = active.id;
    }
  } catch (e) {
    if (e.status === 401) {
      token = '';
      localStorage.removeItem('gw_token');
      goLogin();
    } else {
      showToast('加载数据失败：' + e.message, 'error');
    }
  }
}

// ---------------------------------------------------------------------------
// Render shell
// ---------------------------------------------------------------------------
function render() {
  const meta = PAGE_META[state.currentPage] || PAGE_META.overview;
  $('page-title').textContent = meta.title;
  $('page-subtitle').textContent = meta.subtitle;

  document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.nav === state.currentPage);
  });
  $('admin-nav').classList.toggle('hidden', !isAdmin());

  $('badge-proxies').textContent = String(state.proxies.filter((p) => p.source === 'auto').length);
  $('badge-accounts').textContent = String(state.accounts.length);
  $('badge-keys').textContent = String(state.keys.filter((k) => k.status === 'active').length);

  const initials = (state.user?.email || '??').slice(0, 2).toUpperCase();
  $('user-card').innerHTML = `
    <div class="w-8 h-8 rounded-lg bg-gradient-to-br from-indigo-500 to-cyan-500 text-white text-xs font-bold flex items-center justify-center shrink-0 shadow-md shadow-indigo-500/20">${esc(initials)}</div>
    <div class="min-w-0 flex-1">
      <div class="text-xs font-semibold text-slate-100 truncate">${esc(state.user?.email || '未登录')}</div>
      <div class="text-[11px] text-slate-400 truncate mono">${isAdmin() ? 'Admin' : 'Tenant'} · ${esc(state.user?.plan_status || 'unpaid')} · 并发 ${esc(state.user?.max_concurrency || 5)}</div>
    </div>
    <button id="logout-btn" title="退出登录" class="p-1.5 rounded-lg text-slate-500 hover:text-rose-400 hover:bg-rose-500/10"><i data-lucide="log-out" class="w-3.5 h-3.5"></i></button>
  `;
  $('logout-btn').onclick = logout;

  if (overviewChart) { overviewChart.destroy(); overviewChart = null; }
  const main = $('main-content');
  if (state.currentPage === 'overview') renderOverview(main);
  else if (state.currentPage === 'proxies') renderProxies(main);
  else if (state.currentPage === 'accounts') renderAccounts(main);
  else if (state.currentPage === 'keys') renderKeys(main);
  else if (state.currentPage === 'logs') renderLogs(main);
  else if (state.currentPage === 'admin') renderAdmin(main);

  bindSpotlightEffect(main);
  icons([main, $('user-card'), $('admin-nav')]);
}

// ---------------------------------------------------------------------------
// Overview
// ---------------------------------------------------------------------------
function renderOverview(container) {
  const logs = state.logs;
  const okLogs = logs.filter((l) => l.status_code === 200);
  const successRate = logs.length ? Math.round((okLogs.length / logs.length) * 100) : 100;
  const sumInput = okLogs.reduce((s, l) => s + (l.input_tokens || 0), 0);
  const sumCache = okLogs.reduce((s, l) => s + (l.cache_read_tokens || 0), 0);
  const cacheHit = sumInput ? Math.round((sumCache / sumInput) * 100) : 0;
  const avgTtft = okLogs.length ? Math.round(okLogs.reduce((s, l) => s + (l.ttft_ms || 0), 0) / okLogs.length) : 0;
  const autoProxies = state.proxies.filter((p) => p.source === 'auto');

  container.innerHTML = `
    <section class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      ${kpiCard('近期请求 / 成功率', 'activity', `${logs.length}`, `${successRate}% 正常透传`, 'indigo', `含 Fail-Closed 拦截 ${logs.length - okLogs.length} 次`)}
      ${kpiCard('Prompt Cache 命中率', 'zap', `${cacheHit}%`, `${(sumCache / 1000).toFixed(1)}k tok`, 'emerald', '原生透传缓存头，无上下文篡改')}
      ${kpiCard('平均首字延迟 (TTFT)', 'timer', `${avgTtft}<span class="text-base font-medium text-slate-400 ml-1">ms</span>`, 'SSE 零缓冲', 'cyan', '独立 Transport 连接池复用')}
      ${kpiCard('专属静态 IP', 'globe-lock', String(autoProxies.length), autoProxies.length ? `${autoProxies.filter((p) => p.health_status === 'healthy').length} 条健康` : '未开通', 'amber', '一账号一 IP · 每个上游账号独立出口')}
    </section>

    <section class="grid grid-cols-1 lg:grid-cols-3 gap-5">
      <div class="lg:col-span-2 spotlight-card rounded-2xl p-5 flex flex-col">
        <div class="flex flex-wrap items-center justify-between gap-2 mb-4">
          <div>
            <h2 class="text-sm font-semibold text-white">近期用量与性能趋势</h2>
            <p class="text-xs text-slate-400 mt-0.5">基于上游原始 Usage 统计（单位：千 Tokens / ms）</p>
          </div>
          <div class="inline-flex rounded-xl bg-white/[0.04] border border-white/[0.08] p-1 text-xs">
            <button id="chart-mode-tokens" class="px-3 py-1 rounded-lg font-medium transition-all ${state.chartMode === 'tokens' ? 'bg-indigo-500/25 text-white border border-indigo-400/30' : 'text-slate-400 hover:text-white border border-transparent'}">Token 分布</button>
            <button id="chart-mode-ttft" class="px-3 py-1 rounded-lg font-medium transition-all ${state.chartMode === 'ttft' ? 'bg-cyan-500/20 text-white border border-cyan-400/30' : 'text-slate-400 hover:text-white border border-transparent'}">TTFT 延迟</button>
          </div>
        </div>
        <div class="relative h-64 w-full"><canvas id="overview-chart"></canvas></div>
      </div>

      <div class="spotlight-card rounded-2xl p-5 flex flex-col">
        <div class="flex items-center justify-between mb-4">
          <div class="flex items-center gap-2"><span class="w-2 h-2 rounded-full bg-cyan-400 shadow-[0_0_8px_#22d3ee]"></span><h2 class="text-sm font-semibold text-white">专属隔离出站拓扑</h2></div>
          <button data-jump="accounts" class="text-xs font-medium text-cyan-400 hover:text-cyan-300">管理绑定 →</button>
        </div>
        <div class="space-y-3 flex-1">
          ${state.accounts.map((acc) => {
            const proxy = proxyForAccount(acc.id);
            const isOk = acc.status === 'active' && proxy && proxy.health_status === 'healthy';
            return `
              <div class="rounded-xl p-3.5 border ${isOk ? 'bg-white/[0.025] border-white/[0.07]' : 'bg-rose-500/[0.06] border-rose-500/25'}">
                <div class="flex items-center justify-between gap-2">
                  <span class="text-xs font-semibold text-slate-100 truncate">${esc(acc.name)}</span>
                  <span class="text-[10px] mono px-2 py-0.5 rounded-full font-semibold ${isOk ? 'bg-emerald-500/15 text-emerald-300' : 'bg-rose-500/15 text-rose-300'}">${isOk ? 'ISOLATED' : 'BLOCKED'}</span>
                </div>
                <div class="mt-2.5 flex items-center gap-2 text-[11px] mono">
                  <span class="px-1.5 py-0.5 rounded bg-white/[0.05] text-slate-300 text-[10px]">GW</span>
                  <svg class="flex-1 h-3 overflow-visible" viewBox="0 0 120 12" preserveAspectRatio="none"><line x1="0" y1="6" x2="120" y2="6" stroke="${isOk ? 'rgba(52,211,153,0.22)' : 'rgba(251,113,133,0.25)'}" stroke-width="1.5"/>${isOk ? '<line class="beam-line" x1="0" y1="6" x2="120" y2="6" stroke="#34d399" stroke-width="1.5"/>' : ''}</svg>
                  <span class="truncate max-w-[140px] ${isOk ? 'text-cyan-300' : 'text-rose-300'}">${proxy ? esc(proxy.last_egress_ip || '-') : '未分配'}</span>
                </div>
                <div class="mt-2 flex items-center justify-between text-[11px] text-slate-400"><span>${proxy ? esc(proxy.last_egress_country || '-') : '-'} · 并发 ${acc.max_concurrency}</span><span class="text-emerald-400/90">Fail-Closed</span></div>
              </div>
            `;
          }).join('') || '<div class="text-xs text-slate-500">暂无上游账号绑定</div>'}
        </div>
      </div>
    </section>

    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between">
        <div><h2 class="text-sm font-semibold text-white">最近官方转发审计日志</h2><p class="text-xs text-slate-400 mt-0.5">每笔请求均回传上游真实模型名与 request-id</p></div>
        <button data-jump="logs" class="text-xs font-medium text-cyan-400 inline-flex items-center gap-1">查看全部 <i data-lucide="arrow-right" class="w-3.5 h-3.5"></i></button>
      </div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs">
        <thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase tracking-wider"><th class="px-5 py-3">时间</th><th class="px-5 py-3">模型核验</th><th class="px-5 py-3">上游 Request-ID</th><th class="px-5 py-3">出口 IP</th><th class="px-5 py-3 text-right">TTFT / 状态</th></tr></thead>
        <tbody class="divide-y divide-white/[0.05]">
          ${logs.slice(0, 5).map((l) => {
            const verified = l.status_code === 200 && l.actual_model && l.actual_model === l.requested_model;
            return `<tr data-log-detail="${esc(l.id)}" class="hover:bg-white/[0.03] cursor-pointer">
              <td class="px-5 py-3.5 mono text-slate-400 whitespace-nowrap">${esc(shortTime(l.created_at))}</td>
              <td class="px-5 py-3.5"><div class="flex items-center gap-2"><span class="mono font-medium text-slate-100">${esc(l.requested_model || '-')}</span><span class="px-2 py-0.5 rounded-full text-[10px] font-semibold ${verified ? 'bg-emerald-500/12 text-emerald-300' : 'bg-rose-500/12 text-rose-300'}">${verified ? '一致' : '已阻断'}</span></div></td>
              <td class="px-5 py-3.5 mono text-slate-400 whitespace-nowrap">${esc(l.upstream_request_id || '-')}</td>
              <td class="px-5 py-3.5 mono text-cyan-300/90 whitespace-nowrap">${esc(l.egress_ip_snapshot || '-')}</td>
              <td class="px-5 py-3.5 text-right whitespace-nowrap"><span class="mono text-slate-400 mr-2">${l.ttft_ms || 0}ms</span><span class="px-2 py-0.5 rounded-full mono text-[11px] ${l.status_code === 200 ? 'bg-emerald-500/15 text-emerald-300' : 'bg-rose-500/15 text-rose-300'}">${l.status_code}</span></td>
            </tr>`;
          }).join('') || '<tr><td colspan="5" class="px-5 py-10 text-center text-slate-500">暂无请求记录</td></tr>'}
        </tbody>
      </table></div>
    </section>
  `;

  $('chart-mode-tokens').onclick = () => { state.chartMode = 'tokens'; render(); };
  $('chart-mode-ttft').onclick = () => { state.chartMode = 'ttft'; render(); };
  bindJumps(container);
  bindLogRows(container);
  initOverviewChart();
}

function kpiCard(label, icon, value, tag, color, footnote) {
  const colorMap = {
    indigo: 'bg-indigo-500/15 text-indigo-300 border-indigo-400/20',
    emerald: 'bg-emerald-500/15 text-emerald-300 border-emerald-400/20',
    cyan: 'bg-cyan-500/15 text-cyan-300 border-cyan-400/20',
    amber: 'bg-amber-500/15 text-amber-300 border-amber-400/20',
  };
  return `<div class="spotlight-card rounded-2xl p-5">
    <div class="flex items-center justify-between"><span class="text-xs font-medium text-slate-400">${esc(label)}</span><span class="p-2 rounded-xl border ${colorMap[color] || colorMap.indigo}"><i data-lucide="${icon}" class="w-4 h-4"></i></span></div>
    <div class="mt-4 flex items-baseline gap-2.5"><span class="text-3xl font-bold tracking-tight text-white mono">${value}</span><span class="text-xs font-medium text-slate-400">${tag}</span></div>
    <div class="mt-2 text-[11px] text-slate-500 flex items-center gap-1.5"><span class="w-1.5 h-1.5 rounded-full bg-${color}-400"></span>${esc(footnote)}</div>
  </div>`;
}

function initOverviewChart() {
  const canvas = $('overview-chart');
  if (!canvas || typeof Chart === 'undefined') return;
  const buckets = {};
  state.logs.forEach((l) => {
    const day = l.created_at ? new Date(l.created_at).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' }) : '-';
    if (!buckets[day]) buckets[day] = { input: 0, output: 0, cache: 0, ttft: 0, n: 0 };
    buckets[day].input += l.input_tokens || 0;
    buckets[day].output += l.output_tokens || 0;
    buckets[day].cache += l.cache_read_tokens || 0;
    buckets[day].ttft += l.ttft_ms || 0;
    buckets[day].n += 1;
  });
  const entries = Object.entries(buckets).slice(-7);
  const labels = entries.length ? entries.map(([d]) => d) : ['暂无数据'];
  const opts = {
    responsive: true, maintainAspectRatio: false,
    plugins: {
      legend: { position: 'bottom', labels: { color: '#94a3b8', boxWidth: 10, padding: 16, font: { size: 11 } } },
      tooltip: { backgroundColor: 'rgba(9,12,20,0.95)', titleColor: '#f8fafc', bodyColor: '#cbd5e1', borderColor: 'rgba(129,140,248,0.3)', borderWidth: 1 },
    },
    scales: {
      x: { stacked: state.chartMode === 'tokens', grid: { display: false }, ticks: { color: '#64748b', font: { size: 11 } } },
      y: { stacked: state.chartMode === 'tokens', grid: { color: 'rgba(255,255,255,0.05)' }, ticks: { color: '#64748b', font: { size: 11 } } },
    },
  };
  const datasets = state.chartMode === 'tokens' ? [
    { label: '缓存命中 (k)', data: entries.map(([, v]) => +(v.cache / 1000).toFixed(1)), backgroundColor: 'rgba(16,185,129,0.85)', borderRadius: 5 },
    { label: '新输入 Input (k)', data: entries.map(([, v]) => +(Math.max(0, v.input - v.cache) / 1000).toFixed(1)), backgroundColor: 'rgba(99,102,241,0.85)', borderRadius: 5 },
    { label: '输出 Output (k)', data: entries.map(([, v]) => +(v.output / 1000).toFixed(1)), backgroundColor: 'rgba(34,211,238,0.65)', borderRadius: 5 },
  ] : [
    { label: '平均 TTFT (ms)', data: entries.map(([, v]) => Math.round(v.ttft / Math.max(1, v.n))), borderColor: '#22d3ee', backgroundColor: 'rgba(34,211,238,0.12)', fill: true, tension: 0.38, pointBackgroundColor: '#05060a', pointBorderColor: '#22d3ee', pointBorderWidth: 2 },
  ];
  overviewChart = new Chart(canvas, { type: state.chartMode === 'tokens' ? 'bar' : 'line', data: { labels, datasets }, options: opts });
}

function bindJumps(container) {
  container.querySelectorAll('[data-jump]').forEach((btn) => {
    btn.onclick = () => { state.currentPage = btn.dataset.jump; render(); };
  });
}

function bindLogRows(container) {
  container.querySelectorAll('[data-log-detail]').forEach((row) => {
    row.onclick = () => openLogDetailModal(row.dataset.logDetail);
  });
}

// ---------------------------------------------------------------------------
// Static IP (read-only)
// ---------------------------------------------------------------------------
function renderProxies(container) {
  const plan = state.user?.plan_status || 'unpaid';
  const list = state.proxies.filter((p) => p.source === 'auto');
  container.innerHTML = `
    <div class="spotlight-card rounded-2xl p-4 sm:p-5 flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-indigo-500/25">
      <div class="flex items-start gap-3.5">
        <div class="p-2.5 rounded-xl bg-indigo-500/15 text-indigo-300 border border-indigo-400/25 shrink-0"><i data-lucide="shield-check" class="w-4 h-4"></i></div>
        <div>
          <h3 class="text-xs font-semibold text-white">静态 IP 由系统统一分配 · 一账号一 IP</h3>
          <p class="text-xs text-slate-400 mt-1 leading-relaxed">每绑定一个上游账号，系统就从静态 IP 资源池为它分配一条独立专属出口并完成健康探测；账号之间绝不共用出口，用户端不提供自填代理入口。</p>
        </div>
      </div>
      <button id="refresh-ip-btn" class="inline-flex items-center gap-1.5 px-4 py-2 rounded-xl text-xs font-semibold bg-white/[0.06] text-slate-200 border border-white/[0.1] hover:bg-white/[0.1] shrink-0"><i data-lucide="refresh-cw" class="w-3.5 h-3.5"></i>刷新状态</button>
    </div>

    ${list.length ? `<div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      ${list.map((p) => {
        const acc = p.upstream_account_id ? getAccount(p.upstream_account_id) : null;
        const owner = acc ? acc.name : (p.upstream_account_id ? esc(p.upstream_account_id.slice(0, 8)) + '…' : '未绑定（占位）');
        return `<div class="spotlight-card rounded-2xl p-5">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <div class="flex items-center gap-2"><span class="px-2 py-0.5 rounded-md text-[10px] font-bold uppercase mono bg-indigo-500/15 text-indigo-300 border border-indigo-400/25">${esc(p.scheme || '-')}</span><h3 class="text-sm font-semibold text-white">${esc(p.name || '专属静态 IP')}</h3></div>
              <div class="mt-2 text-xs mono text-slate-400">${esc(p.host || '-')}:${esc(p.port || '-')}</div>
              <div class="mt-1.5 text-[11px] text-slate-500">绑定账号：<span class="text-cyan-300">${owner}</span></div>
            </div>
            ${healthBadge(p.health_status)}
          </div>
          <div class="mt-4 pt-4 border-t border-white/[0.06] grid grid-cols-2 sm:grid-cols-4 gap-4 text-xs">
            <div><div class="text-[11px] text-slate-500">出口 IP</div><div class="mono font-medium text-cyan-300 mt-0.5">${esc(p.last_egress_ip || '待探测')}</div></div>
            <div><div class="text-[11px] text-slate-500">归属地</div><div class="font-medium text-slate-200 mt-0.5">${esc(p.last_egress_country || '-')}</div></div>
            <div><div class="text-[11px] text-slate-500">探测延迟</div><div class="mono font-medium text-emerald-400 mt-0.5">${p.last_latency_ms ? esc(p.last_latency_ms) + 'ms' : '-'}</div></div>
            <div><div class="text-[11px] text-slate-500">分配方式</div><div class="font-medium text-slate-200 mt-0.5">账号专属</div></div>
          </div>
          <div class="mt-4 text-[11px] text-slate-500">最近检测：${esc(fmtTime(p.last_checked_at))} · 套餐状态：<span class="mono text-slate-300">${esc(plan)}</span></div>
        </div>`;
      }).join('')}
    </div>` : `
      <div class="spotlight-card rounded-2xl p-10 text-center">
        <i data-lucide="globe-lock" class="w-8 h-8 text-slate-500 mx-auto"></i>
        <h3 class="text-sm font-semibold text-slate-200 mt-3">尚未分配静态 IP</h3>
        <p class="text-xs text-slate-500 mt-2">当前套餐状态：${esc(plan)}。付款开通后，每绑定一个上游账号，系统会自动从资源池为它分配一条独立静态 IP 并开启 Fail-Closed 出站。</p>
      </div>
    `}
  `;
  $('refresh-ip-btn').onclick = loadAndRender;
}

// ---------------------------------------------------------------------------
// Upstream accounts
// ---------------------------------------------------------------------------
function renderAccounts(container) {
  container.innerHTML = `
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div class="text-xs text-slate-400">所有上游凭证均使用 <span class="mono font-semibold text-cyan-300">AES-256-GCM</span> 加密落库；<span class="text-cyan-300">每个账号独占一条</span>系统从资源池自动分配的静态 IP，账号之间不共用出口。</div>
      <button id="add-account-btn" class="inline-flex items-center gap-1.5 px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white hover:bg-indigo-400 shadow-lg shadow-indigo-500/25"><i data-lucide="plus" class="w-4 h-4"></i>绑定上游账号</button>
    </div>
    <div class="space-y-4">
      ${state.accounts.map((acc) => {
        const pm = providerMeta(acc.provider);
        const p = proxyForAccount(acc.id);
        const ok = acc.status === 'active' && p && p.health_status === 'healthy';
        return `<div class="spotlight-card rounded-2xl p-5">
          <div class="flex flex-col lg:flex-row lg:items-center justify-between gap-4">
            <div class="space-y-2 min-w-0">
              <div class="flex flex-wrap items-center gap-2"><h3 class="text-sm font-semibold text-white">${esc(acc.name)}</h3><span class="px-2.5 py-0.5 rounded-full text-[11px] border ${pm.badge}">${esc(pm.label)}</span><span class="px-2.5 py-0.5 rounded-full text-[11px] ${acc.status === 'active' ? 'bg-emerald-500/12 text-emerald-300 border border-emerald-500/25' : 'bg-amber-500/12 text-amber-300 border border-amber-500/25'}">${acc.status === 'active' ? '运行中' : esc(acc.status)}</span></div>
              <div class="flex flex-wrap gap-x-5 gap-y-1.5 text-xs text-slate-400"><span class="mono">Base URL：${esc(acc.base_url || '-')}</span><span>并发上限：<strong class="text-white mono">${esc(acc.max_concurrency)}</strong></span></div>
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <div class="px-3.5 py-2 rounded-xl border ${ok ? 'border-white/[0.08] bg-white/[0.03]' : 'border-rose-500/30 bg-rose-500/10'} text-xs">
                <div class="text-[10px] text-slate-500 uppercase font-semibold tracking-wider">绑定专属静态 IP</div>
                <div class="mt-0.5 flex items-center gap-2 font-medium ${ok ? 'text-slate-100' : 'text-rose-300'}"><span class="w-1.5 h-1.5 rounded-full ${ok ? 'bg-emerald-400 shadow-[0_0_8px_#34d399]' : 'bg-rose-400'}"></span>${p ? esc(p.last_egress_ip || p.name) : '未分配'}</div>
              </div>
              <div class="px-3.5 py-2 rounded-xl border border-emerald-500/30 bg-emerald-500/10 text-xs"><div class="text-[10px] text-slate-500 uppercase font-semibold tracking-wider">出站策略</div><div class="mt-0.5 font-medium text-emerald-300">Fail-Closed 强制开启</div></div>
              <button data-test-account="${esc(acc.id)}" class="inline-flex items-center gap-1.5 px-3.5 py-2.5 rounded-xl text-xs bg-white/[0.05] text-slate-200 border border-white/[0.08] hover:bg-indigo-500/15"><i data-lucide="plug-zap" class="w-3.5 h-3.5 text-cyan-400"></i>测试连通</button>
              <button data-delete-account="${esc(acc.id)}" class="p-2 rounded-lg text-slate-500 hover:text-rose-400 hover:bg-rose-500/10" title="删除"><i data-lucide="trash-2" class="w-3.5 h-3.5"></i></button>
            </div>
          </div>
        </div>`;
      }).join('') || '<div class="spotlight-card rounded-2xl p-10 text-center text-xs text-slate-500">暂无上游账号</div>'}
    </div>
  `;
  $('add-account-btn').onclick = openAddAccountModal;
  container.querySelectorAll('[data-test-account]').forEach((btn) => { btn.onclick = () => testAccount(btn.dataset.testAccount); });
  container.querySelectorAll('[data-delete-account]').forEach((btn) => { btn.onclick = () => deleteAccount(btn.dataset.deleteAccount); });
}

function openAddAccountModal() {
  const plan = state.user?.plan_status || 'unpaid';
  openModal({
    title: '绑定上游账号',
    bodyHtml: `
      <div class="space-y-3.5 text-xs">
        <div><label class="block font-medium text-slate-300 mb-1.5">账号显示名称</label><input id="f-acc-name" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white focus:outline-none focus:border-indigo-400" placeholder="例如：Anthropic 团队专用 Key" /></div>
        <div class="grid grid-cols-2 gap-3">
          <div><label class="block font-medium text-slate-300 mb-1.5">上游供应商</label><select id="f-acc-provider" class="w-full px-3 py-2.5 rounded-xl bg-[#121726] border border-white/[0.1] text-white focus:outline-none focus:border-indigo-400"><option value="anthropic_api">Anthropic Official API</option><option value="aws_bedrock">AWS Bedrock</option><option value="vertex_ai">Google Vertex AI</option><option value="custom_anthropic">Custom Anthropic</option></select></div>
          <div><label class="block font-medium text-slate-300 mb-1.5">并发上限</label><input id="f-acc-conc" type="number" min="1" max="20" value="2" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono focus:outline-none focus:border-indigo-400" /></div>
        </div>
        <div><label class="block font-medium text-slate-300 mb-1.5">Base URL（可选）</label><input id="f-acc-base" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono focus:outline-none focus:border-indigo-400" placeholder="https://api.anthropic.com" /></div>
        <div><label class="block font-medium text-slate-300 mb-1.5">上游凭证（AES-256-GCM 加密）</label><input id="f-acc-cred" type="password" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono focus:outline-none focus:border-indigo-400" placeholder="sk-ant-api03-..." /></div>
        <div class="p-3 rounded-xl bg-emerald-500/10 border border-emerald-500/25 text-emerald-300 leading-relaxed">创建时系统会从静态 IP 资源池为该账号 <span class="font-semibold">单独分配一条独立出口</span>（一账号一 IP，不与其他账号共用），并强制 Fail-Closed。当前套餐：<span class="mono">${esc(plan)}</span>；若资源池无可用健康 IP，创建会失败并提示补充库存。</div>
      </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400 hover:bg-white/[0.06]">取消</button><button id="m-save-acc" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white hover:bg-indigo-400">加密保存绑定</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-save-acc').onclick = async () => {
    const name = $('f-acc-name').value.trim();
    const credential = $('f-acc-cred').value.trim();
    if (!name || !credential) { showToast('请填写名称与凭证', 'warning'); return; }
    try {
      await api('/accounts', { method: 'POST', body: JSON.stringify({
        name, provider: $('f-acc-provider').value, base_url: $('f-acc-base').value.trim(),
        credential,
        max_concurrency: Math.max(1, Number($('f-acc-conc').value) || 2),
      }) });
      closeModal();
      showToast('账号已加密保存，并分配了独立专属静态 IP', 'success');
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

async function testAccount(id) {
  try {
    const r = await api('/accounts/' + encodeURIComponent(id) + '/test', { method: 'POST', body: {} });
    showToast(r.note || '连通性检查已提交', 'info');
  } catch (e) { showToast(e.message, 'error'); }
}

async function deleteAccount(id) {
  if (!confirm('删除该上游账号？')) return;
  try {
    await api('/accounts/' + encodeURIComponent(id), { method: 'DELETE' });
    await loadData(); render(); showToast('已删除', 'info');
  } catch (e) { showToast(e.message, 'error'); }
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------
function renderKeys(container) {
  const activeKeys = state.keys.filter((k) => k.status === 'active');
  if (!activeKeys.some((k) => k.id === state.selectedSnippetKeyId)) state.selectedSnippetKeyId = activeKeys[0]?.id || '';
  const selected = getKey(state.selectedSnippetKeyId) || activeKeys[0] || null;
  const snippet = buildSnippet(selected ? selected.key_prefix || 'sk-iso-…' : 'sk-iso-your-key');

  container.innerHTML = `
    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06] flex flex-wrap items-center justify-between gap-2">
        <div><h2 class="text-sm font-semibold text-white">已签发的网关 API Key</h2><p class="text-xs text-slate-400 mt-0.5">数据库仅保存 SHA-256 哈希，每枚 Key 严格锁定到单个上游账号</p></div>
        <button id="keys-create-btn" class="inline-flex items-center gap-1.5 px-3.5 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white hover:bg-indigo-400"><i data-lucide="plus" class="w-3.5 h-3.5"></i>签发新 Key</button>
      </div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs">
        <thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase tracking-wider"><th class="px-5 py-3">名称 / 前缀</th><th class="px-5 py-3">绑定账号</th><th class="px-5 py-3">RPM</th><th class="px-5 py-3">状态 / 最近使用</th><th class="px-5 py-3 text-right">操作</th></tr></thead>
        <tbody class="divide-y divide-white/[0.05]">
          ${state.keys.map((k) => {
            const acc = getAccount(k.upstream_account_id);
            return `<tr class="hover:bg-white/[0.025]">
              <td class="px-5 py-3.5"><div class="font-semibold text-slate-100">${esc(k.name)}</div><div class="mono text-[11px] text-cyan-300/80 mt-0.5">${esc(k.key_prefix || '')}…</div></td>
              <td class="px-5 py-3.5"><div class="font-medium text-slate-200">${acc ? esc(acc.name) : '-'}</div><div class="text-[11px] text-slate-500 mt-0.5">${(k.allowed_models || []).length} 个允许模型</div></td>
              <td class="px-5 py-3.5 mono text-slate-300">${esc(k.rpm_limit)} req/min</td>
              <td class="px-5 py-3.5"><span class="px-2.5 py-0.5 rounded-full text-[11px] ${k.status === 'active' ? 'bg-emerald-500/12 text-emerald-300 border border-emerald-500/25' : 'bg-white/[0.05] text-slate-500'}">${k.status === 'active' ? '活跃' : '已吊销'}</span><div class="text-[11px] text-slate-500 mt-1">${esc(fmtTime(k.last_used_at))}</div></td>
              <td class="px-5 py-3.5 text-right">${k.status === 'active' ? `<button data-revoke-key="${esc(k.id)}" class="px-2.5 py-1 rounded-lg text-xs text-rose-400 hover:bg-rose-500/15">吊销</button>` : '<span class="text-slate-600">不可恢复</span>'}</td>
            </tr>`;
          }).join('') || '<tr><td colspan="5" class="px-5 py-10 text-center text-slate-500">暂无 Key</td></tr>'}
        </tbody>
      </table></div>
    </section>

    <section class="spotlight-card rounded-2xl p-5 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div><h2 class="text-sm font-semibold text-white">一键接入配置生成器</h2><p class="text-xs text-slate-400 mt-0.5">替换 Base URL 与 Key 即可零改造接入 Claude Code / SDK</p></div>
        <select id="snippet-key-select" class="text-xs px-3 py-1.5 rounded-lg bg-[#121726] border border-white/[0.1] text-slate-200">${activeKeys.map((k) => `<option value="${esc(k.id)}" ${k.id === state.selectedSnippetKeyId ? 'selected' : ''}>${esc(k.name)}</option>`).join('')}</select>
      </div>
      <div class="flex flex-wrap gap-1.5">
        ${[['claude-code', 'Claude Code CLI'], ['curl', 'cURL (/v1/messages)'], ['python', 'Anthropic Python SDK'], ['openai', 'OpenAI 兼容格式']].map(([id, label]) => `<button data-snippet-tab="${id}" class="px-3 py-1.5 rounded-lg text-xs ${state.snippetTab === id ? 'bg-indigo-500/20 text-white border border-indigo-400/35' : 'bg-white/[0.03] text-slate-400 border border-white/[0.06]'}">${label}</button>`).join('')}
      </div>
      <div class="rounded-xl bg-[#05070d] border border-white/[0.09] overflow-hidden">
        <div class="px-4 py-2.5 bg-white/[0.03] border-b border-white/[0.06] flex items-center justify-between"><div class="flex gap-2"><span class="w-2.5 h-2.5 rounded-full bg-[#ff5f56]"></span><span class="w-2.5 h-2.5 rounded-full bg-[#ffbd2e]"></span><span class="w-2.5 h-2.5 rounded-full bg-[#27c93f]"></span><span class="ml-2 text-[11px] mono text-slate-500">isogate-quickstart.sh</span></div><button id="copy-snippet-btn" class="text-[11px] px-2.5 py-1 rounded-md bg-white/[0.06] text-slate-200">复制配置</button></div>
        <pre class="mono text-xs text-cyan-100/90 leading-relaxed p-4 overflow-x-auto"><code>${esc(snippet)}</code></pre>
      </div>
    </section>
  `;
  $('keys-create-btn').onclick = openCreateKeyModal;
  $('copy-snippet-btn').onclick = () => copyText(snippet, '接入配置');
  const sel = $('snippet-key-select');
  if (sel) sel.onchange = () => { state.selectedSnippetKeyId = sel.value; render(); };
  container.querySelectorAll('[data-snippet-tab]').forEach((btn) => { btn.onclick = () => { state.snippetTab = btn.dataset.snippetTab; render(); }; });
  container.querySelectorAll('[data-revoke-key]').forEach((btn) => { btn.onclick = () => openRevokeModal(btn.dataset.revokeKey); });
}

function buildSnippet(key) {
  if (state.snippetTab === 'claude-code') return `# export 后直接运行 claude\nexport ANTHROPIC_BASE_URL="${PUBLIC_BASE}"\nexport ANTHROPIC_AUTH_TOKEN="${key}"\n\nclaude`;
  if (state.snippetTab === 'curl') return `curl -i ${PUBLIC_BASE}/v1/messages \\\n  -H "x-api-key: ${key}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "content-type: application/json" \\\n  -d '{"model":"claude-sonnet-4-5-20250929","max_tokens":256,"messages":[{"role":"user","content":"Hello from IsoGate"}]}'`;
  if (state.snippetTab === 'python') return `import anthropic\n\nclient = anthropic.Anthropic(base_url="${PUBLIC_BASE}", api_key="${key}")\nmsg = client.messages.create(model="claude-sonnet-4-5-20250929", max_tokens=512, messages=[{"role":"user","content":"Verify zero-adulteration passthrough"}])\nprint(msg.model, msg.id)`;
  return `from openai import OpenAI\n\nclient = OpenAI(base_url="${PUBLIC_BASE}/v1", api_key="${key}")\nresp = client.chat.completions.create(model="claude-sonnet-4-5-20250929", messages=[{"role":"user","content":"Hello via OpenAI-compatible route"}])`;
}

function openCreateKeyModal() {
  if (!state.accounts.length) { showToast('请先绑定上游账号', 'warning'); return; }
  openModal({
    title: '签发新的网关 API Key',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div><label class="block font-medium text-slate-300 mb-1.5">Key 用途备注</label><input id="f-key-name" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white" placeholder="例如：办公室台式机 · Claude Code" /></div>
      <div><label class="block font-medium text-slate-300 mb-1.5">绑定上游账号</label><select id="f-key-acc" class="w-full px-3 py-2.5 rounded-xl bg-[#121726] border border-white/[0.1] text-white">${state.accounts.map((a) => `<option value="${esc(a.id)}">${esc(a.name)}</option>`).join('')}</select></div>
      <div><label class="block font-medium text-slate-300 mb-1.5">RPM 限流</label><input id="f-key-rpm" type="number" value="60" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">取消</button><button id="m-create-key" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white">立即签发</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-create-key').onclick = async () => {
    const name = $('f-key-name').value.trim();
    if (!name) { showToast('请填写用途备注', 'warning'); return; }
    try {
      const res = await api('/keys', { method: 'POST', body: JSON.stringify({ name, account_id: $('f-key-acc').value, rpm_limit: Math.max(1, Number($('f-key-rpm').value) || 60) }) });
      await loadData();
      state.selectedSnippetKeyId = res.id;
      render();
      openModal({
        title: 'API Key 已签发（仅展示一次）',
        bodyHtml: `<div class="space-y-3 text-xs"><div class="p-3 rounded-xl bg-amber-500/12 border border-amber-500/30 text-amber-200">服务端仅保存 SHA-256 哈希，关闭后无法再次查看完整明文。</div><div class="p-3.5 rounded-xl bg-[#05070d] border border-emerald-500/30 text-emerald-300 mono break-all select-all">${esc(res.key)}</div></div>`,
        footerHtml: `<button id="m-copy-new-key" class="px-3.5 py-2 rounded-xl text-xs bg-indigo-500 text-white">复制完整 Key</button><button id="m-done-key" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">我已保存</button>`,
      });
      $('m-copy-new-key').onclick = () => copyText(res.key, '新 API Key');
      $('m-done-key').onclick = closeModal;
    } catch (e) { showToast(e.message, 'error'); }
  };
}

function openRevokeModal(id) {
  const k = getKey(id);
  if (!k) return;
  openModal({
    title: '确认吊销 API Key',
    bodyHtml: `<p class="text-xs text-slate-300 leading-relaxed">吊销后 <span class="mono text-white">${esc(k.key_prefix)}…</span> 的所有请求将立即返回 401，此操作不可撤销。</p>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">取消</button><button id="m-revoke" class="px-4 py-2 rounded-xl text-xs font-semibold bg-rose-600 text-white">确认吊销</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-revoke').onclick = async () => {
    try { await api('/keys/' + encodeURIComponent(id), { method: 'DELETE' }); closeModal(); await loadData(); render(); showToast('已吊销', 'info'); }
    catch (e) { showToast(e.message, 'error'); }
  };
}

// ---------------------------------------------------------------------------
// Audit logs
// ---------------------------------------------------------------------------
function filteredLogs(source = state.logs) {
  const q = state.logFilter.query.trim().toLowerCase();
  return source.filter((l) => {
    if (state.logFilter.status !== 'all' && String(l.status_code) !== state.logFilter.status) return false;
    if (!q) return true;
    return [l.upstream_request_id, l.requested_model, l.actual_model, l.egress_ip_snapshot]
      .some((v) => String(v || '').toLowerCase().includes(q));
  });
}

function renderLogs(container) {
  const rows = filteredLogs();
  container.innerHTML = `
    <div class="spotlight-card rounded-2xl p-4 flex flex-col sm:flex-row items-stretch sm:items-center justify-between gap-3">
      <div class="relative flex-1 max-w-md"><i data-lucide="search" class="w-4 h-4 text-slate-500 absolute left-3 top-1/2 -translate-y-1/2"></i><input id="log-search-input" value="${esc(state.logFilter.query)}" placeholder="搜索 request-id、模型名或出口 IP..." class="w-full pl-9 pr-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.09] text-xs text-white" /></div>
      <div class="flex gap-2"><select id="log-status-select" class="px-3 py-2 rounded-xl bg-[#121726] border border-white/[0.09] text-xs text-slate-200"><option value="all">全部状态码</option><option value="200" ${state.logFilter.status === '200' ? 'selected' : ''}>200 正常</option><option value="401" ${state.logFilter.status === '401' ? 'selected' : ''}>401 凭证</option><option value="403" ${state.logFilter.status === '403' ? 'selected' : ''}>403 拦截</option></select><button id="log-reset-btn" class="px-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.09] text-xs text-slate-300">重置</button></div>
    </div>
    ${logTableHtml(rows, 'tenant')}
  `;
  $('log-search-input').oninput = (e) => {
    state.logFilter.query = e.target.value;
    render();
    const el = $('log-search-input'); if (el) { el.focus(); el.setSelectionRange(el.value.length, el.value.length); }
  };
  $('log-status-select').onchange = (e) => { state.logFilter.status = e.target.value; render(); };
  $('log-reset-btn').onclick = () => { state.logFilter = { query: '', status: 'all' }; render(); };
  bindLogRows(container);
}

function logTableHtml(rows, scope) {
  return `<div class="spotlight-card rounded-2xl overflow-hidden"><div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs">
    <thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase tracking-wider"><th class="px-4 py-3">时间</th><th class="px-4 py-3">请求模型 → 实际模型</th><th class="px-4 py-3">Request-ID</th><th class="px-4 py-3">出口 IP</th><th class="px-4 py-3 text-right">Tokens (In / Cache / Out)</th><th class="px-4 py-3 text-right">TTFT / 状态</th></tr></thead>
    <tbody class="divide-y divide-white/[0.05]">${rows.length ? rows.map((l) => {
      const verified = l.status_code === 200 && l.actual_model && l.actual_model === l.requested_model;
      const attr = scope === 'admin' ? `data-admin-log-detail="${esc(l.id)}"` : `data-log-detail="${esc(l.id)}"`;
      return `<tr ${attr} class="hover:bg-white/[0.03] cursor-pointer">
        <td class="px-4 py-3.5 mono text-slate-400 whitespace-nowrap">${esc(fmtTime(l.created_at))}</td>
        <td class="px-4 py-3.5"><div class="mono font-medium text-slate-100">${esc(l.requested_model || '-')}</div><div class="mt-0.5 text-[11px] ${verified ? 'text-emerald-400' : 'text-rose-400'}">${verified ? '实际一致 · 官方转发' : esc(l.error_message || (l.actual_model ? l.actual_model : '上游未响应'))}</div></td>
        <td class="px-4 py-3.5 mono text-slate-400 whitespace-nowrap">${esc(l.upstream_request_id || '-')}</td>
        <td class="px-4 py-3.5 mono text-cyan-300/90 whitespace-nowrap">${esc(l.egress_ip_snapshot || '-')}</td>
        <td class="px-4 py-3.5 text-right mono text-slate-300 whitespace-nowrap">${(l.input_tokens || 0).toLocaleString()} / <span class="text-emerald-400">${(l.cache_read_tokens || 0).toLocaleString()}</span> / ${(l.output_tokens || 0).toLocaleString()}</td>
        <td class="px-4 py-3.5 text-right whitespace-nowrap"><span class="mono text-slate-400 mr-2">${l.ttft_ms || 0}ms</span><span class="px-2 py-0.5 rounded-full mono ${l.status_code === 200 ? 'bg-emerald-500/15 text-emerald-300' : 'bg-rose-500/15 text-rose-300'}">${l.status_code}</span></td>
      </tr>`;
    }).join('') : '<tr><td colspan="6" class="px-5 py-12 text-center text-slate-500">没有匹配的审计日志</td></tr>'}
    </tbody></table></div></div>`;
}

function openLogDetailModal(id, source) {
  const list = source === 'admin' ? state.admin.logs : state.logs;
  const l = list.find((x) => String(x.id) === String(id));
  if (!l) return;
  const verified = l.status_code === 200 && l.actual_model && l.actual_model === l.requested_model;
  openModal({
    title: `审计核验详情 #${esc(l.id)}`,
    bodyHtml: `<div class="space-y-4 text-xs">
      <div class="p-3.5 rounded-xl border ${verified ? 'bg-emerald-500/10 border-emerald-500/30 text-emerald-200' : 'bg-rose-500/10 border-rose-500/30 text-rose-200'}"><div class="font-semibold">${verified ? '官方转发核验通过' : '请求被阻断或未完成时请求'}</div><div class="mt-1 opacity-90">${esc(l.error_message || '')}</div></div>
      <div class="grid grid-cols-2 gap-3.5 bg-white/[0.03] rounded-xl p-4 border border-white/[0.07]">
        <div><div class="text-slate-500">请求模型</div><div class="mono text-white mt-0.5">${esc(l.requested_model || '-')}</div></div>
        <div><div class="text-slate-500">实际模型</div><div class="mono text-white mt-0.5">${esc(l.actual_model || '-')}</div></div>
        <div><div class="text-slate-500">出口 IP</div><div class="mono text-cyan-300 mt-0.5">${esc(l.egress_ip_snapshot || '-')}</div></div>
        <div><div class="text-slate-500">传输模式</div><div class="text-slate-200 mt-0.5">${l.is_stream ? 'SSE 流式' : 'JSON'}</div></div>
        <div><div class="text-slate-500">TTFT / 总耗时</div><div class="mono text-slate-200 mt-0.5">${l.ttft_ms || 0}ms / ${l.total_latency_ms || 0}ms</div></div>
        <div><div class="text-slate-500">用户 / Key</div><div class="mono text-slate-200 mt-0.5">${esc(l.user_id || l.api_key_id || '-')}</div></div>
      </div>
      <pre class="mono text-[11px] bg-[#05070d] border border-white/[0.08] text-cyan-200/90 p-3.5 rounded-xl overflow-x-auto">X-Gateway-Upstream-Request-Id: ${esc(l.upstream_request_id || '-')}\nX-Gateway-Egress-Proxy-Id: ${esc(l.proxy_id || '-')}\nX-Gateway-Passthrough-Verified: ${verified}</pre>
    </div>`,
    footerHtml: `<button id="m-close-log" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white">关闭</button>`,
  });
  $('m-close-log').onclick = closeModal;
}

// ---------------------------------------------------------------------------
// Admin
// ---------------------------------------------------------------------------
function renderAdmin(container) {
  if (!isAdmin()) { container.innerHTML = '<div class="spotlight-card p-10 text-center text-xs text-slate-500">仅管理员可见</div>'; return; }
  const ov = state.admin.overview || {};
  const s = state.admin.poolStats || {};
  const emailOf = (uid) => state.admin.users.find((u) => u.id === uid)?.email || (uid ? uid.slice(0, 8) + '…' : '-');
  container.innerHTML = `
    <section class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      ${kpiCard('注册用户', 'users', esc(ov.users ?? state.admin.users.length), '全平台', 'indigo', '租户总数')}
      ${kpiCard('已开通套餐', 'badge-check', esc(ov.active_plans ?? state.admin.users.filter((u) => u.plan_status === 'active').length), '付费用户', 'emerald', '每个账号各自分配独立出口')}
      ${kpiCard('已分配静态 IP', 'globe-lock', esc(ov.assigned_ips ?? state.admin.proxies.length), '一账号一 IP', 'cyan', 'Fail-Closed 强制')}
      ${kpiCard('请求 / 模型不一致', 'shield-alert', esc(ov.requests ?? state.stats.requests), String(ov.model_mismatch ?? state.stats.model_mismatch ?? 0), 'amber', '审计链 ' + (state.chain.ok === true ? '完整' : '待校验'))}
    </section>

    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06] flex items-center justify-between"><div><h2 class="text-sm font-semibold text-white">用户与套餐</h2><p class="text-xs text-slate-400 mt-0.5">套餐状态参与出站鉴权；静态 IP 请在下方「账号与 IP」按账号管理（一账号一 IP）</p></div><button id="admin-refresh-btn" class="text-xs px-3 py-1.5 rounded-lg bg-white/[0.05] text-slate-200">刷新</button></div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs"><thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase"><th class="px-5 py-3">邮箱</th><th class="px-5 py-3">角色</th><th class="px-5 py-3">套餐</th><th class="px-5 py-3">静态 IP</th><th class="px-5 py-3">到期</th><th class="px-5 py-3">并发</th><th class="px-5 py-3 text-right">操作</th></tr></thead><tbody class="divide-y divide-white/[0.05]">
        ${state.admin.users.map((u) => `<tr>
          <td class="px-5 py-3.5 font-medium text-slate-100">${esc(u.email)}</td>
          <td class="px-5 py-3.5 mono text-slate-400">${esc(u.role)}</td>
          <td class="px-5 py-3.5"><span class="px-2 py-0.5 rounded-full ${u.plan_status === 'active' ? 'bg-emerald-500/15 text-emerald-300' : 'bg-white/[0.06] text-slate-400'}">${esc(u.plan_status || 'unpaid')}</span></td>
          <td class="px-5 py-3.5 mono text-cyan-300">${esc(u.assigned_ip || '-')}</td>
          <td class="px-5 py-3.5 mono text-slate-400">${esc(fmtTime(u.plan_expires_at))}</td>
          <td class="px-5 py-3.5 mono text-slate-400">${esc(u.max_concurrency || 5)}</td>
          <td class="px-5 py-3.5 text-right whitespace-nowrap">
            <button data-plan="${esc(u.id)}" data-email="${esc(u.email)}" data-plan-status="${esc(u.plan_status || 'unpaid')}" data-plan-expires="${esc(u.plan_expires_at || '')}" data-max-concurrency="${esc(u.max_concurrency || 5)}" data-user-status="${esc(u.status || 'active')}" class="px-2.5 py-1 rounded-lg bg-white/[0.05] text-slate-200 hover:bg-white/[0.1] mr-1">套餐</button>
            <button data-assign-pool="${esc(u.id)}" data-email="${esc(u.email)}" title="兼容入口：给用户分配一条占位 IP（新账号会自动从池取独立 IP）" class="px-2.5 py-1 rounded-lg bg-emerald-500/10 text-emerald-300 hover:bg-emerald-500/20 mr-1">从池分配</button>
            <button data-assign-ip="${esc(u.id)}" data-email="${esc(u.email)}" title="兼容入口：手动指定一个占位静态 IP" class="px-2.5 py-1 rounded-lg bg-indigo-500/15 text-indigo-300 hover:bg-indigo-500/25 mr-1">${u.assigned_ip ? '更换 IP' : '分配 IP'}</button>
            <button data-revoke-ip="${esc(u.id)}" data-email="${esc(u.email)}" title="回收该用户全部静态 IP 并停用套餐" class="px-2.5 py-1 rounded-lg bg-rose-500/10 text-rose-300 hover:bg-rose-500/20" ${u.assigned_ip ? '' : 'disabled'}>回收全部</button>
          </td>
        </tr>`).join('') || '<tr><td colspan="7" class="px-5 py-10 text-center text-slate-500">暂无用户</td></tr>'}
      </tbody></table></div>
    </section>

    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06] flex flex-wrap items-center justify-between gap-3">
        <div><h2 class="text-sm font-semibold text-white">账号与 IP（一账号一 IP）</h2><p class="text-xs text-slate-400 mt-0.5">每个上游账号独占一条静态出口；创建账号时自动从资源池分配，此处可单独更换或回收，不影响同用户其他账号</p></div>
        <span class="text-[11px] text-slate-500">共 ${state.admin.accounts.length} 个账号</span>
      </div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs"><thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase"><th class="px-4 py-3">上游账号</th><th class="px-4 py-3">所属用户</th><th class="px-4 py-3">供应商</th><th class="px-4 py-3">专属出口 IP</th><th class="px-4 py-3">健康</th><th class="px-4 py-3">隔离</th><th class="px-4 py-3 text-right">操作</th></tr></thead><tbody class="divide-y divide-white/[0.05]">
        ${state.admin.accounts.map((a) => `<tr>
          <td class="px-4 py-3.5"><div class="font-medium text-slate-100">${esc(a.name)}</div><div class="mono text-[10px] text-slate-500 mt-0.5">${esc(a.id.slice(0, 8))}…</div></td>
          <td class="px-4 py-3.5 text-slate-300">${esc(emailOf(a.user_id))}</td>
          <td class="px-4 py-3.5 mono text-slate-400">${esc(a.provider)}</td>
          <td class="px-4 py-3.5 mono text-cyan-300">${esc(a.assigned_ip || '未分配')}</td>
          <td class="px-4 py-3.5">${a.proxy_health ? healthBadge(a.proxy_health) : '<span class="text-slate-600">-</span>'}</td>
          <td class="px-4 py-3.5">${a.exclusive === false ? '<span class="px-2 py-0.5 rounded-full text-[10px] bg-amber-500/10 text-amber-300 border border-amber-500/25">legacy 共用</span>' : '<span class="px-2 py-0.5 rounded-full text-[10px] bg-emerald-500/10 text-emerald-300 border border-emerald-500/25">独占</span>'}</td>
          <td class="px-4 py-3.5 text-right whitespace-nowrap">
            <button data-acct-assign="${esc(a.id)}" class="px-2.5 py-1 rounded-lg bg-indigo-500/15 text-indigo-300 hover:bg-indigo-500/25 mr-1">${a.assigned_ip ? '更换 IP' : '分配 IP'}</button>
            <button data-acct-revoke="${esc(a.id)}" class="px-2.5 py-1 rounded-lg bg-rose-500/10 text-rose-300 hover:bg-rose-500/20" ${a.assigned_ip ? '' : 'disabled'}>回收</button>
          </td>
        </tr>`).join('') || '<tr><td colspan="7" class="px-5 py-10 text-center text-slate-500">暂无上游账号</td></tr>'}
      </tbody></table></div>
      <div class="px-5 py-3 border-t border-white/[0.06] text-[11px] text-slate-500">「分配/更换 IP」从资源池取一条 available+healthy 的条目；「回收」删除该账号出口并把池条目释放回 available，账号保留但立即 Fail-Closed。legacy 共用的出口建议逐个更换以完成拆分。</div>
    </section>

    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06] flex flex-wrap items-center justify-between gap-3">
        <div><h2 class="text-sm font-semibold text-white">静态 IP 资源池（库存）</h2><p class="text-xs text-slate-400 mt-0.5">批量录入 → 探测验健康 → 从池自动分配；仅 <span class="mono">available + healthy</span> 的条目可被分配</p></div>
        <button id="pool-add-btn" class="inline-flex items-center gap-1.5 px-3.5 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white hover:bg-indigo-400"><i data-lucide="plus" class="w-3.5 h-3.5"></i>批量添加 IP</button>
      </div>
      <div class="px-5 py-3 flex flex-wrap gap-2 border-b border-white/[0.06]">
        ${poolStatChip('库存总数', s.total ?? state.admin.ipPool.length, 'indigo')}
        ${poolStatChip('可分配', s.allocatable ?? 0, 'emerald')}
        ${poolStatChip('可用', s.available ?? 0, 'cyan')}
        ${poolStatChip('健康', s.healthy ?? 0, 'emerald')}
        ${poolStatChip('已分配', s.assigned ?? 0, 'amber')}
        ${poolStatChip('已停用', s.disabled ?? 0, 'slate')}
      </div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs"><thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase"><th class="px-4 py-3">端点</th><th class="px-4 py-3">状态</th><th class="px-4 py-3">出口 IP / 地区 / 延迟</th><th class="px-4 py-3">健康</th><th class="px-4 py-3">绑定账号 / 用户</th><th class="px-4 py-3 text-right">操作</th></tr></thead><tbody class="divide-y divide-white/[0.05]">
        ${state.admin.ipPool.map((p) => `<tr>
          <td class="px-4 py-3.5"><div class="text-slate-100 font-medium">${esc(p.name || '未命名 IP')}</div><div class="mono text-[11px] text-cyan-300/80 mt-0.5">${esc(p.scheme)}://${esc(p.host)}:${esc(p.port)}</div></td>
          <td class="px-4 py-3.5">${poolStatusBadge(p.status)}</td>
          <td class="px-4 py-3.5"><div class="mono text-cyan-300">${esc(p.last_egress_ip || '待探测')}</div><div class="text-[11px] text-slate-500 mt-0.5">${esc(p.last_egress_country || '-')}${p.last_latency_ms ? ' · ' + esc(p.last_latency_ms) + 'ms' : ''}</div></td>
          <td class="px-4 py-3.5">${healthBadge(p.health_status)}</td>
          <td class="px-4 py-3.5"><div class="mono text-[11px] ${p.assigned_account_id ? 'text-amber-300' : 'text-slate-500'}">${p.assigned_account_id ? '账号 ' + esc(p.assigned_account_id.slice(0, 8)) + '…' : (p.assigned_user_id ? '占位分配' : '-')}</div><div class="mono text-[10px] text-slate-500 mt-0.5">${p.assigned_user_id ? '用户 ' + esc(p.assigned_user_id.slice(0, 8)) + '…' : '-'}</div></td>
          <td class="px-4 py-3.5 text-right whitespace-nowrap">
            <button data-pool-probe="${esc(p.id)}" class="px-2.5 py-1 rounded-lg bg-cyan-500/10 text-cyan-300 hover:bg-cyan-500/20 mr-1">探测</button>
            ${p.status === 'assigned' ? '<span class="text-[11px] text-slate-600">使用中 · 需先回收</span>' : `<button data-pool-toggle="${esc(p.id)}" data-pool-status="${esc(p.status)}" class="px-2.5 py-1 rounded-lg bg-amber-500/10 text-amber-300 hover:bg-amber-500/20 mr-1">${p.status === 'disabled' ? '启用' : '停用'}</button><button data-pool-delete="${esc(p.id)}" class="px-2.5 py-1 rounded-lg bg-rose-500/10 text-rose-300 hover:bg-rose-500/20">删除</button>`}
          </td>
        </tr>`).join('') || '<tr><td colspan="6" class="px-5 py-10 text-center text-slate-500">IP 池为空，点右上角「批量添加 IP」开始录入</td></tr>'}
      </tbody></table></div>
      <div class="px-5 py-3 border-t border-white/[0.06] text-[11px] text-slate-500">录入后需逐条「探测」变为 <span class="mono text-emerald-400">healthy</span> 才会进入可分配队列；已分配条目需在「账号与 IP」表回收后释放回库存。</div>
    </section>

    <section class="spotlight-card rounded-2xl overflow-hidden">
      <div class="px-5 py-4 border-b border-white/[0.06]"><h2 class="text-sm font-semibold text-white">已分配出口（proxy_endpoints）</h2><p class="text-xs text-slate-400 mt-0.5">每条出口专属绑定一个上游账号；支持重新探测与回收</p></div>
      <div class="overflow-x-auto"><table class="w-full text-left border-collapse text-xs"><thead><tr class="bg-white/[0.02] border-b border-white/[0.06] text-[10px] font-semibold text-slate-400 uppercase"><th class="px-5 py-3">用户</th><th class="px-5 py-3">名称</th><th class="px-5 py-3">绑定账号</th><th class="px-5 py-3">出口 IP</th><th class="px-5 py-3">地区</th><th class="px-5 py-3">延迟</th><th class="px-5 py-3">健康</th><th class="px-5 py-3 text-right">操作</th></tr></thead><tbody class="divide-y divide-white/[0.05]">
        ${state.admin.proxies.map((p) => `<tr><td class="px-5 py-3.5 mono text-slate-400">${esc(p.user_id || '-')}</td><td class="px-5 py-3.5 text-slate-100">${esc(p.name || '-')}</td><td class="px-5 py-3.5 mono text-[11px] ${p.upstream_account_id ? 'text-amber-300' : 'text-slate-600'}">${p.upstream_account_id ? esc(p.upstream_account_id.slice(0, 8)) + '…' : 'legacy'}</td><td class="px-5 py-3.5 mono text-cyan-300">${esc(p.last_egress_ip || '-')}</td><td class="px-5 py-3.5 text-slate-300">${esc(p.last_egress_country || '-')}</td><td class="px-5 py-3.5 mono text-slate-300">${p.last_latency_ms ? esc(p.last_latency_ms) + 'ms' : '-'}</td><td class="px-5 py-3.5">${healthBadge(p.health_status)}</td><td class="px-5 py-3.5 text-right whitespace-nowrap"><button data-reprobe="${esc(p.id)}" class="px-2.5 py-1 rounded-lg bg-cyan-500/10 text-cyan-300 hover:bg-cyan-500/20 mr-1">重新探测</button><button data-revoke-proxy="${esc(p.id)}" data-uid="${esc(p.user_id)}" class="px-2.5 py-1 rounded-lg bg-rose-500/10 text-rose-300 hover:bg-rose-500/20">回收</button></td></tr>`).join('') || '<tr><td colspan="8" class="px-5 py-10 text-center text-slate-500">暂无已分配 IP</td></tr>'}
      </tbody></table></div>
    </section>

    <section class="spotlight-card rounded-2xl p-5">
      <div class="flex items-center justify-between"><div><h2 class="text-sm font-semibold text-white">审计链完整性</h2><p class="text-xs text-slate-400 mt-0.5">公开可校验，任何历史记录被篡改都会断链</p></div><span class="px-3 py-1 rounded-full text-xs ${state.chain.ok === true ? 'bg-emerald-500/15 text-emerald-300' : 'bg-rose-500/15 text-rose-300'}">${state.chain.ok === true ? '完整' : state.chain.ok === false ? '断链' : '未知'}</span></div>
      <pre class="mono text-[11px] bg-[#05070d] border border-white/[0.08] text-cyan-200/90 p-3.5 rounded-xl mt-3 overflow-x-auto">${esc(JSON.stringify(state.chain, null, 2))}</pre>
    </section>

    <section><h2 class="text-sm font-semibold text-white mb-3">全局审计日志</h2>${logTableHtml(filteredLogs(state.admin.logs), 'admin')}</section>
  `;
  $('admin-refresh-btn').onclick = loadAndRender;
  $('pool-add-btn').onclick = openAddIPPoolModal;
  container.querySelectorAll('[data-acct-assign]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm('从资源池为该账号分配/更换一条独立静态 IP？其他账号的出口不受影响。')) return;
      btn.disabled = true;
      try {
        const r = await api('/admin/accounts/' + encodeURIComponent(btn.dataset.acctAssign) + '/assign-ip', { method: 'POST', body: '{}' });
        showToast(`已分配 ${r.proxy?.last_egress_ip || r.pool_item?.host || ''}`, 'success');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); btn.disabled = false; }
    };
  });
  container.querySelectorAll('[data-acct-revoke]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm('回收该账号的专属静态 IP？回收后该账号立即 Fail-Closed。')) return;
      try {
        await api('/admin/accounts/' + encodeURIComponent(btn.dataset.acctRevoke) + '/ip', { method: 'DELETE' });
        showToast('已回收并释放回资源池', 'info');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); }
    };
  });
  container.querySelectorAll('[data-assign-pool]').forEach((btn) => {
    btn.onclick = () => openAssignFromPoolModal(btn.dataset.assignPool, btn.dataset.email);
  });
  container.querySelectorAll('[data-pool-probe]').forEach((btn) => {
    btn.onclick = async () => {
      btn.disabled = true;
      showToast('正在经代理探测出口 IP…', 'info');
      try {
        const r = await api('/admin/ip-pool/' + encodeURIComponent(btn.dataset.poolProbe) + '/probe', { method: 'POST', body: '{}' });
        showToast(`探测完成：${r.probe?.health_status || ''} ${r.probe?.egress_ip || ''}`, r.probe?.health_status === 'healthy' ? 'success' : 'warning');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); btn.disabled = false; }
    };
  });
  container.querySelectorAll('[data-pool-toggle]').forEach((btn) => {
    btn.onclick = async () => {
      const next = btn.dataset.poolStatus === 'disabled' ? 'available' : 'disabled';
      try {
        await api('/admin/ip-pool/' + encodeURIComponent(btn.dataset.poolToggle), { method: 'PATCH', body: JSON.stringify({ status: next }) });
        showToast(next === 'disabled' ? '已停用，不再参与自动分配' : '已启用，健康条目可重新分配', 'success');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); }
    };
  });
  container.querySelectorAll('[data-pool-delete]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm('从 IP 池删除该条目？此操作不可撤销。')) return;
      try {
        await api('/admin/ip-pool/' + encodeURIComponent(btn.dataset.poolDelete), { method: 'DELETE' });
        showToast('已从池中删除', 'info');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); }
    };
  });
  container.querySelectorAll('[data-assign-ip]').forEach((btn) => {
    btn.onclick = () => openAssignIPModal(btn.dataset.assignIp, btn.dataset.email);
  });
  container.querySelectorAll('[data-plan]').forEach((btn) => {
    btn.onclick = () => openPlanModal(btn.dataset.plan, btn.dataset.email, {
      plan_status: btn.dataset.planStatus,
      plan_expires_at: btn.dataset.planExpires,
      max_concurrency: Number(btn.dataset.maxConcurrency),
      status: btn.dataset.userStatus,
    });
  });
  container.querySelectorAll('[data-revoke-ip]').forEach((btn) => {
    btn.onclick = () => openRevokeIPModal(btn.dataset.revokeIp, btn.dataset.email);
  });
  container.querySelectorAll('[data-reprobe]').forEach((btn) => {
    btn.onclick = async () => {
      try {
        const r = await api('/admin/proxies/' + encodeURIComponent(btn.dataset.reprobe) + '/probe', { method: 'POST', body: '{}' });
        showToast(`探测完成：${r.probe?.health_status || ''} ${r.probe?.egress_ip || ''}`, r.probe?.health_status === 'healthy' ? 'success' : 'warning');
        await loadData(); render();
      } catch (e) { showToast(e.message, 'error'); }
    };
  });
  container.querySelectorAll('[data-revoke-proxy]').forEach((btn) => {
    btn.onclick = () => openRevokeIPModal(btn.dataset.uid, btn.dataset.uid);
  });
  container.querySelectorAll('[data-admin-log-detail]').forEach((row) => {
    row.onclick = () => openLogDetailModal(row.dataset.adminLogDetail, 'admin');
  });
}

function openAssignIPModal(userId, email) {
  openModal({
    title: '为用户分配专属静态 IP',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div class="p-3 rounded-xl bg-indigo-500/10 border border-indigo-500/25 text-indigo-200">用户：<span class="mono">${esc(email)}</span><br/>一个用户仅保留一条自动分配静态 IP；重复分配会替换旧配置。</div>
      <div class="grid grid-cols-3 gap-3"><div><label class="block text-slate-300 mb-1.5">协议</label><select id="f-ip-scheme" class="w-full px-3 py-2.5 rounded-xl bg-[#121726] border border-white/[0.1] text-white"><option>http</option><option>socks5</option></select></div><div class="col-span-2"><label class="block text-slate-300 mb-1.5">静态 IP / 主机</label><input id="f-ip-host" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" placeholder="1.2.3.4" /></div></div>
      <div class="grid grid-cols-3 gap-3"><div><label class="block text-slate-300 mb-1.5">端口</label><input id="f-ip-port" type="number" value="8080" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div><div><label class="block text-slate-300 mb-1.5">用户名</label><input id="f-ip-user" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white" /></div><div><label class="block text-slate-300 mb-1.5">密码</label><input id="f-ip-pass" type="password" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white" /></div></div>
      <p class="text-slate-500">当前为手动占位入口；接入你提供的静态 IP 获取接口后，此处将由付款回调自动完成。</p>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">取消</button><button id="m-assign" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white">分配并探测</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-assign').onclick = async () => {
    const body = {
      scheme: $('f-ip-scheme').value, host: $('f-ip-host').value.trim(), port: Number($('f-ip-port').value),
      username: $('f-ip-user').value.trim(), password: $('f-ip-pass').value,
    };
    if (!body.host || !body.port) { showToast('请填写主机与端口', 'warning'); return; }
    try {
      const r = await api('/admin/users/' + encodeURIComponent(userId) + '/provision-ip', { method: 'POST', body: JSON.stringify(body) });
      closeModal();
      showToast(`已分配：${r.proxy?.last_egress_ip || body.host} · ${r.probe?.health_status || ''}`, 'success');
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

function openPlanModal(userId, email, u) {
  openModal({
    title: '管理用户套餐',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div class="p-3 rounded-xl bg-indigo-500/10 border border-indigo-500/25 text-indigo-200">用户：<span class="mono">${esc(email)}</span><br/>套餐状态参与网关出站鉴权：非 <span class="mono">active</span> 会立即 Fail-Closed，已有请求不再放行。</div>
      <div class="grid grid-cols-2 gap-3">
        <div><label class="block text-slate-300 mb-1.5">套餐状态</label><select id="f-plan-status" class="w-full px-3 py-2.5 rounded-xl bg-[#121726] border border-white/[0.1] text-white"><option value="unpaid">unpaid · 未付款</option><option value="active">active · 开通</option><option value="expired">expired · 到期</option><option value="cancelled">cancelled · 取消</option></select></div>
        <div><label class="block text-slate-300 mb-1.5">账号状态</label><select id="f-user-status" class="w-full px-3 py-2.5 rounded-xl bg-[#121726] border border-white/[0.1] text-white"><option value="active">active · 正常</option><option value="disabled">disabled · 停用</option></select></div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div><label class="block text-slate-300 mb-1.5">续期天数（留空不改）</label><input id="f-plan-days" type="number" min="1" placeholder="如 30 / 90" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div>
        <div><label class="block text-slate-300 mb-1.5">并发上限</label><input id="f-plan-concurrency" type="number" min="1" value="${esc(u.max_concurrency || 5)}" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div>
      </div>
      <p class="text-slate-500">当前到期：${esc(u.plan_expires_at || '未设置')}</p>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">取消</button><button id="m-save-plan" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white">保存套餐</button>`,
  });
  $('f-plan-status').value = u.plan_status || 'unpaid';
  $('f-user-status').value = u.status || 'active';
  $('m-cancel').onclick = closeModal;
  $('m-save-plan').onclick = async () => {
    const days = $('f-plan-days').value.trim();
    const body = {
      plan_status: $('f-plan-status').value,
      status: $('f-user-status').value,
      max_concurrency: Number($('f-plan-concurrency').value),
    };
    if (days) body.plan_days = Number(days);
    try {
      await api('/admin/users/' + encodeURIComponent(userId) + '/plan', { method: 'PATCH', body: JSON.stringify(body) });
      closeModal();
      showToast('套餐已更新', 'success');
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

function openRevokeIPModal(userId, email) {
  openModal({
    title: '回收专属静态 IP',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div class="p-3 rounded-xl bg-rose-500/10 border border-rose-500/25 text-rose-200">用户：<span class="mono">${esc(email)}</span><br/>回收后该用户绑定的上游账号失去代理，套餐同步置为 <span class="mono">expired</span>，所有网关请求立即 Fail-Closed。此操作不可撤销，但可重新分配 IP 恢复。</div>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400">取消</button><button id="m-revoke-ip" class="px-4 py-2 rounded-xl text-xs font-semibold bg-rose-600 text-white">确认回收</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-revoke-ip').onclick = async () => {
    try {
      await api('/admin/users/' + encodeURIComponent(userId) + '/ip', { method: 'DELETE' });
      closeModal();
      showToast('已回收静态 IP，套餐已停用', 'success');
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

function poolStatChip(label, value, color) {
  const map = {
    indigo: 'bg-indigo-500/10 text-indigo-300 border-indigo-400/25',
    emerald: 'bg-emerald-500/10 text-emerald-300 border-emerald-400/25',
    cyan: 'bg-cyan-500/10 text-cyan-300 border-cyan-400/25',
    amber: 'bg-amber-500/10 text-amber-300 border-amber-400/25',
    slate: 'bg-white/[0.04] text-slate-400 border-white/[0.08]',
  };
  return `<span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg border text-[11px] ${map[color] || map.slate}"><span class="text-slate-500">${esc(label)}</span><strong class="mono text-sm">${esc(value)}</strong></span>`;
}

function poolStatusBadge(status) {
  const map = {
    available: ['bg-emerald-500/12 text-emerald-300 border-emerald-500/30', '可用'],
    assigned: ['bg-amber-500/12 text-amber-300 border-amber-500/30', '已分配'],
    disabled: ['bg-white/[0.05] text-slate-400 border-white/[0.1]', '已停用'],
  };
  const [cls, label] = map[status] || map.disabled;
  return `<span class="px-2.5 py-0.5 rounded-full text-[11px] font-medium border ${cls}">${label}</span>`;
}

function openAddIPPoolModal() {
  openModal({
    title: '批量添加静态 IP 到资源池',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div class="p-3 rounded-xl bg-indigo-500/10 border border-indigo-500/25 text-indigo-200 leading-relaxed">
        每行一条，支持 <span class="mono">host:port</span>、<span class="mono">host:port:user:pass</span> 或 <span class="mono">scheme://user:pass@host:port</span>；<span class="mono">#</span> 开头为注释。端点重复自动跳过，凭证 <span class="mono">AES-256-GCM</span> 加密落库，私网地址直接拒绝。<br/>录入后须逐条「探测」变为 healthy 才可分配。
      </div>
      <div><label class="block font-medium text-slate-300 mb-1.5">批量文本</label>
        <textarea id="f-pool-lines" rows="6" spellcheck="false" class="w-full px-3 py-2.5 rounded-xl bg-[#05070d] border border-white/[0.1] text-cyan-100 mono text-[11px] leading-relaxed focus:outline-none focus:border-indigo-400" placeholder="# host:port:user:pass&#10;216.223.21.54:8022:qdqrxwzcez:iormiiumiomty&#10;# 或完整 URL&#10;http://user:pass@1.2.3.4:8080"></textarea>
      </div>
      <div class="grid grid-cols-3 gap-3">
        <div><label class="block text-slate-300 mb-1.5">默认协议（文本未写 scheme 时）</label><select id="f-pool-scheme" class="w-full px-3 py-2 rounded-xl bg-[#121726] border border-white/[0.1] text-white"><option value="http">http</option><option value="https">https</option><option value="socks5">socks5</option></select></div>
        <div class="col-span-2"><label class="block text-slate-300 mb-1.5">备注（可选，统一写入本批）</label><input id="f-pool-note" class="w-full px-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white" placeholder="例如：供应商 A · 美国住宅静态" /></div>
      </div>
      <div class="p-3 rounded-xl bg-emerald-500/10 border border-emerald-500/25 text-emerald-300">也可在下方单条填写后「追加到文本」，一次提交多条。</div>
      <div class="grid grid-cols-4 gap-2 items-end">
        <div class="col-span-2"><label class="block text-slate-400 mb-1.5">主机</label><input id="f-pool-one-host" class="w-full px-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" placeholder="1.2.3.4" /></div>
        <div><label class="block text-slate-400 mb-1.5">端口</label><input id="f-pool-one-port" type="number" value="8080" class="w-full px-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div>
        <div><label class="block text-slate-400 mb-1.5">账号:密码（可选）</label><input id="f-pool-one-auth" class="w-full px-3 py-2 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" placeholder="u:p" /></div>
      </div>
      <button id="f-pool-append" type="button" class="px-3 py-1.5 rounded-lg bg-white/[0.06] border border-white/[0.1] text-slate-200 hover:bg-white/[0.1]">追加到文本</button>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400 hover:bg-white/[0.06]">取消</button><button id="m-save-pool" class="px-4 py-2 rounded-xl text-xs font-semibold bg-indigo-500 text-white hover:bg-indigo-400">加入资源池</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('f-pool-append').onclick = () => {
    const host = $('f-pool-one-host').value.trim();
    const port = $('f-pool-one-port').value.trim();
    const auth = $('f-pool-one-auth').value.trim();
    if (!host || !port) { showToast('请填写主机与端口', 'warning'); return; }
    const line = $('f-pool-scheme').value + '://' + (auth ? auth + '@' : '') + host + ':' + port;
    const ta = $('f-pool-lines');
    ta.value = (ta.value.trim() ? ta.value.replace(/\s*$/, '') + '\n' : '') + line;
    ta.focus();
    showToast('已追加到批量文本', 'success');
  };
  $('m-save-pool').onclick = async () => {
    const lines = $('f-pool-lines').value;
    if (!lines.trim()) { showToast('请填入至少一条 IP', 'warning'); return; }
    try {
      const r = await api('/admin/ip-pool', { method: 'POST', body: JSON.stringify({
        lines, scheme: $('f-pool-scheme').value, note: $('f-pool-note').value.trim(),
      }) });
      closeModal();
      const warns = [...(r.parse_errors || []), ...(r.errors || [])];
      showToast(`已加入 ${r.added} 条（重复跳过 ${r.duplicates}）` + (warns.length ? `，${warns.length} 条被拒绝` : ''), r.added ? 'success' : 'warning');
      if (warns.length) console.warn('IP pool rejected:', warns);
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

function openAssignFromPoolModal(userId, email) {
  const s = state.admin.poolStats || {};
  openModal({
    title: '从 IP 池自动分配专属静态 IP',
    bodyHtml: `<div class="space-y-3.5 text-xs">
      <div class="p-3 rounded-xl bg-emerald-500/10 border border-emerald-500/25 text-emerald-200 leading-relaxed">用户：<span class="mono">${esc(email)}</span><br/>系统按入库顺序取一条 <span class="mono">available + healthy</span> 的静态 IP，写入该用户专属出口，自动重绑其上游账号，并把套餐置为 <span class="mono">active</span>。</div>
      <div class="p-3 rounded-xl bg-white/[0.03] border border-white/[0.08] text-slate-300">当前可分配：<strong class="mono text-emerald-300">${esc(s.allocatable ?? 0)}</strong> 条 / 库存 ${esc(s.total ?? state.admin.ipPool.length)} 条。若为 0，请先在池中「探测」出 healthy 条目。</div>
      <div><label class="block text-slate-300 mb-1.5">套餐天数（留空默认 30 天）</label><input id="f-pool-days" type="number" min="1" placeholder="30" class="w-full px-3 py-2.5 rounded-xl bg-white/[0.04] border border-white/[0.1] text-white mono" /></div>
      <p class="text-slate-500">重复分配会先释放该用户旧的池占用再取新的，不会造成库存泄漏。接入真实静态 IP 供应商接口后，此动作可由付款回调自动触发。</p>
    </div>`,
    footerHtml: `<button id="m-cancel" class="px-3.5 py-2 rounded-xl text-xs text-slate-400 hover:bg-white/[0.06]">取消</button><button id="m-assign-pool" class="px-4 py-2 rounded-xl text-xs font-semibold bg-emerald-600 text-white hover:bg-emerald-500">从池分配</button>`,
  });
  $('m-cancel').onclick = closeModal;
  $('m-assign-pool').onclick = async () => {
    const days = $('f-pool-days').value.trim();
    const body = days ? { plan_days: Number(days) } : {};
    try {
      const r = await api('/admin/users/' + encodeURIComponent(userId) + '/assign-from-pool', { method: 'POST', body: JSON.stringify(body) });
      closeModal();
      showToast(`已分配 ${r.proxy?.last_egress_ip || r.pool_item?.host || ''} 并自动重绑账号`, 'success');
      await loadData(); render();
    } catch (e) { showToast(e.message, 'error'); }
  };
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------
async function loadAndRender() {
  await loadData();
  render();
}

document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.onclick = () => { state.currentPage = btn.dataset.nav; render(); };
  });
  const sidebar = $('sidebar');
  const backdrop = $('mobile-backdrop');
  const openSidebar = () => { sidebar.classList.remove('-translate-x-full'); backdrop.classList.remove('hidden'); };
  const closeSidebar = () => { sidebar.classList.add('-translate-x-full'); backdrop.classList.add('hidden'); };
  $('open-sidebar-btn').onclick = openSidebar;
  $('close-sidebar-btn').onclick = closeSidebar;
  backdrop.onclick = closeSidebar;

  $('refresh-btn').onclick = loadAndRender;
  $('quick-create-key-btn').onclick = openCreateKeyModal;
  $('modal-close-btn').onclick = closeModal;
  $('modal-root').onclick = (e) => { if (e.target.id === 'modal-root') closeModal(); };

  render();
  if (token) loadAndRender();
  else goLogin();
  icons(document);
});
