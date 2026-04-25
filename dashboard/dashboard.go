package dashboard

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"
)

// StatsProvider is implemented by the router to provide stats
type StatsProvider interface {
	GetStats() interface{}
}

// Handler serves the dashboard HTML and stats API
type Handler struct {
	stats     StatsProvider
	authToken string
}

const cookieName = "gw_token"

func NewHandler(stats StatsProvider, authToken string) *Handler {
	return &Handler{stats: stats, authToken: authToken}
}

// checkAuth verifies the token from cookie or Authorization header
func (h *Handler) checkAuth(r *http.Request) bool {
	// Check cookie
	if c, err := r.Cookie(cookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(h.authToken)) == 1 {
		return true
	}
	// Check Authorization header (for API calls)
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return subtle.ConstantTimeCompare([]byte(auth[7:]), []byte(h.authToken)) == 1
	}
	return false
}

// ServeLogin handles GET/POST /dashboard/login
func (h *Handler) ServeLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		r.ParseForm()
		token := r.FormValue("token")
		if token == h.authToken {
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(30 * 24 * time.Hour / time.Second), // 30 days
			})
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(loginHTML(true)))
		return
	}

	// GET — if already authed, redirect to dashboard
	if h.checkAuth(r) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginHTML(false)))
}

// ServeLogout clears the cookie
func (h *Handler) ServeLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/dashboard/login", http.StatusSeeOther)
}

// ServeStats handles GET /api/stats
func (h *Handler) ServeStats(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.stats.GetStats())
}

