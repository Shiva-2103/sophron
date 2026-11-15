// Sophron dashboard. Plain JavaScript, no build step: it reads /api/v1 and
// renders. Every number comes from the API, so the dashboard, Slack and
// `make cost` never disagree.
(() => {
  "use strict";
  const app = document.getElementById("app");
  const RANGES = { "1h": "1h", "24h": "24h", "7d": "168h" };
  const state = { me: null, range: "24h", tab: {}, timer: null };

  // ---------- helpers ----------
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const money = (v, digits) => {
    if (v == null || isNaN(v)) return "—";
    if (digits == null) digits = Math.abs(v) >= 100 ? 0 : Math.abs(v) >= 1 ? 2 : Math.abs(v) >= 0.01 ? 3 : 4;
    return "$" + Number(v).toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
  };
  const pct = (v) => (v == null ? "—" : Math.round(v * 100) + "%");
  const num = (v, d = 1) => (v == null ? "—" : Number(v).toLocaleString("en-US", { maximumFractionDigits: d }));
  const ago = (t) => {
    if (!t) return "";
    const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
    if (s < 60) return Math.round(s) + "s ago";
    if (s < 3600) return Math.round(s / 60) + " min ago";
    if (s < 86400) return Math.round(s / 3600) + " h ago";
    return Math.round(s / 86400) + " d ago";
  };
  const when = (t) => (t ? new Date(t).toLocaleString(undefined, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" }) : "");
  const dur = (secs) => {
    secs = Math.round(secs || 0);
    if (secs < 60) return secs + "s";
    if (secs < 3600) return Math.floor(secs / 60) + "m " + (secs % 60) + "s";
    return Math.floor(secs / 3600) + "h " + Math.floor((secs % 3600) / 60) + "m";
  };
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
  };

  async function api(path, opts = {}) {
    const res = await fetch("/api/v1" + path, {
      credentials: "same-origin",
      ...opts,
      headers: { "X-Sophron": "1", "Content-Type": "application/json", ...(opts.headers || {}) },
    });
    if (res.status === 401 && path !== "/login") {
      state.me = { ...(state.me || {}), loggedIn: false };
      renderLogin();
      throw new Error("signin");
    }
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || res.statusText);
    return data;
  }
  const safe = (p, fallback) => p.catch((e) => { if (e.message === "signin") throw e; return fallback; });

  function toast(msg) {
    const t = document.createElement("div");
    t.className = "toast";
    t.textContent = msg;
    document.body.appendChild(t);
    setTimeout(() => t.remove(), 3500);
  }

  // ---------- icons and charts ----------
  const ICONS = {
    overview: '<rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/>',
    models: '<path d="M12 3 21 8v8l-9 5-9-5V8z"/><path d="M3 8l9 5 9-5M12 13v8"/>',
    problems: '<path d="M12 3 22 20H2z"/><path d="M12 10v4M12 17v.5"/>',
    approvals: '<path d="M4 12.5 9 17.5 20 6.5"/>',
    cost: '<path d="M12 3v18M17 7.5c0-1.9-2.2-3-5-3s-5 1.1-5 3 2.2 2.6 5 3.2 5 1.4 5 3.3-2.2 3-5 3-5-1.1-5-3"/>',
    activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
    setup: '<circle cx="12" cy="12" r="3.2"/><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.2 5.2l2.1 2.1M16.7 16.7l2.1 2.1M5.2 18.8l2.1-2.1M16.7 7.3l2.1-2.1"/>',
    spark: '<path d="M12 3l1.8 5.4L19 10l-5.2 1.6L12 17l-1.8-5.4L5 10l5.2-1.6z"/>',
    lock: '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
    moon: '<path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
    check: '<path d="M4 12.5 9 17.5 20 6.5"/>',
    out: '<path d="M15 4h4v16h-4M10 8l-4 4 4 4M6 12h10"/>',
  };
  const icon = (n, size = 18, color = "currentColor") =>
    `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="${color}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${ICONS[n]}</svg>`;
  const logo = (size = 30) =>
    `<div class="logo" style="width:${size}px;height:${size}px"><svg width="${size * 0.66}" height="${size * 0.66}" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 13.5 6 6.5l3.5 11 3-6.5 2 3h7"/></svg></div>`;

  function area(values, w, h, label) {
    const pts = values.map((v, i) => [i, v]).filter((p) => p[1] != null);
    if (pts.length < 2) return `<div class="empty" style="padding:24px"><b>Not enough data yet</b>The chart fills in as Sophron collects.</div>`;
    const ys = pts.map((p) => p[1]);
    let lo = Math.min(...ys), hi = Math.max(...ys);
    if (hi - lo < 1e-9) { hi += 1; lo = Math.max(0, lo - 1); }
    const n = values.length - 1 || 1;
    const X = (i) => (i / n) * w, Y = (v) => h - 8 - ((v - lo) / (hi - lo)) * (h - 20);
    const d = "M" + pts.map((p) => `${X(p[0]).toFixed(1)} ${Y(p[1]).toFixed(1)}`).join(" L");
    const last = pts[pts.length - 1];
    const grid = [0.25, 0.5, 0.75].map((g) => `<line x1="0" x2="${w}" y1="${h * g}" y2="${h * g}" stroke="var(--line)" stroke-dasharray="3 4"/>`).join("");
    return `<svg viewBox="0 0 ${w} ${h}" width="100%" height="${h}" preserveAspectRatio="none" role="img" aria-label="${esc(label)}">
      <defs><linearGradient id="g" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="var(--brand)" stop-opacity=".28"/><stop offset="1" stop-color="var(--brand)" stop-opacity="0"/></linearGradient></defs>
      ${grid}<path d="${d} L${X(last[0])} ${h} L${X(pts[0][0])} ${h} Z" fill="url(#g)"/>
      <path d="${d}" fill="none" stroke="var(--brand)" stroke-width="2.2" vector-effect="non-scaling-stroke" stroke-linejoin="round"/></svg>`;
  }
  function bars(values, w, h) {
    const max = Math.max(...values, 1e-9), n = values.length || 1, bw = (w / n) * 0.62;
    return `<svg width="${w}" height="${h}" role="img" aria-label="spend per bucket">${values.map((v, i) =>
      `<rect x="${((i * w) / n).toFixed(1)}" y="${(h - (v / max) * (h - 4)).toFixed(1)}" width="${bw.toFixed(1)}" height="${((v / max) * (h - 4)).toFixed(1)}" rx="2.5" fill="var(--brand)" opacity="${i === n - 1 ? 1 : 0.35}"/>`).join("")}</svg>`;
  }

  const SEV = { critical: 3, warning: 2, info: 1 };
  function statusChip(f) {
    if (!f) return `<span class="chip good"><span class="dot"></span>healthy</span>`;
    if (f.severity === "critical") return `<span class="chip crit">▲ ${esc(shortTitle(f))}</span>`;
    if (f.severity === "warning") return `<span class="chip warn"><span class="dot"></span>${esc(shortTitle(f))}</span>`;
    return `<span class="chip info"><span class="dot"></span>${esc(shortTitle(f))}</span>`;
  }
  function shortTitle(f) {
    const m = { "oom-killed": "OOM killed", "container-failing": "crashing", "pending-unschedulable": "can't schedule",
      "rollout-stuck": "rollout stuck", "slow-cold-start": "slow start", "queue-backlog": "queue backing up" };
    return m[f.detector] || f.detector;
  }
  const FIX_STATUS = { pending: "warn", approved: "info", applied: "info", verified: "good", denied: "muted", obsolete: "muted", blocked: "muted", failed: "crit" };
  const fixChip = (s) => `<span class="chip ${FIX_STATUS[s] || "muted"}">${s === "failed" ? "▲ " : '<span class="dot"></span>'}${esc(s)}</span>`;

  // ---------- shell ----------
  const PAGES = ["overview", "models", "problems", "approvals", "cost", "activity", "setup"];
  const TITLES = { overview: "Overview", models: "Models", problems: "Problems", approvals: "Approvals", cost: "Cost", activity: "Activity", setup: "Setup" };
  const page = () => { const p = location.hash.replace(/^#\//, "").split("?")[0]; return PAGES.includes(p) ? p : "overview"; };

  function shell(counts) {
    const p = page(), me = state.me || {};
    const nav = PAGES.map((n) => {
      let b = "";
      if (n === "approvals" && counts.pending) b = `<span class="badge">${counts.pending}</span>`;
      if (n === "problems" && counts.open) b = `<span class="badge crit">${counts.open}</span>`;
      return `<a class="nav ${n === p ? "on" : ""}" href="#/${n}">${icon(n)}${TITLES[n]}${b}</a>`;
    }).join("");
    const user = me.loggedIn ? me.user : { name: "guest", role: "viewer" };
    const ranged = p === "overview" || p === "cost" || p === "models";
    const seg = ranged ? `<div class="seg">${Object.keys(RANGES).map((r) => `<button data-range="${r}" class="${r === state.range ? "on" : ""}">${r}</button>`).join("")}</div>` : "";
    const dark = document.documentElement.dataset.theme === "dark";
    return `<div class="shell"><aside class="side">
      <div class="brand">${logo()}Sophron</div>
      <div class="cluster"><span class="dot" style="color:var(--good)"></span>${esc(me.cluster || "cluster")}</div>
      ${nav}
      <div class="sidecard"><div style="display:flex;gap:8px;align-items:center;font-weight:600">${icon("lock", 15, "var(--sage)")}${me.authEnabled ? "Approval mode" : "Read-only mode"}</div>
        <div class="ter" style="margin-top:4px">${me.authEnabled ? "Fixes run only after an admin approves, within the policy." : "No admin password set: nothing can be approved. Run make admin-password."}</div></div>
      <div class="me"><div class="avatar">${esc(user.name[0])}</div><div style="flex:1"><b>${esc(user.name)}</b><div class="ter" style="font-size:12px">${esc(user.role)}</div></div>
        ${me.loggedIn ? `<button class="iconbtn" style="width:32px;height:32px" id="logout" title="Sign out">${icon("out", 16)}</button>` : me.authEnabled ? `<a href="#/login">Sign in</a>` : ""}</div>
    </aside><div class="main"><div class="top"><h1>${TITLES[p]}</h1>${seg}
      <div style="margin-left:auto" class="ter" id="updated"></div>
      <button class="iconbtn" id="theme" title="Light or dark">${icon(dark ? "sun" : "moon", 17)}</button></div>
      <div class="content" id="content"><div class="boot">Loading…</div></div></div></div>`;
  }

  async function render() {
    if (location.hash === "#/login") return renderLogin();
    clearTimeout(state.timer);
    try {
      if (!state.me) state.me = await api("/me");
      if (state.me.authEnabled && !state.me.loggedIn) return renderLogin();
      const [open, pending] = await Promise.all([safe(api("/findings?status=open"), []), safe(api("/fixes?status=pending"), [])]);
      const scroll = window.scrollY;
      if (!document.getElementById("content") || app.dataset.page !== page() || app.dataset.counts !== `${open.length}/${pending.length}`) {
        app.innerHTML = shell({ open: open.length, pending: pending.length });
        app.dataset.page = page();
        app.dataset.counts = `${open.length}/${pending.length}`;
        wireShell();
      }
      const html = await VIEWS[page()]({ open, pending });
      const c = document.getElementById("content");
      if (c && !c.contains(document.activeElement)) { c.innerHTML = html; wireContent(); window.scrollTo(0, scroll); }
      const u = document.getElementById("updated");
      if (u) u.textContent = "Updated " + new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
    } catch (e) {
      if (e.message === "signin") return;
      const c = document.getElementById("content") || app;
      c.innerHTML = `<div class="card empty"><b>Can't reach the Sophron API</b>${esc(e.message)}</div>`;
    }
    state.timer = setTimeout(render, 15000);
  }

  function wireShell() {
    document.querySelectorAll("[data-range]").forEach((b) => b.addEventListener("click", () => {
      state.range = b.dataset.range;
      document.querySelectorAll("[data-range]").forEach((x) => x.classList.toggle("on", x === b));
      render();
    }));
    const t = document.getElementById("theme");
    if (t) t.addEventListener("click", () => {
      const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      document.documentElement.dataset.theme = next;
      store.set("sophron-theme", next);
      app.dataset.page = "";
      render();
    });
    const lo = document.getElementById("logout");
    if (lo) lo.addEventListener("click", async () => { await api("/logout", { method: "POST" }); state.me = null; location.hash = "#/overview"; render(); });
  }

  function wireContent() {
    document.querySelectorAll("[data-tab]").forEach((b) => b.addEventListener("click", () => {
      const [group, val] = b.dataset.tab.split(":");
      state.tab[group] = val;
      render();
    }));
    document.querySelectorAll("[data-decide]").forEach((b) => b.addEventListener("click", async () => {
      const [id, decision] = b.dataset.decide.split(":");
      const reason = (document.getElementById("reason-" + id) || {}).value || "";
      b.disabled = true;
      try {
        const r = await api(`/fixes/${id}/${decision}`, { method: "POST", body: JSON.stringify({ reason }) });
        toast(decision === "approve" ? `Fix #${id} ${r.status}: ${r.result || ""}` : `Fix #${id} denied`);
      } catch (e) { toast(e.message); }
      document.activeElement && document.activeElement.blur();
      app.dataset.counts = "";
      render();
    }));
    document.querySelectorAll("tr[data-href]").forEach((tr) => tr.addEventListener("click", () => { location.hash = tr.dataset.href; }));
  }

  // ---------- views ----------
  const worst = (fs) => fs.slice().sort((a, b) => (SEV[b.severity] || 0) - (SEV[a.severity] || 0))[0];
  const forModel = (open, m) => worst(open.filter((f) => f.namespace === m.namespace && (f.workloadName === m.workload || f.pod === m.workload)));
  const perToken = (m) => m.costPer1MOutputTokensBusy ?? m.costPer1MOutputTokens;

  function modelRows(models, open, full) {
    if (!models.length) return `<div class="empty"><b>No model servers yet</b>Label a pod sophron.dev/model-server or expose vLLM / llama.cpp metrics to Prometheus.</div>`;
    return `<table class="table"><thead><tr><th>Model</th><th>Status</th><th>Throughput</th><th>$ / 1M tokens</th>${full ? "<th>Per month</th>" : ""}<th>Capacity used</th></tr></thead><tbody>
      ${models.map((m) => {
        const f = forModel(open, m), cap = m.capacityUsed;
        const status = f ? statusChip(f) : m.pods === 0 ? `<span class="chip muted">gone</span>` : m.readyPods === 0 ? `<span class="chip warn"><span class="dot"></span>starting</span>` : statusChip(null);
        return `<tr class="click" data-href="#/problems"><td><b>${esc(m.namespace)}/${esc(m.workload)}</b><div class="sub2">${esc(m.server || "")} · ${m.readyPods}/${m.pods} ready</div></td>
          <td>${status}</td><td class="num">${num(m.outputTokensPerSecond)} <span class="ter">tok/s</span></td>
          <td class="num">${money(perToken(m))}${m.costPer1MOutputTokensBusy != null ? `<div class="sub2">busy ${pct(m.busyShare)}</div>` : ""}</td>
          ${full ? `<td class="num">${money(m.monthForecast)}${m.estimate ? '<div class="sub2">estimate</div>' : ""}</td>` : ""}
          <td><div style="display:flex;align-items:center;gap:10px"><div class="bar"><i class="${cap > 0.9 ? "warn" : ""}" style="width:${Math.round((cap || 0) * 100)}%"></i></div><span class="num ter" style="width:34px">${pct(cap)}</span></div></td></tr>`;
      }).join("")}</tbody></table>`;
  }

  function fixCard(r, canApprove) {
    const a = r.action || {};
    return `<div class="alert approve"><div class="grow">
      <div style="display:flex;gap:10px;align-items:center">${fixChip(r.status)}<span class="ter">Fix #${r.id} · ${esc(r.mode)} · requested ${ago(r.requestedAt)}</span></div>
      <div style="font-weight:600;font-size:15px;margin-top:8px">${esc(a.summary)}</div>
      <div class="mut" style="margin-top:4px">Because: ${esc(r.title)}</div>
      <div class="kv"><span>Target</span><span class="mono">${esc(a.namespace)}/${esc((a.workloadKind || "").toLowerCase())}/${esc(a.workloadName)}${a.container ? " · " + esc(a.container) : ""}</span>
        <span>Change</span><span class="mono">${esc(a.kind)}${a.value ? " → " + esc(a.value) : ""}</span></div>
      ${canApprove ? `<input class="reason" id="reason-${r.id}" placeholder="Reason (optional, goes in the audit log)">` : `<div class="note" style="margin-top:10px">Sign in as admin to approve or deny.</div>`}
    </div><div style="display:flex;flex-direction:column;gap:8px">
      <button class="btn pri" data-decide="${r.id}:approve" ${canApprove ? "" : "disabled"}>Approve fix</button>
      <button class="btn sec" data-decide="${r.id}:deny" ${canApprove ? "" : "disabled"}>Deny</button></div></div>`;
  }

  const VIEWS = {
    async overview({ open, pending }) {
      const w = RANGES[state.range];
      const [sum, models, trend, savings] = await Promise.all([
        safe(api("/cost/summary?window=" + w), null), safe(api("/cost/models?window=" + w), []),
        safe(api("/cost/trend?window=" + (state.range === "1h" ? "24h" : w)), []), safe(api("/savings"), []),
      ]);
      const live = models.filter((m) => m.pods > 0);
      const crit = open.filter((f) => f.severity === "critical");
      const lines = [];
      lines.push(open.length ? `${open.length} open problem${open.length > 1 ? "s" : ""}${crit.length ? ` (${crit.length} critical)` : ""}` : "no open problems");
      if (pending.length) lines.push(`${pending.length} fix${pending.length > 1 ? "es" : ""} waiting for approval`);
      if (sum) lines.push(`spend ${money(sum.spendPerHour)}/hour, ${pct(sum.idleShare)} of it idle`);
      const busy = live.filter((m) => (m.capacityUsed || 0) > 0.9);
      if (busy.length) lines.push(`${busy.map((m) => m.workload).join(", ")} close to capacity`);
      const needs = [
        ...pending.slice(0, 2).map((r) => fixCard(r, state.me && state.me.canApprove)),
        ...crit.filter((f) => !pending.some((r) => r.findingId === f.id)).slice(0, 2).map((f) =>
          `<div class="alert"><span class="chip crit">▲ critical</span><div class="grow"><b>${esc(f.title)}</b><div class="mut" style="margin-top:3px">${esc(f.fix && f.fix.summary)}</div></div><a class="btn sec" href="#/problems">Details</a></div>`),
      ].join("");
      const spendBars = trend.map((p) => p.modelCost);
      return `<div class="summary">${icon("spark", 17, "var(--brand)")}<span><b>Last ${state.range}:</b> ${esc(lines.join(" · "))}.</span></div>
        ${needs}
        <div class="row">
          <div class="card pad kpi" style="flex:1.35;display:flex;justify-content:space-between;gap:12px"><div><div class="label">Month forecast</div>
            <div class="value" style="font-size:34px">${money(sum && sum.monthForecast, 0)}</div><div class="sub">${money(sum && sum.spendPerHour)}/hour · ${money(sum && sum.idleCostPerHour * 730, 0)} of it idle</div></div>
            <div style="align-self:flex-end">${spendBars.length ? bars(spendBars.slice(-24), 150, 56) : ""}</div></div>
          <div class="card pad kpi grow"><div class="label">Cost per 1M tokens</div><div class="value">${money(sum && sum.costPer1MOutputTokens)}</div><div class="sub">output tokens, all models, ${state.range}</div></div>
          <div class="card pad kpi grow"><div class="label">Models serving</div><div class="value">${sum ? sum.modelsServing : "—"} of ${sum ? sum.models : "—"}</div><div class="sub">${open.length} open problems</div></div>
          <div class="card pad kpi grow"><div class="label">Idle</div><div class="value">${pct(sum && sum.idleShare)}</div><div class="sub">of node spend reserved by no pod</div></div>
        </div>
        <div class="row">
          <div class="card grow" style="flex:2.1;overflow:hidden"><div style="display:flex;justify-content:space-between;padding:16px 20px"><div class="h2">Models</div><a href="#/models">View all →</a></div>${modelRows(live.slice(0, 6), open, false)}</div>
          <div style="flex:1;display:flex;flex-direction:column;gap:14px;min-width:0">
            <div class="card pad"><div style="display:flex;justify-content:space-between"><div class="h2">Cost per 1M tokens</div><span class="num" style="font-weight:600">${money(sum && sum.costPer1MOutputTokens)}</span></div>
              <div class="sub2" style="margin-bottom:10px">All models, by hour</div>${area(trend.map((p) => p.costPer1MOutputTokens), 340, 120, "cost per 1M tokens")}</div>
            <div class="card pad"><div class="h2" style="margin-bottom:6px">Top savings</div>
              ${savings.length ? `<div class="list">${savings.slice(0, 3).map((s) => `<div style="display:flex;justify-content:space-between;gap:10px"><span>${esc(s.title)}</span><b class="num" style="color:var(--good);white-space:nowrap">${s.savingPerMonth > 0 ? money(s.savingPerMonth, 0) + "/mo" : "reliability"}</b></div>`).join("")}</div>`
                : `<div class="note">No savings yet. Suggestions need a few hours of history.</div>`}</div>
          </div></div>`;
    },

    async models({ open }) {
      const models = await api("/cost/models?window=" + RANGES[state.range]);
      const notes = models.filter((m) => (m.notes || []).length || (m.apiComparison || []).length);
      return `<div class="card" style="overflow:hidden">${modelRows(models, open, true)}</div>
        ${notes.map((m) => `<div class="card pad"><div class="h2">${esc(m.namespace)}/${esc(m.workload)}</div>
          <div class="kv"><span>Cost now</span><span class="num">${money(m.costPerHour)}/hour · ${money(m.monthForecast)}/month (${esc((m.priceSources || []).join(", "))})</span>
          <span>Per 1M output tokens</span><span class="num">${money(m.costPer1MOutputTokens)} average · ${money(m.costPer1MOutputTokensBusy)} while busy</span>
          <span>Peak seen</span><span class="num">${num(m.peakOutputTokensPerSecond)} tokens/s</span></div>
          ${(m.apiComparison || []).map((c) => `<div class="note" style="margin-top:10px">vs <b>${esc(c.api)}</b>: the API would cost ${money(c.apiCostPerMonth, 2)}/month for the same tokens; self-hosting costs ${money(c.selfCostPerMonth, 2)}. Break-even at ${num(c.breakEvenTokensPerMonth / 1e6, 1)}M tokens/month.</div>`).join("")}
          ${(m.notes || []).map((n) => `<div class="note" style="margin-top:6px">Note: ${esc(n)}</div>`).join("")}</div>`).join("")}`;
    },

    async problems({ open }) {
      const tab = state.tab.problems || "open";
      const list = tab === "open" ? open : await api("/findings?status=resolved&limit=50");
      const tabs = `<div class="tabs"><button data-tab="problems:open" class="${tab === "open" ? "on" : ""}">Open (${open.length})</button><button data-tab="problems:resolved" class="${tab === "resolved" ? "on" : ""}">Resolved</button></div>`;
      if (!list.length) return tabs + `<div class="card empty"><b>${tab === "open" ? "Nothing is wrong right now" : "Nothing resolved yet"}</b>${tab === "open" ? "Six detectors check every model every 30 seconds." : ""}</div>`;
      return tabs + list.map((f) => {
        const r = f.resolution;
        return `<div class="card pad"><div style="display:flex;gap:10px;align-items:center">${tab === "open" ? statusChip(f) : '<span class="chip good"><span class="dot"></span>resolved</span>'}<span class="ter">${esc(f.detector)} · first seen ${ago(f.firstSeen)}${f.resolvedAt ? " · resolved " + ago(f.resolvedAt) : ""}</span></div>
          <div style="font-weight:600;font-size:15.5px;margin-top:10px">${esc(f.title)}</div><div class="mut" style="margin-top:6px;line-height:1.55">${esc(f.summary)}</div>
          <ul class="ev">${(f.evidence || []).map((e) => `<li>${esc(e)}</li>`).join("")}</ul>
          ${tab === "open" ? `<div style="margin-top:12px"><b>Suggested fix:</b> ${esc(f.fix.summary)}</div><ol class="ev">${(f.fix.steps || []).map((s) => `<li>${esc(s)}</li>`).join("")}</ol>${f.fix.snippet ? `<pre class="snip">${esc(f.fix.snippet)}</pre>` : ""}
            ${f.fix.action ? `<div class="note" style="margin-top:10px">Sophron can do this for you: <a href="#/approvals">${esc(f.fix.action.summary)} →</a></div>` : ""}` : ""}
          ${r ? `<div style="margin-top:12px"><b>How it was resolved:</b> ${esc(r.summary)}</div><ul class="ev">${(r.changes || []).map((c) => `<li>${esc(c)}</li>`).join("")}</ul>
            <div class="kv"><span>Changed with</span><span>${esc(r.changedBy || "no change recorded")}</span><span>Approved by</span><span>${esc(r.approvedBy || "not needed")}</span>
            ${r.downSeconds ? `<span>Not serving for</span><span>${dur(r.downSeconds)}</span><span>Cost while down</span><span class="num">${money(r.downCost)}</span>` : ""}</div>` : ""}</div>`;
      }).join("");
    },

    async approvals({ pending }) {
      const all = await api("/fixes?status=all");
      const done = all.filter((r) => r.status !== "pending");
      const can = state.me && state.me.canApprove;
      return `${pending.length ? pending.map((r) => fixCard(r, can)).join("") : `<div class="card empty"><b>Nothing waiting for you</b>When a problem has a fix Sophron can apply, it shows up here first. Nothing changes until an admin approves.</div>`}
        <div class="card" style="overflow:hidden"><div class="h2" style="padding:16px 20px">History</div>
        ${done.length ? `<table class="table"><thead><tr><th>Fix</th><th>Status</th><th>Decided by</th><th>Result</th><th>When</th></tr></thead><tbody>${done.map((r) =>
          `<tr><td><b>#${r.id}</b> ${esc(r.action.summary)}<div class="sub2">${esc(r.title)}</div></td><td>${fixChip(r.status)}</td><td>${esc(r.decidedBy || (r.status === "blocked" ? "policy" : "—"))}${r.reason ? `<div class="sub2">${esc(r.reason)}</div>` : ""}</td><td class="mut">${esc(r.result || "")}</td><td class="ter">${when(r.verifiedAt || r.appliedAt || r.decidedAt || r.requestedAt)}</td></tr>`).join("")}</tbody></table>`
          : `<div class="empty">No decisions yet.</div>`}</div>`;
    },

    async cost() {
      const w = RANGES[state.range];
      const [sum, nodes, savings, trend] = await Promise.all([api("/cost/summary?window=" + w), api("/cost/nodes"), safe(api("/savings"), []), safe(api("/cost/trend?window=" + (state.range === "1h" ? "24h" : w)), [])]);
      return `<div class="row">
          <div class="card pad kpi grow"><div class="label">Spend</div><div class="value">${money(sum.spendPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${money(sum.monthForecast, 0)} per month, all nodes</div></div>
          <div class="card pad kpi grow"><div class="label">Models</div><div class="value">${money(sum.modelCostPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${money(sum.costPer1MOutputTokens)} per 1M output tokens</div></div>
          <div class="card pad kpi grow"><div class="label">Idle</div><div class="value">${money(sum.idleCostPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${pct(sum.idleShare)} of spend; ${sum.estimatedNodes ? sum.estimatedNodes + " nodes use estimated prices" : "all prices real"}</div></div>
        </div>
        <div class="card pad"><div class="h2">Model spend</div><div class="sub2" style="margin-bottom:10px">USD per bucket, ${state.range}</div>${area(trend.map((p) => p.modelCost), 900, 140, "model spend")}</div>
        <div class="card" style="overflow:hidden"><div class="h2" style="padding:16px 20px">Nodes</div><table class="table"><thead><tr><th>Node</th><th>Price</th><th>Split: models · other · idle</th><th>Idle</th></tr></thead><tbody>
          ${nodes.map((n) => `<tr><td><b>${esc(n.node)}</b><div class="sub2">${esc(n.instanceType || "")} ${esc(n.capacityType || "")}</div></td>
            <td class="num">${money(n.hourly)}/h<div class="sub2">${esc(n.source)}${n.estimate ? " (estimate)" : ""}${n.note ? " · " + esc(n.note) : ""}</div></td>
            <td style="min-width:260px"><div class="stack"><i style="width:${(n.modelCostPerHour / n.hourly) * 100}%;background:var(--brand)"></i><i style="width:${(n.otherCostPerHour / n.hourly) * 100}%;background:var(--sage)"></i></div>
              <div class="sub2">${n.modelPods} model pods ${money(n.modelCostPerHour)} · ${n.otherPods} other ${money(n.otherCostPerHour)} · idle ${money(n.idleCostPerHour)}</div></td>
            <td class="num">${pct(n.idleShare)}</td></tr>`).join("")}</tbody></table></div>
        <div class="card pad"><div class="h2">Savings</div><div class="sub2">From the last 7 days, biggest first</div>
          ${savings.length ? `<div class="list" style="margin-top:8px">${savings.map((s) => `<div><div style="display:flex;justify-content:space-between;gap:12px"><b>${esc(s.title)}</b><b class="num" style="color:var(--good);white-space:nowrap">${s.savingPerMonth > 0 ? money(s.savingPerMonth, 2) + "/mo" : "reliability"}</b></div>
            <div class="mut" style="margin-top:4px">${esc(s.detail)}</div><ul class="ev">${(s.evidence || []).map((e) => `<li>${esc(e)}</li>`).join("")}</ul>
            <div class="note">Confidence: ${esc(s.confidence)}</div>${s.snippet ? `<pre class="snip">${esc(s.snippet)}</pre>` : ""}</div>`).join("")}</div>`
            : `<div class="empty"><b>No savings found yet</b>Sophron needs a few hours of history (a day or more is best) before it suggests changes.</div>`}</div>`;
    },

    async activity() {
      const log = await api("/audit");
      if (!log.length) return `<div class="card empty"><b>No activity yet</b>Sign-ins, fix requests, approvals and applied changes appear here.</div>`;
      return `<div class="card" style="overflow:hidden"><table class="table"><thead><tr><th>When</th><th>Who</th><th>What</th><th>Target</th><th>Detail</th></tr></thead><tbody>
        ${log.map((e) => `<tr><td class="ter" style="white-space:nowrap">${when(e.at)}</td><td><b>${esc(e.actor)}</b></td><td><span class="chip ${e.event.includes("fail") ? "crit" : e.event.includes("verified") || e.event === "login" ? "good" : e.event.includes("approved") || e.event.includes("applied") ? "info" : "muted"}">${esc(e.event)}</span></td>
          <td class="mono">${esc(e.target)}</td><td class="mut">${esc(e.detail)}</td></tr>`).join("")}</tbody></table>
        <div class="note" style="padding:12px 20px">Each entry is chained to the previous one with SHA-256: editing or deleting any line breaks the chain, and Setup shows it.</div></div>`;
    },

    async setup() {
      const checks = await api("/setup");
      const chip = { ok: "good", warn: "warn", fail: "crit", skip: "muted" };
      const bad = checks.filter((c) => c.status === "fail").length, warn = checks.filter((c) => c.status === "warn").length;
      return `<div class="summary">${icon("check", 17, "var(--good)")}<span><b>${bad ? bad + " problem" + (bad > 1 ? "s" : "") : "All good"}${warn ? `, ${warn} to improve` : ""}.</b> Sophron checks its own setup: data sources, prices, alerts, sign-in and permissions.</span></div>
        <div class="card">${checks.map((c) => `<div class="check"><span class="chip ${chip[c.status]}" style="min-width:58px;justify-content:center">${c.status === "fail" ? "▲ " : ""}${esc(c.status)}</span>
          <div class="grow"><b>${esc(c.name)}</b><div class="mut" style="margin-top:3px">${esc(c.detail)}</div>${c.fix ? `<div class="note" style="margin-top:4px">Fix: <span class="mono">${esc(c.fix)}</span></div>` : ""}</div></div>`).join("")}</div>`;
    },
  };

  // ---------- login ----------
  function renderLogin() {
    clearTimeout(state.timer);
    app.dataset.page = "login";
    const lines = Array.from({ length: 9 }, (_, i) => { const y = 60 + i * 95, a = 26 + i * 3;
      return `<path d="M-20 ${y} C 190 ${y - a}, 340 ${y + a}, 530 ${y - a / 2} S 800 ${y + a / 3}, 800 ${y}" fill="none" stroke="currentColor" stroke-opacity=".16" stroke-width="1.2"/>`; }).join("");
    const me = state.me || {};
    app.innerHTML = `<div class="login"><div class="art"><svg class="lines" viewBox="0 0 760 900" preserveAspectRatio="none">${lines}</svg>
      <div class="brand" style="position:relative;color:inherit">${logo(34)}Sophron</div>
      <h2>Every problem has a price.<br>Every fix has a receipt.</h2>
      <p>Cost, health and safe fixes for the LLMs you run on Kubernetes. Runs in your cluster; nothing leaves it.</p>
      <div class="receipt"><div style="display:flex;gap:10px;align-items:center"><span class="chip good">${icon("check", 14)}verified</span><b>Fix applied</b></div>
        <div class="kv"><span>Change</span><span>memory 512Mi → 768Mi</span><span>Approved by</span><span>an admin</span><span>Outage cost</span><span>$0.07 over 12 min</span></div></div></div>
      <div class="form"><div>
        <div class="display" style="font-size:28px;font-weight:600">Welcome back</div>
        <div class="mut" style="margin-top:6px">Sign in to <b>${esc(me.cluster || "Sophron")}</b></div>
        ${me.authEnabled === false ? `<div class="card pad" style="margin-top:24px"><b>Sign-in isn't set up</b><div class="mut" style="margin-top:6px">No admin password exists yet, so the dashboard is read-only. Run <span class="mono">make admin-password</span> on the lab machine.</div><a class="btn sec" style="margin-top:14px" href="#/overview">Continue read-only</a></div>` : `
        <form id="loginform"><div class="field"><label for="name">Name</label><input id="name" autocomplete="username" value="admin"></div>
        <div class="field"><label for="pw">Password</label><input id="pw" type="password" autocomplete="current-password" autofocus></div>
        <button class="btn pri" style="width:100%;margin-top:22px;padding:13px" type="submit">Sign in</button><div class="err" id="err"></div></form>
        <div class="note" style="margin-top:22px;text-align:center">Admins approve fixes. Viewers can look but not change anything.</div>`}
      </div></div></div>`;
    const form = document.getElementById("loginform");
    if (form) form.addEventListener("submit", async (e) => {
      e.preventDefault();
      try {
        await api("/login", { method: "POST", body: JSON.stringify({ name: document.getElementById("name").value, password: document.getElementById("pw").value }) });
        state.me = null;
        app.dataset.page = "";
        location.hash = "#/overview";
        render();
      } catch (err) { document.getElementById("err").textContent = err.message === "signin" ? "Wrong name or password" : err.message; }
    });
  }

  // ---------- start ----------
  const saved = store.get("sophron-theme");
  document.documentElement.dataset.theme = saved || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  window.addEventListener("hashchange", () => { app.dataset.page = ""; render(); });
  render();
})();
