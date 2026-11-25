package gateway

import "net/http"

func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.LiveUsage())
}

func (s *Server) handleUsageHub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(usageHubHTML))
}

const usageHubHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>acpx — Usage Report Hub</title>
<style>
  :root {
    --bg: #0d1117; --surf: #161b22; --elev: #1c2128; --border: #30363d;
    --fg: #e6edf3; --muted: #8b949e; --accent: #58a6ff; --ok: #3fb950;
    --warn: #d29922; --err: #f85149; --radius: 10px;
    --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--fg); font-family: var(--font); font-size: 14px; line-height: 1.45; }
  header {
    background: var(--surf); border-bottom: 1px solid var(--border);
    padding: 12px 24px; display: flex; align-items: center; justify-content: space-between; gap: 16px;
    position: sticky; top: 0; z-index: 10;
  }
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 15px; }
  .pill { background: var(--accent); color: #0d1117; padding: 2px 8px; border-radius: 6px; font-family: var(--mono); font-size: 12px; }
  .controls { display: flex; align-items: center; gap: 10px; }
  button, input {
    background: var(--elev); color: var(--fg); border: 1px solid var(--border);
    border-radius: 7px; padding: 6px 12px; font-size: 13px; font-family: inherit;
  }
  button { cursor: pointer; }
  button:hover { border-color: var(--accent); }
  button.primary { background: var(--accent); color: #0d1117; border-color: var(--accent); font-weight: 600; }
  main { max-width: 1240px; margin: 20px auto; padding: 0 20px 40px; display: grid; gap: 16px; }
  .kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 12px; }
  .card { background: var(--surf); border: 1px solid var(--border); border-radius: var(--radius); padding: 16px; }
  .kpi-label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
  .kpi-val { font-size: 26px; font-weight: 700; margin-top: 6px; font-family: var(--mono); }
  .kpi-sub { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .grid2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(460px, 1fr)); gap: 16px; }
  h2 { font-size: 14px; font-weight: 600; margin-bottom: 12px; display: flex; justify-content: space-between; align-items: center; }
  .chart { display: flex; align-items: flex-end; gap: 4px; height: 140px; padding-top: 12px; border-bottom: 1px solid var(--border); }
  .bar-wrap { flex: 1; display: flex; flex-direction: column; align-items: center; height: 100%; justify-content: flex-end; position: relative; }
  .bar { width: 100%; min-height: 2px; background: var(--accent); border-radius: 4px 4px 0 0; transition: height .2s; }
  .bar.err { background: var(--err); }
  .bar-lbl { font-size: 10px; color: var(--muted); margin-top: 6px; font-family: var(--mono); }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { text-align: left; color: var(--muted); font-weight: 600; font-size: 11px; text-transform: uppercase; padding: 8px 6px; border-bottom: 1px solid var(--border); }
  td { padding: 8px 6px; border-bottom: 1px solid #21262d; font-family: var(--mono); font-size: 12px; }
  tr:last-child td { border-bottom: none; }
  .badge { display: inline-block; padding: 2px 7px; border-radius: 999px; font-size: 11px; font-weight: 600; }
  .b-ok { background: rgba(63,185,80,.15); color: var(--ok); }
  .b-warn { background: rgba(210,153,34,.15); color: var(--warn); }
  .b-err { background: rgba(248,81,73,.15); color: var(--err); }
  .share-bg { width: 80px; height: 6px; background: var(--elev); border-radius: 4px; overflow: hidden; display: inline-block; vertical-align: middle; }
  .share-fg { height: 100%; background: var(--accent); }
  .q-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 12px; }
  .q-card { background: var(--elev); border: 1px solid var(--border); border-radius: 8px; padding: 12px; display: grid; gap: 8px; }
  .q-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
  .q-name { font-family: var(--mono); font-weight: 600; font-size: 13px; }
  .q-row { display: grid; gap: 4px; }
  .q-meta { display: flex; justify-content: space-between; font-size: 12px; font-family: var(--mono); }
  .q-bar { width: 100%; height: 8px; background: var(--bg); border-radius: 5px; overflow: hidden; }
  .q-fill { height: 100%; border-radius: 5px; transition: width .25s; }
  .q-ok { background: var(--ok); }
  .q-warn { background: var(--warn); }
  .q-err { background: var(--err); }
  #authBox { max-width: 440px; margin: 80px auto; text-align: center; }
  #authBox input { width: 100%; margin: 14px 0 10px; font-family: var(--mono); }
  .muted { color: var(--muted); }
  .hidden { display: none !important; }
  .err-banner { background: rgba(248,81,73,.14); border: 1px solid var(--err); color: var(--err); padding: 10px 14px; border-radius: 8px; }