// ServeDashboard handles GET /dashboard
func (h *Handler) ServeDashboard(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(r) {
		http.Redirect(w, r, "/dashboard/login", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func loginHTML(showError bool) string {
	errMsg := ""
	if showError {
		errMsg = `<div style="color:#f87171;margin-bottom:16px;font-size:.85rem">Invalid token</div>`
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>🚀 AI Gateway — Login</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0f0f0f;color:#e0e0e0;display:flex;align-items:center;justify-content:center;min-height:100vh}
.login{background:#1a1a1a;border:1px solid #2a2a2a;border-radius:12px;padding:32px;width:100%;max-width:360px}
.login h1{font-size:1.2rem;color:#fff;margin-bottom:4px}
.login .sub{color:#888;font-size:.8rem;margin-bottom:24px}
input[type=password]{width:100%;padding:10px 12px;background:#0f0f0f;border:1px solid #333;border-radius:6px;color:#fff;font-size:.9rem;outline:none;margin-bottom:16px}
input[type=password]:focus{border-color:#4ade80}
button{width:100%;padding:10px;background:#4ade80;color:#000;border:none;border-radius:6px;font-size:.9rem;font-weight:600;cursor:pointer}
button:hover{background:#22c55e}
</style>
</head>
<body>
<div class="login">
<h1>🚀 AI Gateway</h1>
<div class="sub">Enter your auth token to access the dashboard</div>
` + errMsg + `
<form method="POST" action="/dashboard/login">
<input type="password" name="token" placeholder="Auth token" autofocus required>
<button type="submit">Log in</button>
</form>
</div>
</body>
</html>`
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>🚀 AI Gateway Dashboard</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0f0f0f;color:#e0e0e0;padding:20px}
h1{font-size:1.5rem;color:#fff;margin-bottom:4px}
.subtitle{color:#888;font-size:.85rem;margin-bottom:20px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin-bottom:24px}
.card{background:#1a1a1a;border:1px solid #2a2a2a;border-radius:8px;padding:16px}
.card h3{font-size:.75rem;color:#888;text-transform:uppercase;letter-spacing:.5px;margin-bottom:8px}
.card .value{font-size:1.8rem;font-weight:700;color:#fff}
.card .value.ok{color:#4ade80}
.card .value.warn{color:#facc15}
.card .value.err{color:#f87171}
table{width:100%;border-collapse:collapse;margin-bottom:24px}
th{text-align:left;font-size:.7rem;color:#888;text-transform:uppercase;letter-spacing:.5px;padding:8px 12px;border-bottom:1px solid #2a2a2a}
td{padding:8px 12px;border-bottom:1px solid #1a1a1a;font-size:.85rem;vertical-align:top}
tr.account-row{cursor:pointer}
tr.account-row:hover{background:#1a1a1a}
tr.model-row td{padding:4px 12px 4px 32px;background:#141414;border-bottom:1px solid #1a1a1a}
tr.model-row.hidden{display:none}
.model-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:8px;padding:8px 0}
.model-card{background:#1e1e1e;border:1px solid #2a2a2a;border-radius:6px;padding:10px 12px}
.model-card .name{font-size:.75rem;color:#bbb;margin-bottom:6px;word-break:break-all}
.model-card .metrics{display:flex;gap:8px;flex-wrap:wrap}
.model-metric{flex:1;min-width:60px}
.model-metric .label{font-size:.65rem;color:#666;text-transform:uppercase;margin-bottom:2px}
.model-metric .nums{font-size:.78rem;color:#ddd}
.status{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px}
.status.ok{background:#4ade80}
.status.cooldown{background:#facc15}
.status.error{background:#f87171}
.badge{display:inline-block;padding:2px 8px;border-radius:4px;font-size:.75rem;font-weight:500}
.badge.ok{background:#052e16;color:#4ade80}
.badge.fallback{background:#422006;color:#facc15}
.badge.error{background:#2c0b0e;color:#f87171}
.badge.per_model{background:#0c1a3a;color:#60a5fa;font-size:.65rem}
.badge.per_account{background:#1a1a1a;color:#888;font-size:.65rem}
.badge.both{background:#1a0c2e;color:#a78bfa;font-size:.65rem}
.section{margin-bottom:24px}
.section h2{font-size:1rem;color:#ccc;margin-bottom:12px;padding-bottom:8px;border-bottom:1px solid #2a2a2a}
.bar{height:4px;background:#2a2a2a;border-radius:2px;margin-top:4px;overflow:hidden}
.bar-fill{height:100%;border-radius:2px;transition:width .3s}
.bar-fill.low{background:#4ade80}
.bar-fill.mid{background:#facc15}
.bar-fill.high{background:#f87171}
.topbar{display:flex;justify-content:space-between;align-items:center;margin-bottom:8px}
.refresh{color:#888;font-size:.75rem}
.logout{color:#888;font-size:.75rem;text-decoration:none;border:1px solid #333;padding:4px 10px;border-radius:4px}
.logout:hover{color:#f87171;border-color:#f87171}
.mono{font-family:'SF Mono',Monaco,Consolas,monospace;font-size:.8rem}
.expand-icon{color:#555;font-size:.7rem;margin-left:6px;transition:transform .2s;display:inline-block}
.expand-icon.open{transform:rotate(90deg)}
.err-link{cursor:pointer;text-decoration:underline;text-decoration-style:dotted;text-underline-offset:2px}
.err-link:hover{color:#fca5a5}
tr.error-row td{padding:6px 12px 6px 24px;background:#1a1010;border-bottom:1px solid #2a1a1a}
tr.error-row.hidden{display:none}
.error-list{font-size:.8rem;color:#ccc}
.error-list .err-entry{padding:3px 0;border-bottom:1px solid #221a1a;display:flex;gap:10px}
.error-list .err-time{color:#666;white-space:nowrap;font-family:'SF Mono',Monaco,Consolas,monospace;font-size:.75rem}
.error-list .err-model{color:#a78bfa;min-width:80px}
.error-list .err-msg{color:#f87171;word-break:break-word}
</style>
</head>
<body>
<div class="topbar">
<div><h1>🚀 AI Gateway Dashboard</h1><div class="subtitle" id="uptime"></div></div>
<div><span class="refresh" style="margin-right:12px">Auto-refresh: 3s</span><a href="/dashboard/logout" class="logout">Logout</a></div>
</div>
<div class="grid" id="summary"></div>
<div class="section"><h2>Provider Accounts</h2><table id="accounts"><thead><tr><th>Account</th><th>Mode</th><th>Status</th><th>RPM</th><th>RPD</th><th>TPD</th><th>Requests</th><th>Errors</th></tr></thead><tbody></tbody></table></div>
<div class="section"><h2>Recent Requests</h2><table id="requests"><thead><tr><th>Time</th><th>Requested</th><th>Actual</th><th>Provider</th><th>Duration</th><th>Tokens</th><th>Status</th></tr></thead><tbody></tbody></table></div>
<div class="section"><h2>Alerts</h2><div id="alerts"></div></div>
<script>
const expanded=new Set();
const errExpanded=new Set();

function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML}

function bar(used,limit){
  if(!limit)return '<span style="color:#555">—</span>';
  const pct=Math.round(used/limit*100);
  const cls=pct<50?'low':pct<80?'mid':'high';
  return used+'/'+(limit||'∞')+'<div class="bar"><div class="bar-fill '+cls+'" style="width:'+Math.min(pct,100)+'%"></div></div>';
}

function modelCards(modelStats,models){
  if(!modelStats)return '';
  return '<div class="model-grid">'+models.map(id=>{
    const s=modelStats[id];
    if(!s)return '';
    const shortName=id.split('/').pop();
    return '<div class="model-card">'+
      '<div class="name" title="'+id+'">'+shortName+'</div>'+
      '<div class="metrics">'+
        '<div class="model-metric"><div class="label">RPM</div><div class="nums">'+bar(s.rpm_used,s.rpm_limit)+'</div></div>'+
        (s.tpm_limit?'<div class="model-metric"><div class="label">TPM</div><div class="nums">'+bar(s.tpm_used,s.tpm_limit)+'</div></div>':'')+
        (s.rpd_limit?'<div class="model-metric"><div class="label">RPD</div><div class="nums">'+bar(s.rpd_used,s.rpd_limit)+'</div></div>':'')+
        (s.monthly_limit?'<div class="model-metric"><div class="label">Monthly</div><div class="nums">'+bar(s.monthly_used,s.monthly_limit)+'</div></div>':'')+
      '</div>'+
    '</div>';
  }).join('')+'</div>';
}

function toggle(key){
  if(expanded.has(key))expanded.delete(key);
  else expanded.add(key);
  refresh();
}
function toggleErr(key,ev){
  ev.stopPropagation();
  if(errExpanded.has(key))errExpanded.delete(key);
  else errExpanded.add(key);
  refresh();
}

function errorRows(errors,key){
  if(!errors||!errors.length)return '<div style="color:#666;padding:6px">No recent errors</div>';
  return '<div class="error-list">'+errors.slice().reverse().map(e=>{
    const t=new Date(e.time).toLocaleTimeString([],{hour12:false});
    return '<div class="err-entry"><span class="err-time">'+t+'</span>'+
      (e.model?'<span class="err-model">'+esc(e.model.split('/').pop())+'</span>':'')+
      (e.code?'<span class="badge error" style="font-size:.7rem;padding:1px 5px">'+e.code+'</span>':'')+
      '<span class="err-msg">'+esc(e.message)+'</span></div>';
  }).join('')+'</div>';
}

async function refresh(){
try{
const r=await fetch('/api/stats');
if(r.status===401){location.href='/dashboard/login';return}
const d=await r.json();
document.getElementById('uptime').textContent='Uptime: '+d.uptime;
document.getElementById('summary').innerHTML=
  '<div class="card"><h3>Total</h3><div class="value">'+d.total_requests+'</div></div>'+
  '<div class="card"><h3>Success</h3><div class="value ok">'+d.successful+'</div></div>'+
  '<div class="card"><h3>Failed</h3><div class="value'+(d.failed>0?' err':'')+'">'+d.failed+'</div></div>'+
  '<div class="card"><h3>Accounts</h3><div class="value">'+d.accounts.length+'</div></div>';

const ab=document.querySelector('#accounts tbody');
const rows=[];
d.accounts.forEach(a=>{
  const key=a.provider+'/'+a.account;
  const hasModels=(a.limit_mode==='per_model'||a.limit_mode==='both')&&a.usage.model_stats&&Object.keys(a.usage.model_stats).length>0;
  const isOpen=expanded.has(key);
  const rpmPct=a.limits.rpm?Math.round(a.usage.rpm_used/a.limits.rpm*100):0;
  const rpdPct=a.limits.rpd?Math.round(a.usage.rpd_used/a.limits.rpd*100):0;
  const tpdPct=a.limits.tpd?Math.round(a.usage.tpd_used/a.limits.tpd*100):0;

  rows.push('<tr class="account-row"'+(hasModels?' onclick="toggle(\''+key+'\')"':'')+'>'+
    '<td><span class="status '+a.status+'"></span>'+key+(hasModels?'<span class="expand-icon'+(isOpen?' open':'')+'">▶</span>':'')+'</td>'+
    '<td><span class="badge '+a.limit_mode+'">'+a.limit_mode+'</span></td>'+
    '<td><span class="badge '+a.status+'">'+a.status+'</span>'+(a.usage.cooldown_remaining_s>0?' ('+a.usage.cooldown_remaining_s+'s)':'')+'</td>'+
    '<td>'+bar(a.usage.rpm_used,a.limits.rpm)+'</td>'+
    '<td>'+bar(a.usage.rpd_used,a.limits.rpd)+'</td>'+
    '<td>'+bar(a.usage.tpd_used,a.limits.tpd)+'</td>'+
    '<td>'+a.usage.total_requests+'</td>'+
    '<td>'+(a.usage.total_errors>0?'<span class="err-link" onclick="toggleErr(\''+key+'\',event)">'+a.usage.total_errors+'</span>':a.usage.total_errors)+'</td>'+
  '</tr>');

  // Error details row (toggle independently from model cards)
  if(a.usage.total_errors>0){
    const errOpen=errExpanded.has(key);
    rows.push('<tr class="error-row'+(errOpen?'':' hidden')+'"><td colspan="8">'+
      errorRows(a.usage.recent_errors,key)+
    '</td></tr>');
  }

  if(hasModels){
    rows.push('<tr class="model-row'+(isOpen?'':' hidden')+'"><td colspan="8">'+
      modelCards(a.usage.model_stats,a.models)+
    '</td></tr>');
  }
});
ab.innerHTML=rows.join('');

const rb=document.querySelector('#requests tbody');
const reqs=(d.recent_requests||[]).slice(0,30);
rb.innerHTML=reqs.map(r=>{
  const t=new Date(r.time).toLocaleTimeString([], {hour12: false});
  const badge=r.status==='ok'?(r.fallback?'fallback':'ok'):'error';
  return '<tr><td class="mono">'+t+'</td><td>'+r.requested_model+'</td>'+
    '<td>'+r.actual_model+'</td><td>'+((r.provider||'')+'/'+( r.account||''))+'</td>'+
    '<td>'+r.duration_ms+'ms</td><td>'+r.tokens+'</td>'+
    '<td><span class="badge '+badge+'">'+(r.fallback?'↳ fallback':r.status)+'</span>'+(r.error?' '+r.error:'')+'</td></tr>';
}).join('');

const ad=document.getElementById('alerts');
const alerts=(d.alerts||[]).slice(-20).reverse();
ad.innerHTML=alerts.length?alerts.map(a=>{
  const t=new Date(a.time).toLocaleTimeString([], {hour12: false});
  const emoji=a.level==='error'?'🔴':'🟡';
  return '<div style="padding:6px 0;border-bottom:1px solid #1a1a1a;font-size:.85rem"><span class="mono">'+t+'</span> '+emoji+' '+a.message+'</div>'
}).join(''):'<div style="color:#888;padding:12px">No alerts</div>';
}catch(e){console.error(e)}
}
refresh();setInterval(refresh,3000);
</script>
</body>
</html>`
