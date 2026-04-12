package dashboard

import (
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
	if c, err := r.Cookie(cookieName); err == nil && c.Value == h.authToken {
		return true
	}
	// Check Authorization header (for API calls)
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:] == h.authToken
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
td{padding:8px 12px;border-bottom:1px solid #1a1a1a;font-size:.85rem}
tr:hover{background:#1a1a1a}
.status{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px}
.status.ok{background:#4ade80}
.status.cooldown{background:#facc15}
.status.error{background:#f87171}
.badge{display:inline-block;padding:2px 8px;border-radius:4px;font-size:.75rem;font-weight:500}
.badge.ok{background:#052e16;color:#4ade80}
.badge.upgraded{background:#422006;color:#facc15}
.badge.error{background:#2c0b0e;color:#f87171}
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
</style>
</head>
<body>
<div class="topbar">
<div><h1>🚀 AI Gateway Dashboard</h1><div class="subtitle" id="uptime"></div></div>
<div><span class="refresh" style="margin-right:12px">Auto-refresh: 3s</span><a href="/dashboard/logout" class="logout">Logout</a></div>
</div>
<div class="grid" id="summary"></div>
<div class="section"><h2>Provider Accounts</h2><table id="accounts"><thead><tr><th>Account</th><th>Models</th><th>Status</th><th>RPM</th><th>RPD</th><th>TPD</th><th>Requests</th><th>Errors</th></tr></thead><tbody></tbody></table></div>
<div class="section"><h2>Recent Requests</h2><table id="requests"><thead><tr><th>Time</th><th>Requested</th><th>Actual</th><th>Provider</th><th>Duration</th><th>Tokens</th><th>Status</th></tr></thead><tbody></tbody></table></div>
<div class="section"><h2>Alerts</h2><div id="alerts"></div></div>
<script>
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
ab.innerHTML=d.accounts.map(a=>{
const rpmPct=a.limits.rpm?Math.round(a.usage.rpm_used/a.limits.rpm*100):0;
const rpdPct=a.limits.rpd?Math.round(a.usage.rpd_used/a.limits.rpd*100):0;
const tpdPct=a.limits.tpd?Math.round(a.usage.tpd_used/a.limits.tpd*100):0;
const barClass=p=>p<50?'low':p<80?'mid':'high';
return '<tr><td><span class="status '+a.status+'"></span>'+a.provider+'/'+a.account+'</td>'+
'<td class="mono">'+a.models.join(', ')+'</td>'+
'<td><span class="badge '+a.status+'">'+a.status+'</span>'+(a.usage.cooldown_remaining_s>0?' ('+a.usage.cooldown_remaining_s+'s)':'')+'</td>'+
'<td>'+a.usage.rpm_used+'/'+(a.limits.rpm||'∞')+'<div class="bar"><div class="bar-fill '+barClass(rpmPct)+'" style="width:'+rpmPct+'%"></div></div></td>'+
'<td>'+a.usage.rpd_used+'/'+(a.limits.rpd||'∞')+'<div class="bar"><div class="bar-fill '+barClass(rpdPct)+'" style="width:'+rpdPct+'%"></div></div></td>'+
'<td>'+a.usage.tpd_used+'/'+(a.limits.tpd||'∞')+'<div class="bar"><div class="bar-fill '+barClass(tpdPct)+'" style="width:'+tpdPct+'%"></div></div></td>'+
'<td>'+a.usage.total_requests+'</td><td>'+a.usage.total_errors+'</td></tr>'
}).join('');
const rb=document.querySelector('#requests tbody');
const reqs=(d.recent_requests||[]).slice(0,30);
rb.innerHTML=reqs.map(r=>{
const t=new Date(r.time).toLocaleTimeString();
const badge=r.status==='ok'?(r.upgraded?'upgraded':'ok'):'error';
return '<tr><td class="mono">'+t+'</td><td>'+r.requested_model+'</td>'+
'<td>'+r.actual_model+'</td><td>'+((r.provider||'')+'/'+( r.account||''))+'</td>'+
'<td>'+r.duration_ms+'ms</td><td>'+r.tokens+'</td>'+
'<td><span class="badge '+badge+'">'+(r.upgraded?'↑ upgraded':r.status)+'</span>'+(r.error?' '+r.error:'')+'</td></tr>'
}).join('');
const ad=document.getElementById('alerts');
const alerts=(d.alerts||[]).slice(-20).reverse();
ad.innerHTML=alerts.length?alerts.map(a=>{
const t=new Date(a.time).toLocaleTimeString();
const emoji=a.level==='error'?'🔴':'🟡';
return '<div style="padding:6px 0;border-bottom:1px solid #1a1a1a;font-size:.85rem"><span class="mono">'+t+'</span> '+emoji+' '+a.message+'</div>'
}).join(''):'<div style="color:#888;padding:12px">No alerts</div>';
}catch(e){console.error(e)}
}
refresh();setInterval(refresh,3000);
</script>
</body>
</html>`