</style>
</head>
<body>
<header>
  <div class="brand"><span class="pill">◆ acpx</span> Usage Report Hub</div>
  <div class="controls">
    <span id="liveDot" class="muted" style="font-size:12px;">localhost</span>
    <button id="refreshQuotaBtn">Refresh Limits</button>
    <button id="checkHealthBtn">Check Health</button>
    <button id="toggleChart">24h / 14d</button>
    <button id="toggleAuto">Auto: ON</button>
    <button id="refreshBtn" class="primary">Refresh</button>
    <button id="logoutBtn" class="hidden">Lock</button>
  </div>
</header>

<div id="authBox" class="card hidden">
  <h2 style="justify-content:center;">Local API Key Required</h2>
  <p class="muted">Paste your local <code>acpx_...</code> key (from the TUI Dashboard or <code>acpx export-env</code>).</p>
  <input id="keyInput" type="password" placeholder="acpx_...">
  <button id="unlockBtn" class="primary" style="width:100%;">Unlock Usage Hub</button>
  <p id="authErr" class="muted" style="color:var(--err);margin-top:8px;"></p>
</div>

<main id="app" class="hidden">
  <div id="banners"></div>
  <section class="kpis">
    <div class="card"><div class="kpi-label">Total Requests</div><div class="kpi-val" id="kReq">0</div><div class="kpi-sub" id="kReqSub">24h: 0 · 7d: 0</div></div>
    <div class="card"><div class="kpi-label">Success Rate</div><div class="kpi-val" id="kRate">100%</div><div class="kpi-sub" id="kErrSub">0 errors</div></div>
    <div class="card"><div class="kpi-label">Total Tokens</div><div class="kpi-val" id="kTok">0</div><div class="kpi-sub" id="kTokSub">in: 0 · out: 0</div></div>
    <div class="card"><div class="kpi-label">Avg Latency</div><div class="kpi-val" id="kLat">0 ms</div><div class="kpi-sub" id="kModels">0 models</div></div>
  </section>

  <section class="card">
    <h2><span>Subscription Limits &amp; Remaining Quota</span><span id="quotaMeta" class="muted" style="font-size:12px;font-weight:400;">hourly · weekly · monthly</span></h2>
    <div id="quotaGrid" class="q-grid"></div>
  </section>

  <section class="card">
    <h2><span id="chartTitle">Requests — Last 24 Hours</span><span class="muted" id="chartMeta" style="font-size:12px;font-weight:400;"></span></h2>
    <div id="chart" class="chart"></div>
  </section>

  <section class="grid2">
    <div class="card">
      <h2><span>By Account &amp; Live Health</span><span id="healthMeta" class="muted" style="font-size:12px;font-weight:400;"></span></h2>
      <table><thead><tr><th>Account</th><th>State</th><th>Probe</th><th>Reqs</th><th>Err</th><th>Tokens</th></tr></thead><tbody id="acctBody"></tbody></table>
    </div>
    <div class="card">
      <h2>By Model</h2>
      <table><thead><tr><th>Model</th><th>Reqs</th><th>Tokens</th><th>Avg ms</th><th>Share</th></tr></thead><tbody id="modelBody"></tbody></table>
    </div>
  </section>

  <section class="card">
    <h2>Recent Requests (last 50)</h2>
    <table><thead><tr><th>Time</th><th>Endpoint</th><th>Model</th><th>Account</th><th>Status</th><th>Tokens</th><th>Latency</th></tr></thead><tbody id="recentBody"></tbody></table>
  </section>
</main>

<script>
const qs = new URLSearchParams(location.search);
if (qs.get('key')) {
  sessionStorage.setItem('acpx_key', qs.get('key'));
  history.replaceState({}, '', location.pathname);
}
let apiKey = sessionStorage.getItem('acpx_key') || '';
let chartMode = '24h';
let autoRefresh = true;
let lastUsage = null;
let lastState = null;
let lastHealth = null;
let lastQuota = null;

const $ = id => document.getElementById(id);
const fmt = n => Number(n || 0).toLocaleString();
const esc = s => String(s || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

function showAuth(msg) {
  $('app').classList.add('hidden');
  $('logoutBtn').classList.add('hidden');
  $('authBox').classList.remove('hidden');
  $('authErr').textContent = msg || '';
}

function showApp() {
  $('authBox').classList.add('hidden');
  $('app').classList.remove('hidden');
  $('logoutBtn').classList.remove('hidden');
}

async function fetchData() {
  if (!apiKey) { showAuth(); return; }
  try {
    const h = { 'Authorization': 'Bearer ' + apiKey };
    const [uRes, sRes, hRes, qRes] = await Promise.all([
      fetch('/admin/usage', { headers: h }),
      fetch('/admin/state', { headers: h }),
      fetch('/admin/health', { headers: h }),
      fetch('/admin/quota', { headers: h }),
    ]);
    if (uRes.status === 401 || sRes.status === 401) {
      sessionStorage.removeItem('acpx_key');
      apiKey = '';
      showAuth('Invalid local API key.');
      return;
    }
    lastUsage = await uRes.json();
    lastState = await sRes.json();
    lastHealth = hRes.ok ? await hRes.json() : null;
    lastQuota = qRes.ok ? await qRes.json() : null;
    showApp();
    render();
    $('liveDot').textContent = '● ' + (lastState.address || '127.0.0.1') + ' · updated ' + new Date().toLocaleTimeString();
  } catch (e) {
    $('liveDot').textContent = '○ offline: ' + e.message;
  }
}

function stateBadge(st) {
  if (st === 'ready') return '<span class="badge b-ok">ready</span>';
  if (st === 'busy') return '<span class="badge b-warn">busy</span>';
  if (st === 'cooling_down' || st === 'login_needed') return '<span class="badge b-warn">' + esc(st) + '</span>';
  return '<span class="badge b-err">' + esc(st || 'unknown') + '</span>';
}

function fmtReset(iso) {
  if (!iso) return '';
  const diffMs = new Date(iso).getTime() - Date.now();
  if (diffMs <= 0) return 'resets now';
  const mins = Math.round(diffMs / 60000);
  if (mins < 60) return 'resets in ' + mins + 'm';
  const hrs = Math.floor(mins / 60);
  if (hrs < 48) return 'resets in ' + hrs + 'h ' + (mins % 60) + 'm';
  return 'resets in ' + Math.floor(hrs / 24) + 'd ' + (hrs % 24) + 'h';
}

function renderQuotaWindow(w) {
  if (!w) return '';
  const used = Math.max(0, Math.min(100, Number(w.used_percent || 0)));
  const left = Math.max(0, Math.min(100, Number(w.left_percent != null ? w.left_percent : (100 - used))));
  const cls = used >= 90 ? 'q-err' : (used >= 70 ? 'q-warn' : 'q-ok');
  const countTxt = w.limit_count ? (' (' + fmt(w.used_count) + '/' + fmt(w.limit_count) + ')') : '';
  const resetTxt = fmtReset(w.resets_at);
  return '<div class="q-row">' +
    '<div class="q-meta"><span>' + esc(w.label) + countTxt + '</span><span><strong>' + left.toFixed(0) + '% left</strong>' + (resetTxt ? ' · <span class="muted">' + esc(resetTxt) + '</span>' : '') + '</span></div>' +
    '<div class="q-bar"><div class="q-fill ' + cls + '" style="width:' + left + '%"></div></div>' +
    '</div>';
}

function render() {
  const u = lastUsage || {};
  const s = lastState || {};

  // Subscription Limits cards
  const qList = (lastQuota && lastQuota.accounts) || [];
  if (lastQuota && lastQuota.updated_at && !String(lastQuota.updated_at).startsWith('0001-')) {
    $('quotaMeta').textContent = 'updated ' + new Date(lastQuota.updated_at).toLocaleTimeString();
  }
  const fmtTok = n => {
    n = Number(n || 0);
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
    return String(n);
  };
  $('quotaGrid').innerHTML = qList.map(q => {
    const isLive = q.source && (q.source.endsWith('-live') || q.source === 'claude-logs');
    const srcBadge = isLive
      ? '<span class="badge b-ok">' + esc(q.plan_type ? q.plan_type : (q.source === 'claude-logs' ? 'claude' : 'live')) + '</span>'
      : (q.source === 'local-budget'
        ? '<span class="badge b-warn">budget</span>'
        : '<span class="badge" style="background:rgba(139,148,158,.15);color:var(--muted);">local</span>');
    const tokNote = (q.tokens_5h || q.tokens_7d) ? (' · tok 5h=' + fmtTok(q.tokens_5h) + ' 7d=' + fmtTok(q.tokens_7d)) : '';
    let body = renderQuotaWindow(q.primary) + renderQuotaWindow(q.secondary);
    if (!body && q.credits) {
      body = '<div style="font-family:var(--mono);font-size:13px;font-weight:600;color:var(--ok);">' + esc(q.credits) + '</div>' +
        '<div class="muted" style="font-size:11px;font-family:var(--mono);">5h: ' + fmt(q.local_5h_reqs) + ' reqs · 7d: ' + fmt(q.local_7d_reqs) + ' reqs' + tokNote + '</div>';
    } else if (!body) {
      body = '<div class="muted" style="font-size:12px;">' +
        'Local traffic: <strong>' + fmt(q.local_5h_reqs) + '</strong> reqs (5h) · <strong>' + fmt(q.local_7d_reqs) + '</strong> reqs (7d)' + tokNote +
        (q.error ? '<br><span style="color:var(--warn);">' + esc(q.error) + '</span>' : '<br>Provider has no live quota API — set hourly/weekly budgets in Accounts.') +
        '</div>';
    } else {
      body += '<div class="muted" style="font-size:11px;font-family:var(--mono);">acpx traffic: 1h=' + fmt(q.local_1h_reqs) + ' · 5h=' + fmt(q.local_5h_reqs) + ' · 7d=' + fmt(q.local_7d_reqs) + tokNote + (q.credits ? ' · ' + esc(q.credits) : '') + '</div>';
    }
    return '<div class="q-card"><div class="q-head"><span class="q-name">' + esc(q.account_name) + '</span>' + srcBadge + '</div>' + body + '</div>';
  }).join('') || '<div class="muted">No accounts configured yet.</div>';

  $('banners').innerHTML = (s.banners || []).map(b =>
    '<div class="err-banner">! ' + esc(b.account_name) + ': ' + esc(b.message) + '</div>'
  ).join('');

  $('kReq').textContent = fmt(u.total_requests);
  $('kReqSub').textContent = '24h: ' + fmt(u.requests_24h) + ' · 7d: ' + fmt(u.requests_7d);
  $('kRate').textContent = (u.total_requests ? u.success_rate.toFixed(1) : '100.0') + '%';
  $('kErrSub').textContent = fmt(u.error_requests) + ' errors';
  $('kTok').textContent = fmt(u.total_tokens);
  $('kTokSub').textContent = 'in: ' + fmt(u.prompt_tokens) + ' · out: ' + fmt(u.completion_tokens) + ' · 24h: ' + fmt(u.tokens_24h);
  $('kLat').textContent = fmt(u.avg_latency_ms) + ' ms';
  $('kModels').textContent = fmt(s.models_count) + ' exposed models · ' + esc(s.secrets_backend || '');

  const buckets = chartMode === '24h' ? (u.hourly_24h || []) : (u.daily_14d || []);
  $('chartTitle').textContent = chartMode === '24h' ? 'Requests — Last 24 Hours' : 'Requests — Last 14 Days';
  const maxReq = Math.max(1, ...buckets.map(b => b.requests));
  $('chartMeta').textContent = 'peak: ' + fmt(maxReq) + ' reqs';
  $('chart').innerHTML = buckets.map((b, i) => {
    const pct = Math.round((b.requests / maxReq) * 100);
    const showLbl = chartMode === '14d' || i % 3 === 0 || i === buckets.length - 1;
    const tip = b.label + ': ' + b.requests + ' reqs, ' + b.errors + ' errs, ' + fmt(b.tokens) + ' tokens';
    return '<div class="bar-wrap" title="' + esc(tip) + '">' +
      '<div class="bar' + (b.errors > 0 && b.errors === b.requests ? ' err' : '') + '" style="height:' + Math.max(pct, b.requests ? 4 : 1) + '%"></div>' +
      '<div class="bar-lbl">' + (showLbl ? esc(b.label) : '&nbsp;') + '</div></div>';
  }).join('');

  const healthByKey = {};
  (s.accounts || []).forEach(a => { healthByKey[a.backend + '/' + a.name] = a; });
  const usageAccts = u.by_account || [];
  const mergedAccts = new Map();
  (s.accounts || []).forEach(a => {
    const k = a.backend + '/' + a.name;
    mergedAccts.set(k, { account: k, state: a.state, requests: 0, errors: 0, total_tokens: 0, avg_latency_ms: 0 });
  });
  usageAccts.forEach(a => {
    const cur = mergedAccts.get(a.account) || { account: a.account, state: (healthByKey[a.account]||{}).state || 'ready' };
    Object.assign(cur, a);
    mergedAccts.set(a.account, cur);
  });
  const probeByKey = {};
  (lastHealth && lastHealth.accounts || []).forEach(p => { probeByKey[p.account_name] = p; });
  if (lastHealth) {
    $('healthMeta').textContent = lastHealth.healthy_count + '/' + lastHealth.total_count + ' healthy' +
      (lastHealth.last_sweep_at ? ' · ' + new Date(lastHealth.last_sweep_at).toLocaleTimeString() : '');
  }
  const probeBadge = p => {
    if (!p || p.status === 'unknown') return '<span class="muted">—</span>';
    if (p.status === 'healthy') return '<span class="badge b-ok">' + fmt(p.latency_ms) + 'ms</span>';
    return '<span class="badge b-err" title="' + esc(p.error || '') + '">' + esc(p.status) + '</span>';
  };
  $('acctBody').innerHTML = Array.from(mergedAccts.values()).map(a =>
    '<tr><td>' + esc(a.account) + '</td><td>' + stateBadge(a.state) + '</td><td>' + probeBadge(probeByKey[a.account]) + '</td><td>' + fmt(a.requests) + '</td><td>' + fmt(a.errors) + '</td><td>' + fmt(a.total_tokens) + '</td></tr>'
  ).join('') || '<tr><td colspan="6" class="muted">No accounts yet</td></tr>';

  const totalReq = Math.max(1, u.total_requests || 1);
  $('modelBody').innerHTML = (u.by_model || []).map(m => {
    const share = Math.round((m.requests / totalReq) * 100);
    return '<tr><td>' + esc(m.model) + '</td><td>' + fmt(m.requests) + '</td><td>' + fmt(m.total_tokens) + '</td><td>' + fmt(m.avg_latency_ms) + '</td><td><span class="share-bg"><span class="share-fg" style="width:' + share + '%"></span></span> ' + share + '%</td></tr>';
  }).join('') || '<tr><td colspan="5" class="muted">No requests recorded yet</td></tr>';

  $('recentBody').innerHTML = (u.recent || []).map(r => {
    const ok = r.status >= 200 && r.status < 400;
    const t = new Date(r.time).toLocaleTimeString();
    return '<tr><td>' + esc(t) + '</td><td>' + esc(r.endpoint) + '</td><td>' + esc(r.model) + '</td><td>' + esc(r.account) + '</td><td><span class="badge ' + (ok ? 'b-ok' : 'b-err') + '">' + r.status + '</span></td><td>' + fmt(r.total_tokens) + (r.estimated ? '~' : '') + '</td><td>' + fmt(r.duration_ms) + ' ms</td></tr>';
  }).join('') || '<tr><td colspan="7" class="muted">Waiting for traffic...</td></tr>';
}

$('unlockBtn').onclick = () => {
  apiKey = $('keyInput').value.trim();
  if (apiKey) { sessionStorage.setItem('acpx_key', apiKey); fetchData(); }
};
$('keyInput').onkeydown = e => { if (e.key === 'Enter') $('unlockBtn').click(); };
$('logoutBtn').onclick = () => { sessionStorage.removeItem('acpx_key'); apiKey = ''; showAuth(); };
$('refreshBtn').onclick = fetchData;
$('refreshQuotaBtn').onclick = async () => {
  if (!apiKey) return;
  $('refreshQuotaBtn').textContent = 'Refreshing...';
  try {
    await fetch('/admin/quota/refresh', { method: 'POST', headers: { 'Authorization': 'Bearer ' + apiKey } });
    await fetchData();
  } finally {
    $('refreshQuotaBtn').textContent = 'Refresh Limits';
  }
};
$('checkHealthBtn').onclick = async () => {
  if (!apiKey) return;
  $('checkHealthBtn').textContent = 'Checking...';
  try {
    await fetch('/admin/health/check', { method: 'POST', headers: { 'Authorization': 'Bearer ' + apiKey } });
    await fetchData();
  } finally {
    $('checkHealthBtn').textContent = 'Check Health';
  }
};
$('toggleChart').onclick = () => { chartMode = chartMode === '24h' ? '14d' : '24h'; render(); };
$('toggleAuto').onclick = () => { autoRefresh = !autoRefresh; $('toggleAuto').textContent = 'Auto: ' + (autoRefresh ? 'ON' : 'OFF'); };

fetchData();
setInterval(() => { if (autoRefresh && apiKey) fetchData(); }, 3000);
</script>
</body>
</html>
`
