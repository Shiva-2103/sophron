// Sophron dashboard. Plain JavaScript, no build step: it reads /api/v1 and
// renders. Every number comes from the API, so the dashboard, Slack and
// `make cost` never disagree.
(() => {
  "use strict";
  const app = document.getElementById("app");
  const RANGES = { "1h": "1h", "24h": "24h", "7d": "168h" };
  const RANGE_HOURS = { "1h": 1, "24h": 24, "7d": 168 };
  const POLL_MS = 15000;
  const STALE_SECS = 180; // collector cycles every 30 s; 3 min without one is a problem
  const state = { me: null, range: "24h", tab: {}, timer: null, seq: 0, reasons: {}, armed: {} };

  // ---------- helpers ----------
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const money = (v, digits) => {
    if (v == null || !isFinite(v)) return "—";
    if (digits == null) digits = Math.abs(v) >= 100 ? 0 : Math.abs(v) >= 1 ? 2 : Math.abs(v) >= 0.01 ? 3 : 4;
    return "$" + Number(v).toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
  };
  const pct = (v) => (v == null || !isFinite(v) ? "—" : Math.round(v * 100) + "%");
  const num = (v, d = 1) => (v == null || !isFinite(v) ? "—" : Number(v).toLocaleString("en-US", { maximumFractionDigits: d }));
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + "s"}`;
  const bytes = (v) => {
    if (v == null || !isFinite(v)) return "—";
    const gi = v / 2 ** 30;
    return gi >= 1 ? num(gi, gi >= 10 ? 0 : 1) + " GiB" : num(v / 2 ** 20, 0) + " MiB";
  };
  const secsSince = (t) => Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  const ago = (t) => {
    if (!t) return "";
    const s = secsSince(t);
    if (s < 60) return Math.round(s) + "s ago";
    if (s < 3600) return Math.round(s / 60) + " min ago";
    if (s < 86400) return Math.round(s / 3600) + " h ago";
    return Math.round(s / 86400) + " d ago";
  };
  const when = (t) => (t ? new Date(t).toLocaleString(undefined, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" }) : "");
  const hhmm = (t) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const dayLabel = (t) => new Date(t).toLocaleDateString(undefined, { day: "2-digit", month: "short" });
  const dur = (secs) => {
    secs = Math.round(secs || 0);
    if (secs < 60) return secs + "s";
    if (secs < 3600) return Math.floor(secs / 60) + "m " + (secs % 60) + "s";
    if (secs < 86400) return Math.floor(secs / 3600) + "h " + Math.floor((secs % 3600) / 60) + "m";
    return Math.floor(secs / 86400) + "d " + Math.floor((secs % 86400) / 3600) + "h";
  };
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
  };
  const modelHref = (ns, name) => `#/models/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;

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
  // safe keeps one failing endpoint from blanking a whole page.
  const safe = (p, fallback) => p.catch((e) => { if (e.message === "signin") throw e; return fallback; });

  function toast(msg, kind = "") {
    let box = document.querySelector(".toasts");
    if (!box) { box = document.createElement("div"); box.className = "toasts"; box.setAttribute("role", "status"); document.body.appendChild(box); }
    const t = document.createElement("div");
    t.className = "toast " + kind;
    t.textContent = msg;
    box.appendChild(t);
    setTimeout(() => t.remove(), 4500);
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
    back: '<path d="M15 5l-7 7 7 7"/>',
    clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/>',
  };
  const icon = (n, size = 18, color = "currentColor") =>
    `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="${color}" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICONS[n]}</svg>`;
  const logo = (size = 30) =>
    `<div class="logo" style="width:${size}px;height:${size}px"><svg width="${size * 0.66}" height="${size * 0.66}" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 13.5 6 6.5l3.5 11 3-6.5 2 3h7"/></svg></div>`;

  let chartSeq = 0;
  // chart draws one or more lines over the same times. Labels live in HTML,
  // outside the stretched SVG, so text never distorts. Hovering a column
  // shows its time and values.
  function chart({ times, series, height = 130, fmt = (v) => num(v), label = "chart", daily = false }) {
    const n = times.length;
    const has = series.some((s) => s.values.filter((v) => v != null).length >= 2);
    if (!has) return `<div class="empty" style="padding:24px"><b>Not enough data yet</b>The chart fills in as Sophron collects.</div>`;
    const all = series.flatMap((s) => s.values.filter((v) => v != null));
    let hi = Math.max(...all), lo = Math.min(0, ...all);
    if (hi - lo < 1e-9) hi = lo + 1;
    hi *= 1.08;
    const W = 1000, H = height, X = (i) => (n <= 1 ? 0 : (i / (n - 1)) * W), Y = (v) => H - 4 - ((v - lo) / (hi - lo)) * (H - 10);
    const id = "cg" + ++chartSeq;
    const paths = series.map((s, k) => {
      const pts = s.values.map((v, i) => [i, v]).filter((p) => p[1] != null);
      if (pts.length < 2) return "";
      const d = "M" + pts.map((p) => `${X(p[0]).toFixed(1)} ${Y(p[1]).toFixed(1)}`).join(" L");
      const color = s.color || "var(--brand)";
      const fill = k === 0 && !s.dashed ? `<path d="${d} L${X(pts[pts.length - 1][0])} ${H} L${X(pts[0][0])} ${H} Z" fill="url(#${id})"/>` : "";
      return `${fill}<path d="${d}" fill="none" stroke="${color}" stroke-width="2" ${s.dashed ? 'stroke-dasharray="5 5"' : ""} vector-effect="non-scaling-stroke" stroke-linejoin="round"/>`;
    }).join("");
    const grid = [0.25, 0.5, 0.75].map((g) => `<line x1="0" x2="${W}" y1="${H * g}" y2="${H * g}" stroke="var(--line)" stroke-dasharray="3 4" vector-effect="non-scaling-stroke"/>`).join("");
    const tl = (t) => (daily ? dayLabel(t) : hhmm(t));
    const hover = times.map((t, i) => {
      const x0 = n <= 1 ? 0 : X(i) - W / (n - 1) / 2;
      const tip = `${daily ? dayLabel(t) : when(t)}\n` + series.map((s) => `${s.name}: ${s.values[i] == null ? "—" : fmt(s.values[i])}`).join("\n");
      return `<rect x="${Math.max(0, x0).toFixed(1)}" y="0" width="${(W / Math.max(1, n - 1)).toFixed(1)}" height="${H}" fill="transparent"><title>${esc(tip)}</title></rect>`;
    }).join("");
    const legend = series.length > 1 ? `<div class="legend">${series.map((s) => `<span><i style="background:${s.color || "var(--brand)"}${s.dashed ? ";opacity:.6" : ""}"></i>${esc(s.name)}</span>`).join("")}</div>` : "";
    return `<div class="chart">${legend}<div class="ymax num">${esc(fmt(hi / 1.08))}</div>
      <svg viewBox="0 0 ${W} ${H}" width="100%" height="${H}" preserveAspectRatio="none" role="img" aria-label="${esc(label)}">
      <defs><linearGradient id="${id}" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="var(--brand)" stop-opacity=".26"/><stop offset="1" stop-color="var(--brand)" stop-opacity="0"/></linearGradient></defs>
      ${grid}${paths}${hover}</svg>
      <div class="xaxis num"><span>${esc(tl(times[0]))}</span><span>${esc(tl(times[Math.floor((n - 1) / 2)]))}</span><span>${esc(n > 1 ? tl(times[n - 1]) : "")}</span></div></div>`;
  }

  // perHour turns trend buckets into USD per hour, so the first and the
  // current bucket (only partly covered) don't look like a drop in spend.
  function perHour(trend, key = "modelCost") {
    if (trend.length < 2) return trend.map((p) => (empty(p) ? null : p[key]));
    const bucket = (new Date(trend[1].time) - new Date(trend[0].time)) / 1000;
    const winStart = Date.now() - RANGE_HOURS[trendRange()] * 3600e3;
    return trend.map((p) => {
      const start = Math.max(new Date(p.time).getTime(), winStart), end = Math.min(new Date(p.time).getTime() + bucket * 1000, Date.now());
      const hours = (end - start) / 3600e3;
      if (empty(p)) return null; // no data that hour (collector down): a gap, not $0
      return hours > bucket / 3600 / 20 ? p[key] / hours : null; // skip slivers
    });
  }
  const empty = (p) => !p.modelCost && !p.outputTokens;
  const trendRange = () => (state.range === "1h" ? "24h" : state.range);
  const trendWindow = () => RANGES[trendRange()];

  // down averages a long series into at most n points.
  function down(rows, n, pick) {
    if (rows.length <= n) return rows.map((r) => ({ t: r.t, v: pick(r) }));
    const size = rows.length / n, out = [];
    for (let i = 0; i < n; i++) {
      const part = rows.slice(Math.floor(i * size), Math.floor((i + 1) * size));
      const vals = part.map(pick).filter((v) => v != null);
      out.push({ t: part[0].t, v: vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : null });
    }
    return out;
  }

  const SEV = { critical: 3, warning: 2, info: 1 };
  const bySeverity = (a, b) => (SEV[b.severity] || 0) - (SEV[a.severity] || 0) || new Date(b.firstSeen) - new Date(a.firstSeen);
  function shortTitle(f) {
    const m = { "oom-killed": "OOM killed", "container-failing": "crashing", "pending-unschedulable": "can't schedule",
      "rollout-stuck": "rollout stuck", "slow-cold-start": "slow start", "queue-backlog": "queue backing up" };
    return m[f.detector] || f.detector;
  }
  function statusChip(f) {
    if (!f) return `<span class="chip good"><span class="dot"></span>healthy</span>`;
    if (f.severity === "critical") return `<span class="chip crit">▲ ${esc(shortTitle(f))}</span>`;
    if (f.severity === "warning") return `<span class="chip warn"><span class="dot"></span>${esc(shortTitle(f))}</span>`;
    return `<span class="chip info"><span class="dot"></span>${esc(shortTitle(f))}</span>`;
  }
  const sevChip = (s) => s === "critical" ? `<span class="chip crit">▲ critical</span>` : s === "warning" ? `<span class="chip warn"><span class="dot"></span>warning</span>` : `<span class="chip info"><span class="dot"></span>${esc(s || "info")}</span>`;
  const FIX_STATUS = { pending: "warn", approved: "info", applied: "info", verified: "good", denied: "muted", obsolete: "muted", superseded: "muted", blocked: "muted", failed: "crit" };
  const fixChip = (s) => `<span class="chip ${FIX_STATUS[s] || "muted"}">${s === "failed" ? "▲ " : '<span class="dot"></span>'}${esc(s)}</span>`;
  const FIX_HELP = { superseded: "a bigger step replaced it", obsolete: "the problem went away first", blocked: "the policy doesn't allow it", verified: "the problem is gone" };

  // ---------- routing ----------
  const PAGES = ["overview", "models", "problems", "approvals", "cost", "activity", "setup"];
  const TITLES = { overview: "Overview", models: "Models", problems: "Problems", approvals: "Approvals", cost: "Cost", activity: "Activity", setup: "Setup" };
  function route() {
    const parts = location.hash.replace(/^#\/?/, "").split("?")[0].split("/").map(decodeURIComponent);
    const p = PAGES.includes(parts[0]) ? parts[0] : "overview";
    if (p === "models" && parts.length >= 3) return { page: p, view: "model", ns: parts[1], name: parts.slice(2).join("/") };
    return { page: p, view: p };
  }
  const routeKey = () => { const r = route(); return r.view === "model" ? `model/${r.ns}/${r.name}` : r.page; };

  // ---------- shell ----------
  function shell(counts) {
    const r = route(), p = r.page, me = state.me || {};
    const links = (cls) => PAGES.map((n) => {
      let b = "";
      if (n === "approvals" && counts.pending) b = `<span class="badge">${counts.pending}</span>`;
      if (n === "problems" && counts.open) b = `<span class="badge crit">${counts.open}</span>`;
      return `<a class="${cls} ${n === p ? "on" : ""}" href="#/${n}" ${n === p ? 'aria-current="page"' : ""}>${icon(n)}<span>${TITLES[n]}</span>${b}</a>`;
    }).join("");
    const user = me.loggedIn && me.user ? me.user : { name: "guest", role: "viewer" };
    const ranged = p === "overview" || p === "cost" || p === "models";
    const seg = ranged ? `<div class="seg" role="group" aria-label="Time range">${Object.keys(RANGES).map((k) => `<button data-range="${k}" class="${k === state.range ? "on" : ""}" aria-pressed="${k === state.range}">${k}</button>`).join("")}</div>` : "";
    const dark = document.documentElement.dataset.theme === "dark";
    const title = r.view === "model" ? `<a class="crumb" href="#/models">${icon("back", 16)}Models</a><h1>${esc(r.name)}</h1>` : `<h1>${TITLES[p]}</h1>`;
    return `<div class="shell"><aside class="side">
      <div class="brand">${logo()}Sophron</div>
      <div class="cluster" title="Cluster"><span class="dot" id="clusterdot" style="color:var(--good)"></span>${esc(me.cluster || "cluster")}</div>
      <nav aria-label="Main">${links("nav")}</nav>
      <div class="sidecard"><div style="display:flex;gap:8px;align-items:center;font-weight:600">${icon("lock", 15, "var(--sage)")}${me.authEnabled ? "Approval mode" : "Read-only mode"}</div>
        <div class="ter" style="margin-top:4px">${me.authEnabled ? "Fixes run only after an admin approves, within the policy." : "No admin password set: nothing can be approved. Run make admin-password."}</div>${me.version ? `<div class="ter" style="margin-top:8px;font-size:11.5px">Sophron ${esc(me.version)}</div>` : ""}</div>
      <div class="me"><div class="avatar">${esc((user.name || "?")[0])}</div><div style="flex:1;min-width:0"><b>${esc(user.name)}</b><div class="ter" style="font-size:12px">${esc(user.role)}</div></div>
        ${me.loggedIn ? `<button class="iconbtn" style="width:32px;height:32px" id="logout" title="Sign out" aria-label="Sign out">${icon("out", 16)}</button>` : me.authEnabled ? `<a href="#/login">Sign in</a>` : ""}</div>
    </aside><div class="main"><div class="top">${title}${seg}
      <div class="fresh ter" id="updated"></div>
      <button class="iconbtn" id="theme" title="Light or dark" aria-label="Switch to ${dark ? "light" : "dark"} mode">${icon(dark ? "sun" : "moon", 17)}</button>
      ${me.loggedIn ? `<button class="iconbtn mobile-only" id="logout2" title="Sign out" aria-label="Sign out">${icon("out", 16)}</button>` : ""}</div>
      <nav class="mnav" aria-label="Main">${links("mlink")}</nav>
      <div id="banner"></div>
      <div class="content" id="content"><div class="boot">Loading…</div></div></div></div>`;
  }

  function schedule() {
    clearTimeout(state.timer);
    state.timer = setTimeout(() => (document.hidden ? schedule() : render()), POLL_MS);
  }

  async function render() {
    if (location.hash === "#/login") return renderLogin();
    clearTimeout(state.timer);
    const seq = ++state.seq; // a newer render (a click, a poll) makes this one stale
    try {
      if (!state.me) state.me = await api("/me");
      if (state.me.authEnabled && !state.me.loggedIn) return renderLogin();
      const [open, pending, status] = await Promise.all([safe(api("/findings?status=open"), []), safe(api("/fixes?status=pending"), []), safe(api("/status"), null)]);
      if (seq !== state.seq) return;
      const key = routeKey(), counts = `${open.length}/${pending.length}`;
      if (!document.getElementById("content") || app.dataset.page !== key || app.dataset.counts !== counts) {
        app.innerHTML = shell({ open: open.length, pending: pending.length });
        app.dataset.page = key;
        app.dataset.counts = counts;
        wireShell();
      }
      document.title = `${open.length ? `(${open.length}) ` : ""}${route().view === "model" ? route().name : TITLES[route().page]} · Sophron`;
      const r = route();
      const html = await (r.view === "model" ? modelView(r, { open, pending }) : VIEWS[r.page]({ open, pending }));
      if (seq !== state.seq) return;
      const c = document.getElementById("content");
      const a = document.activeElement; // don't redraw under someone typing a reason
      if (c && !(a && c.contains(a) && a.matches("input, textarea, select"))) {
        const scroll = window.scrollY;
        c.innerHTML = html;
        wireContent();
        window.scrollTo(0, scroll);
      }
      freshness(status);
    } catch (e) {
      if (e.message === "signin" || seq !== state.seq) return;
      const c = document.getElementById("content") || app;
      c.innerHTML = `<div class="card empty"><b>Can't reach the Sophron API</b>${esc(e.message)}<div class="note" style="margin-top:8px">Retrying every 15 seconds.</div></div>`;
    }
    schedule();
  }

  // freshness says how old the data is, and warns when the collector stalls.
  function freshness(status) {
    const u = document.getElementById("updated"), b = document.getElementById("banner"), dot = document.getElementById("clusterdot");
    if (!u) return;
    const age = status && status.lastRun ? secsSince(status.lastRun) : null;
    const stale = age != null && age > STALE_SECS;
    const err = status && status.error;
    u.innerHTML = `<span class="dot" style="color:var(--${stale || err ? "warn" : "good"})"></span><span class="hide-sm">${age == null ? "Updated " + hhmm(new Date()) : age < 10 ? "Live" : "Data from " + ago(status.lastRun)}</span>`;
    if (dot) dot.style.color = `var(--${stale || err ? "warn" : "good"})`;
    if (b) b.innerHTML = stale ? `<div class="banner">${icon("clock", 16)}<span>Sophron hasn't collected data for ${dur(age)}. Numbers below may be out of date. Check <span class="mono">make collector-logs</span>.</span></div>`
      : err ? `<div class="banner">${icon("problems", 16)}<span>Last collection had an error: ${esc(err)}</span></div>` : "";
  }

  function wireShell() {
    document.querySelectorAll("[data-range]").forEach((b) => b.addEventListener("click", () => {
      state.range = b.dataset.range;
      document.querySelectorAll("[data-range]").forEach((x) => { x.classList.toggle("on", x === b); x.setAttribute("aria-pressed", x === b); });
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
    const out = async () => { await safe(api("/logout", { method: "POST" }), null); state.me = null; location.hash = "#/overview"; render(); };
    ["logout", "logout2"].forEach((id) => { const lo = document.getElementById(id); if (lo) lo.addEventListener("click", out); });
  }

  function wireContent() {
    document.querySelectorAll("[data-tab]").forEach((b) => b.addEventListener("click", () => {
      const [group, val] = b.dataset.tab.split(":");
      state.tab[group] = val;
      render();
    }));
    document.querySelectorAll("input.reason").forEach((i) => {
      const id = i.id.replace("reason-", "");
      i.value = state.reasons[id] || "";
      i.addEventListener("input", () => { state.reasons[id] = i.value; });
    });
    document.querySelectorAll("[data-decide]").forEach((b) => b.addEventListener("click", async () => {
      const [id, decision] = b.dataset.decide.split(":");
      // Approve changes the cluster: ask for a second click, like Slack's confirm.
      if (decision === "approve" && !state.armed[id]) {
        state.armed[id] = true;
        b.textContent = "Click again to apply";
        b.classList.add("armed");
        setTimeout(() => { delete state.armed[id]; if (document.body.contains(b)) { b.textContent = "Approve fix"; b.classList.remove("armed"); } }, 5000);
        return;
      }
      delete state.armed[id];
      b.disabled = true;
      b.textContent = decision === "approve" ? "Applying…" : "Denying…";
      try {
        const r = await api(`/fixes/${id}/${decision}`, { method: "POST", body: JSON.stringify({ reason: state.reasons[id] || "" }) });
        delete state.reasons[id];
        toast(decision === "approve" ? `Fix #${id} ${r.status}${r.result ? ": " + r.result : ""}` : `Fix #${id} denied`, r.status === "failed" ? "bad" : "");
      } catch (e) { if (e.message !== "signin") toast(e.message, "bad"); }
      if (document.activeElement) document.activeElement.blur();
      app.dataset.counts = "";
      render();
    }));
    document.querySelectorAll("[data-href]").forEach((el) => {
      el.addEventListener("click", (e) => { if (!e.target.closest("a,button,input")) location.hash = el.dataset.href; });
      el.addEventListener("keydown", (e) => { if (e.key === "Enter") location.hash = el.dataset.href; });
    });
    document.querySelectorAll("[data-copy]").forEach((b) => b.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(b.dataset.copy); toast("Copied"); } catch { toast("Couldn't copy; select the text instead", "bad"); }
    }));
  }

  // ---------- shared pieces ----------
  const worst = (fs) => fs.slice().sort(bySeverity)[0];
  const ofModel = (f, ns, name) => f.namespace === ns && (f.workloadName === name || (!f.workloadName && f.pod && f.pod.startsWith(name)));
  const forModel = (open, m) => worst(open.filter((f) => ofModel(f, m.namespace, m.workload)));
  const perToken = (m) => m.costPer1MOutputTokensBusy ?? m.costPer1MOutputTokens;
  const tscroll = (html) => `<div class="tscroll">${html}</div>`;
  const snip = (text) => `<div class="snipwrap"><pre class="snip">${esc(text)}</pre><button class="copy" data-copy="${esc(text)}" title="Copy" aria-label="Copy">Copy</button></div>`;
  const cheaperAPI = (m) => (m.apiComparison || []).filter((c) => c.apiCostPerMonth < c.selfCostPerMonth).sort((a, b) => a.apiCostPerMonth - b.apiCostPerMonth)[0];

  function modelStatus(m, f) {
    if (f) return statusChip(f);
    if (m.pods === 0) return `<span class="chip muted">no pods</span>`;
    if (m.readyPods === 0) return `<span class="chip warn"><span class="dot"></span>not ready</span>`;
    if (m.readyPods < m.pods) return `<span class="chip warn"><span class="dot"></span>${m.readyPods}/${m.pods} ready</span>`;
    return statusChip(null);
  }

  function capBar(cap) {
    return `<div style="display:flex;align-items:center;gap:10px"><div class="bar" role="meter" aria-valuenow="${Math.round((cap || 0) * 100)}" aria-valuemin="0" aria-valuemax="100" aria-label="capacity used"><i class="${cap > 0.9 ? "warn" : ""}" style="width:${Math.min(100, Math.round((cap || 0) * 100))}%"></i></div><span class="num ter" style="width:38px">${pct(cap)}</span></div>`;
  }

  function modelRows(models, open, full) {
    if (!models.length) return `<div class="empty"><b>No model servers yet</b>Label a pod sophron.dev/model-server, or expose vLLM / llama.cpp metrics to Prometheus.</div>`;
    return tscroll(`<table class="table"><thead><tr><th>Model</th><th>Status</th><th>Throughput</th><th>$ / 1M tokens</th>${full ? "<th>Per month</th><th>vs API</th>" : ""}<th>Capacity used</th></tr></thead><tbody>
      ${models.map((m) => {
        const f = forModel(open, m), api = cheaperAPI(m);
        return `<tr class="click" tabindex="0" data-href="${modelHref(m.namespace, m.workload)}"><td><a href="${modelHref(m.namespace, m.workload)}" class="rowlink">${esc(m.namespace)}/${esc(m.workload)}</a><div class="sub2">${esc(m.server || "")} · ${m.readyPods}/${m.pods} ready</div></td>
          <td>${modelStatus(m, f)}</td><td class="num">${num(m.outputTokensPerSecond)} <span class="ter">tok/s</span><div class="sub2">peak ${num(m.peakOutputTokensPerSecond, 0)}</div></td>
          <td class="num">${money(perToken(m))}${m.costPer1MOutputTokensBusy != null ? `<div class="sub2" title="Counts only the time it was producing tokens">busy ${pct(m.busyShare)}</div>` : ""}</td>
          ${full ? `<td class="num">${money(m.monthForecast)}${m.estimate ? '<div class="sub2">estimate</div>' : ""}</td>
            <td>${(m.apiComparison || []).length ? (api ? `<span class="chip warn" title="${esc(api.api)}">API ${pct(1 - api.apiCostPerMonth / api.selfCostPerMonth)} cheaper</span>` : `<span class="chip good">self-hosting cheaper</span>`) : `<span class="ter">—</span>`}</td>` : ""}
          <td>${capBar(m.capacityUsed)}</td></tr>`;
      }).join("")}</tbody></table>`);
  }

  function fixCard(r, canApprove, finding) {
    const a = r.action || {};
    const ev = finding && (finding.evidence || []).length ? `<ul class="ev">${finding.evidence.slice(0, 3).map((e) => `<li>${esc(e)}</li>`).join("")}</ul>` : "";
    const armed = state.armed[r.id];
    return `<div class="alert approve"><div class="grow">
      <div class="meta">${fixChip(r.status)}${r.severity ? sevChip(r.severity) : ""}<span class="ter">Fix #${r.id} · requested ${ago(r.requestedAt)}</span></div>
      <div style="font-weight:600;font-size:15px;margin-top:8px">${esc(a.summary || "Change the workload")}</div>
      <div class="mut" style="margin-top:4px">Because: ${esc(r.title)}${a.namespace && a.workloadName ? ` · <a href="${modelHref(a.namespace, a.workloadName)}">open model</a>` : ""}</div>
      ${ev}
      <div class="kv"><span>Target</span><span class="mono">${esc(a.namespace)}/${esc((a.workloadKind || "").toLowerCase())}/${esc(a.workloadName)}${a.container ? " · " + esc(a.container) : ""}</span>
        <span>Change</span><span class="mono">${esc(a.kind)}${a.value ? " → " + esc(a.value) : ""}</span></div>
      ${canApprove ? `<input class="reason" id="reason-${r.id}" placeholder="Reason (optional, goes in the audit log)" aria-label="Reason for fix ${r.id}">` : `<div class="note" style="margin-top:10px">Sign in as admin to approve or deny.</div>`}
    </div><div class="actions">
      <button class="btn pri ${armed ? "armed" : ""}" data-decide="${r.id}:approve" ${canApprove ? "" : "disabled"}>${armed ? "Click again to apply" : "Approve fix"}</button>
      <button class="btn sec" data-decide="${r.id}:deny" ${canApprove ? "" : "disabled"}>Deny</button></div></div>`;
  }

  function costOff(e) {
    return `<div class="card empty"><b>Cost isn't available</b>${esc(e.message)}<div class="note" style="margin-top:8px">The cost engine runs when the collector starts with <span class="mono">--cost</span> (on by default).</div></div>`;
  }

  // auditChip colours an audit event by what it means.
  const auditChip = (ev) => `<span class="chip ${ev.includes("fail") ? "crit" : ev.includes("verified") || ev === "login" ? "good" : ev === "login.failed" ? "warn" : ev.includes("approved") || ev.includes("applied") ? "info" : ev.includes("denied") || ev.includes("blocked") ? "warn" : "muted"}">${esc(ev)}</span>`;

  // ---------- views ----------
  const VIEWS = {
    async overview({ open, pending }) {
      const w = RANGES[state.range];
      const [sum, models, trend, savings, log] = await Promise.all([
        safe(api("/cost/summary?window=" + w), null), safe(api("/cost/models?window=" + w), []),
        safe(api("/cost/trend?window=" + trendWindow()), []), safe(api("/savings"), []), safe(api("/audit"), []),
      ]);
      const live = models.filter((m) => m.pods > 0);
      const crit = open.filter((f) => f.severity === "critical");
      const now = [];
      now.push(open.length ? `${plural(open.length, "open problem")}${crit.length ? ` (${crit.length} critical)` : ""}` : "no open problems");
      if (pending.length) now.push(`${plural(pending.length, "fix", "fixes")} waiting for approval`);
      if (sum) now.push(`spending ${money(sum.spendPerHour)}/hour, ${pct(sum.idleShare)} of it on idle capacity`);
      const busy = live.filter((m) => (m.capacityUsed || 0) > 0.9);
      if (busy.length) now.push(`${busy.map((m) => m.workload).join(", ")} near peak capacity`);
      const byId = Object.fromEntries(open.map((f) => [f.id, f]));
      const needs = [
        ...pending.slice(0, 2).map((r) => fixCard(r, state.me && state.me.canApprove, byId[r.findingId])),
        ...open.filter((f) => !pending.some((r) => r.findingId === f.id)).sort(bySeverity).slice(0, 3).map((f) =>
          `<div class="alert ${f.severity === "critical" ? "" : "soft"}">${statusChip(f)}<div class="grow"><b>${esc(f.title)}</b><div class="mut" style="margin-top:3px">${esc(f.fix && f.fix.summary)}</div></div><a class="btn sec" href="#/problems">Details</a></div>`),
      ].join("");
      const other = sum ? Math.max(0, sum.spendPerHour - sum.modelCostPerHour - sum.idleCostPerHour) : 0;
      const split = sum && sum.spendPerHour > 0 ? `<div class="stack" style="margin-top:14px" title="models · other pods · idle">
          <i style="width:${(sum.modelCostPerHour / sum.spendPerHour) * 100}%;background:var(--brand)"></i><i style="width:${(other / sum.spendPerHour) * 100}%;background:var(--sage)"></i></div>
          <div class="legend" style="margin-top:8px"><span><i style="background:var(--brand)"></i>models ${money(sum.modelCostPerHour)}/h</span><span><i style="background:var(--sage)"></i>other ${money(other)}/h</span><span><i style="background:var(--raised);border:1px solid var(--line2)"></i>idle ${money(sum.idleCostPerHour)}/h</span></div>` : "";
      const saved = savings.reduce((a, s) => a + (s.savingPerMonth || 0), 0);
      return `<div class="summary">${icon("spark", 17, "var(--brand)")}<span><b>Right now:</b> ${esc(now.join(" · "))}.</span></div>
        ${needs}
        <div class="row">
          <div class="card pad kpi" style="flex:1.5"><div class="label">Month forecast</div>
            <div class="value" style="font-size:34px">${money(sum && sum.monthForecast, 0)}</div><div class="sub">${money(sum && sum.spendPerHour)}/hour across ${sum ? plural(sum.nodes, "node") : "—"}${sum && sum.estimatedNodes ? ` · ${sum.estimatedNodes} estimated` : ""}</div>${split}</div>
          <div class="card pad kpi grow"><div class="label">Cost per 1M tokens</div><div class="value">${money(sum && sum.costPer1MOutputTokens)}</div><div class="sub">output tokens, all models, last ${state.range}</div></div>
          <div class="card pad kpi grow"><div class="label">Models serving</div><div class="value">${sum ? sum.modelsServing : "—"} <span class="ter" style="font-size:16px">of ${sum ? sum.models : "—"}</span></div><div class="sub">${open.length ? plural(open.length, "open problem") : "all healthy"}</div></div>
          <div class="card pad kpi grow"><div class="label">Could save</div><div class="value" style="color:var(--good)">${saved > 0 ? money(saved, 0) : "—"}<span class="ter" style="font-size:15px">${saved > 0 ? "/mo" : ""}</span></div><div class="sub">${savings.length ? plural(savings.length, "suggestion") : "no suggestions yet"}</div></div>
        </div>
        <div class="row">
          <div class="card grow" style="flex:2.1;overflow:hidden"><div class="cardhead"><div class="h2">Models</div><a href="#/models">View all →</a></div>${modelRows(live.slice(0, 6), open, false)}</div>
          <div class="col">
            <div class="card pad"><div style="display:flex;justify-content:space-between;gap:10px"><div class="h2">Cost per 1M tokens</div><span class="num" style="font-weight:600">${money(sum && sum.costPer1MOutputTokens)}</span></div>
              <div class="sub2" style="margin-bottom:8px">All models, ${trendRange() === "7d" ? "per day" : "per hour"}, last ${trendRange()}</div>
              ${chart({ times: trend.map((p) => p.time), series: [{ name: "$ / 1M tokens", values: trend.map((p) => p.costPer1MOutputTokens) }], height: 110, fmt: (v) => money(v), label: "cost per 1M tokens", daily: trendRange() === "7d" })}</div>
            <div class="card pad"><div style="display:flex;justify-content:space-between"><div class="h2">Top savings</div><a href="#/cost">All →</a></div>
              ${savings.length ? `<div class="list" style="margin-top:4px">${savings.slice(0, 3).map((s) => `<div style="display:flex;justify-content:space-between;gap:10px"><span>${esc(s.title)}</span><b class="num" style="color:var(--good);white-space:nowrap">${s.savingPerMonth > 0 ? money(s.savingPerMonth, 0) + "/mo" : "reliability"}</b></div>`).join("")}</div>`
                : `<div class="note" style="margin-top:6px">No savings yet. Suggestions need a few hours of history.</div>`}</div>
          </div></div>
        <div class="card" style="overflow:hidden"><div class="cardhead"><div class="h2">Recent activity</div><a href="#/activity">All →</a></div>
          ${log.length ? `<div class="feed">${log.slice(0, 6).map((e) => `<div>${auditChip(e.event)}<span class="grow"><b>${esc(e.actor)}</b> <span class="mut">${esc(e.detail || e.target)}</span></span><span class="ter nowrap">${ago(e.at)}</span></div>`).join("")}</div>`
            : `<div class="empty" style="padding:22px">Nothing yet. Fix requests, approvals and sign-ins show up here.</div>`}</div>`;
    },

    async models({ open }) {
      let models;
      try { models = await api("/cost/models?window=" + RANGES[state.range]); } catch (e) { if (e.message === "signin") throw e; return costOff(e); }
      const live = models.filter((m) => m.pods > 0), gone = models.filter((m) => m.pods === 0);
      return `<div class="card" style="overflow:hidden">${modelRows(live, open, true)}</div>
        ${gone.length ? `<div class="card" style="overflow:hidden"><div class="cardhead"><div class="h2">Seen in the last ${state.range}, no pods now</div></div>${modelRows(gone, open, true)}</div>` : ""}
        <div class="note">Click a model for its charts, pods, costs and history. "While busy" counts only the time it was producing tokens, so idle hours don't inflate the price.</div>`;
    },

    async problems({ open }) {
      const tab = state.tab.problems || "open";
      const [list, pending] = await Promise.all([tab === "open" ? open.slice().sort(bySeverity) : api("/findings?status=resolved&limit=50"), safe(api("/fixes?status=pending"), [])]);
      const tabs = `<div class="tabs" role="tablist"><button role="tab" aria-selected="${tab === "open"}" data-tab="problems:open" class="${tab === "open" ? "on" : ""}">Open (${open.length})</button><button role="tab" aria-selected="${tab === "resolved"}" data-tab="problems:resolved" class="${tab === "resolved" ? "on" : ""}">Resolved</button></div>`;
      if (!list.length) return tabs + `<div class="card empty"><b>${tab === "open" ? "Nothing is wrong right now" : "Nothing resolved yet"}</b>${tab === "open" ? "Six detectors check every model every 30 seconds." : "Resolved problems keep their receipt here: what changed, who approved it, what the outage cost."}</div>`;
      return tabs + list.map((f) => {
        const r = f.resolution, fix = f.fix || {}, wait = pending.find((p) => p.findingId === f.id);
        const target = f.workloadName ? `<a href="${modelHref(f.namespace, f.workloadName)}">${esc(f.namespace)}/${esc(f.workloadName)}</a>` : esc(`${f.namespace}/${f.pod || ""}`);
        return `<div class="card pad"><div class="meta">${tab === "open" ? statusChip(f) : '<span class="chip good"><span class="dot"></span>resolved</span>'}<span class="ter">${target} · first seen ${ago(f.firstSeen)}${f.resolvedAt ? ` · resolved ${ago(f.resolvedAt)} after ${dur((new Date(f.resolvedAt) - new Date(f.firstSeen)) / 1000)}` : ""}</span></div>
          <div style="font-weight:600;font-size:15.5px;margin-top:10px">${esc(f.title)}</div><div class="mut" style="margin-top:6px;line-height:1.55">${esc(f.summary)}</div>
          ${(f.evidence || []).length ? `<ul class="ev">${f.evidence.map((e) => `<li>${esc(e)}</li>`).join("")}</ul>` : ""}
          ${tab === "open" && fix.summary ? `<div style="margin-top:12px"><b>Suggested fix:</b> ${esc(fix.summary)}</div>${(fix.steps || []).length ? `<ol class="ev">${fix.steps.map((s) => `<li>${esc(s)}</li>`).join("")}</ol>` : ""}${fix.snippet ? snip(fix.snippet) : ""}
            ${wait ? `<div class="callout">${fixChip("pending")}<span class="grow">Fix #${wait.id} is waiting: ${esc(wait.action && wait.action.summary)}</span><a class="btn pri" href="#/approvals">Review</a></div>`
              : fix.action ? `<div class="note" style="margin-top:10px">Sophron can do this for you: ${esc(fix.action.summary)}.</div>` : ""}` : ""}
          ${r ? `<div style="margin-top:12px"><b>How it was resolved:</b> ${esc(r.summary)}</div>${(r.changes || []).length ? `<ul class="ev">${r.changes.map((c) => `<li>${esc(c)}</li>`).join("")}</ul>` : ""}
            <div class="kv"><span>Changed with</span><span>${esc(r.changedBy || "no change recorded")}</span><span>Approved by</span><span>${esc(r.approvedBy || "not needed")}</span>
            ${r.downSeconds ? `<span>Not serving for</span><span>${dur(r.downSeconds)}</span>` : ""}${r.downCost != null ? `<span>Cost while down</span><span class="num">${money(r.downCost)}</span>` : ""}</div>` : ""}</div>`;
      }).join("");
    },

    async approvals({ open, pending }) {
      const all = await api("/fixes?status=all");
      const done = all.filter((r) => r.status !== "pending");
      const can = state.me && state.me.canApprove;
      const byId = Object.fromEntries(open.map((f) => [f.id, f]));
      const counts = done.reduce((a, r) => ((a[r.status] = (a[r.status] || 0) + 1), a), {});
      return `${pending.length ? pending.map((r) => fixCard(r, can, byId[r.findingId])).join("") : `<div class="card empty"><b>Nothing waiting for you</b>When a problem has a fix Sophron can apply, it shows up here first. Nothing changes until an admin approves.</div>`}
        <div class="card" style="overflow:hidden"><div class="cardhead"><div class="h2">History</div><div class="meta">${Object.entries(counts).map(([s, n]) => `<span class="ter">${n} ${esc(s)}</span>`).join("<span class='ter'>·</span>")}</div></div>
        ${done.length ? tscroll(`<table class="table"><thead><tr><th>Fix</th><th>Status</th><th>Decided by</th><th>Result</th><th>When</th></tr></thead><tbody>${done.map((r) => {
          const a = r.action || {};
          return `<tr><td><b>#${r.id}</b> ${esc(a.summary || a.kind)}<div class="sub2">${esc(r.title)}</div></td><td>${fixChip(r.status)}${FIX_HELP[r.status] ? `<div class="sub2">${FIX_HELP[r.status]}</div>` : ""}</td><td>${esc(r.decidedBy || (r.status === "blocked" ? "policy" : "—"))}${r.reason ? `<div class="sub2">“${esc(r.reason)}”</div>` : ""}</td><td class="mut">${esc(r.result || "")}</td><td class="ter nowrap">${when(r.verifiedAt || r.appliedAt || r.decidedAt || r.requestedAt)}</td></tr>`;
        }).join("")}</tbody></table>`) : `<div class="empty">No decisions yet.</div>`}</div>`;
    },

    async cost() {
      const w = RANGES[state.range];
      let sum;
      try { sum = await api("/cost/summary?window=" + w); } catch (e) { if (e.message === "signin") throw e; return costOff(e); }
      const [nodes, savings, trend] = await Promise.all([safe(api("/cost/nodes"), []), safe(api("/savings"), []), safe(api("/cost/trend?window=" + trendWindow()), [])]);
      const daily = trendRange() === "7d";
      const share = (v, n) => (n.hourly > 0 ? Math.max(0, Math.min(100, (v / n.hourly) * 100)) : 0);
      const tokens = perHour(trend, "outputTokens").map((v) => (v == null ? null : v / 1e6));
      return `<div class="row">
          <div class="card pad kpi grow"><div class="label">Spend</div><div class="value">${money(sum.spendPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${money(sum.monthForecast, 0)} per month, ${plural(sum.nodes, "node")}</div></div>
          <div class="card pad kpi grow"><div class="label">Models</div><div class="value">${money(sum.modelCostPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${money(sum.modelCostInWindow)} spent in the last ${state.range} · ${num(sum.outputTokensInWindow / 1e6, 2)}M tokens</div></div>
          <div class="card pad kpi grow"><div class="label">Idle</div><div class="value">${money(sum.idleCostPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${pct(sum.idleShare)} of spend; ${sum.estimatedNodes ? plural(sum.estimatedNodes, "node") + " use estimated prices" : "all prices real"}</div></div>
        </div>
        <div class="row">
          <div class="card pad grow"><div class="h2">Model spend</div><div class="sub2" style="margin-bottom:8px">USD per hour, ${daily ? "daily average" : "hourly"}, last ${trendRange()}</div>
            ${chart({ times: trend.map((p) => p.time), series: [{ name: "$/hour", values: perHour(trend) }], fmt: (v) => money(v), label: "model spend per hour", daily })}</div>
          <div class="card pad grow"><div class="h2">Tokens produced</div><div class="sub2" style="margin-bottom:8px">Millions of output tokens per hour${daily ? ", daily average" : ""}, last ${trendRange()}</div>
            ${chart({ times: trend.map((p) => p.time), series: [{ name: "M tokens", values: tokens }], fmt: (v) => num(v, 2) + "M", label: "output tokens", daily })}</div>
        </div>
        <div class="card" style="overflow:hidden"><div class="cardhead"><div class="h2">Nodes</div><div class="legend"><span><i style="background:var(--brand)"></i>models</span><span><i style="background:var(--sage)"></i>other pods</span><span><i style="background:var(--raised);border:1px solid var(--line2)"></i>idle</span></div></div>
          ${nodes.length ? tscroll(`<table class="table"><thead><tr><th>Node</th><th>Price</th><th>Split</th><th>Idle</th></tr></thead><tbody>
          ${nodes.map((n) => `<tr><td><b>${esc(n.node)}</b><div class="sub2">${esc(n.instanceType || "")} ${esc(n.capacityType || "")}</div></td>
            <td class="num">${money(n.hourly)}/h<div class="sub2">${esc(n.source)}${n.estimate ? " (estimate)" : ""}${n.note ? " · " + esc(n.note) : ""}</div></td>
            <td style="min-width:260px"><div class="stack"><i style="width:${share(n.modelCostPerHour, n)}%;background:var(--brand)"></i><i style="width:${share(n.otherCostPerHour, n)}%;background:var(--sage)"></i></div>
              <div class="sub2">${plural(n.modelPods, "model pod")} ${money(n.modelCostPerHour)} · ${n.otherPods} other ${money(n.otherCostPerHour)} · idle ${money(n.idleCostPerHour)}</div></td>
            <td class="num">${pct(n.idleShare)}</td></tr>`).join("")}</tbody></table>`) : `<div class="empty">No nodes priced yet.</div>`}</div>
        <div class="card pad"><div class="h2">Savings</div><div class="sub2">From the last 7 days, biggest first${savings.length ? ` · ${money(savings.reduce((a, s) => a + s.savingPerMonth, 0), 0)}/month in total` : ""}</div>
          ${savings.length ? `<div class="list" style="margin-top:8px">${savings.map((s) => `<div><div style="display:flex;justify-content:space-between;gap:12px"><b>${esc(s.title)}</b><b class="num" style="color:var(--good);white-space:nowrap">${s.savingPerMonth > 0 ? money(s.savingPerMonth, 2) + "/mo" : "reliability"}</b></div>
            <div class="mut" style="margin-top:4px">${esc(s.detail)}</div>${(s.evidence || []).length ? `<ul class="ev">${s.evidence.map((e) => `<li>${esc(e)}</li>`).join("")}</ul>` : ""}
            <div class="meta" style="margin-top:6px"><span class="chip ${s.confidence === "high" ? "good" : s.confidence === "medium" ? "info" : "muted"}">${esc(s.confidence)} confidence</span>${s.namespace && s.workload ? `<a href="${modelHref(s.namespace, s.workload)}">open model</a>` : ""}</div>${s.snippet ? snip(s.snippet) : ""}</div>`).join("")}</div>`
            : `<div class="empty"><b>No savings found yet</b>Sophron needs a few hours of history (a day or more is best) before it suggests changes.</div>`}</div>`;
    },

    async activity() {
      const log = await api("/audit");
      const tab = state.tab.activity || "all";
      const groups = { all: () => true, fixes: (e) => !e.event.startsWith("login"), signins: (e) => e.event.startsWith("login") };
      const list = log.filter(groups[tab] || groups.all);
      const tabs = `<div class="tabs" role="tablist">${[["all", "All"], ["fixes", "Fixes"], ["signins", "Sign-ins"]].map(([k, l]) => `<button role="tab" aria-selected="${tab === k}" data-tab="activity:${k}" class="${tab === k ? "on" : ""}">${l} (${log.filter(groups[k]).length})</button>`).join("")}</div>`;
      if (!list.length) return tabs + `<div class="card empty"><b>No activity yet</b>Sign-ins, fix requests, approvals and applied changes appear here.</div>`;
      return tabs + `<div class="card" style="overflow:hidden">${tscroll(`<table class="table"><thead><tr><th>When</th><th>Who</th><th>What</th><th>Target</th><th>Detail</th></tr></thead><tbody>
        ${list.map((e) => `<tr><td class="ter nowrap" title="${esc(new Date(e.at).toString())}">${when(e.at)}</td><td><b>${esc(e.actor)}</b></td><td>${auditChip(e.event)}</td>
          <td class="mono">${esc(e.target)}</td><td class="mut">${esc(e.detail)}</td></tr>`).join("")}</tbody></table>`)}
        <div class="note" style="padding:12px 20px">Each entry is chained to the previous one with SHA-256: editing or deleting any line breaks the chain, and Setup shows it. Showing the latest ${log.length}.</div></div>`;
    },

    async setup() {
      const checks = await api("/setup");
      const chip = { ok: "good", warn: "warn", fail: "crit", skip: "muted" };
      const order = { fail: 0, warn: 1, ok: 2, skip: 3 };
      const bad = checks.filter((c) => c.status === "fail").length, warn = checks.filter((c) => c.status === "warn").length;
      return `<div class="summary">${icon(bad ? "problems" : "check", 17, bad ? "var(--crit)" : "var(--good)")}<span><b>${bad ? plural(bad, "problem") : "All good"}${warn ? `, ${warn} to improve` : ""}.</b> Sophron checks its own setup: data sources, prices, alerts, sign-in and permissions. Run <span class="mono">make doctor</span> for the same checks in a terminal.</span></div>
        <div class="card">${checks.slice().sort((a, b) => order[a.status] - order[b.status]).map((c) => `<div class="check"><span class="chip ${chip[c.status] || "muted"}" style="min-width:58px;justify-content:center">${c.status === "fail" ? "▲ " : ""}${esc(c.status)}</span>
          <div class="grow"><b>${esc(c.name)}</b><div class="mut" style="margin-top:3px">${esc(c.detail)}</div>${c.fix ? `<div class="note" style="margin-top:4px">Fix: <span class="mono">${esc(c.fix)}</span></div>` : ""}</div></div>`).join("")}</div>`;
    },
  };

  // ---------- one model ----------
  async function modelView(r, { open }) {
    const w = RANGES[state.range];
    const [models, latest, resolved, fixList, savings] = await Promise.all([
      safe(api("/cost/models?window=" + w), []), safe(api("/snapshots/latest"), []), safe(api("/findings?status=resolved&limit=200"), []),
      safe(api("/fixes?status=all"), []), safe(api("/savings"), []),
    ]);
    const m = models.find((x) => x.namespace === r.ns && x.workload === r.name);
    const pods = latest.filter((s) => s.namespace === r.ns && (s.workloadName === r.name || s.pod.startsWith(r.name + "-")));
    if (!m && !pods.length) return `<div class="card empty"><b>No model called ${esc(r.ns)}/${esc(r.name)}</b>It may have been deleted, or it hasn't been seen in the last ${state.range}.<div style="margin-top:14px"><a class="btn sec" href="#/models">Back to models</a></div></div>`;

    // History of up to 3 pods, joined by minute.
    const hist = await Promise.all(pods.slice(0, 3).map((p) => safe(api(`/snapshots?namespace=${encodeURIComponent(p.namespace)}&pod=${encodeURIComponent(p.pod)}&since=${w}`), [])));
    const byMin = new Map();
    hist.flat().forEach((s) => {
      const t = Math.floor(new Date(s.time).getTime() / 60000) * 60000;
      const row = byMin.get(t) || { t: new Date(t).toISOString(), tok: null, mem: null, lim: null, cpu: null, req: null };
      const add = (k, v) => { if (v != null) row[k] = (row[k] || 0) + v; };
      add("tok", s.generationTokensPerSecond); add("cpu", s.cpuCores); add("req", s.cpuRequestCores || null);
      if (s.memoryWorkingSetBytes != null) row.mem = Math.max(row.mem || 0, s.memoryWorkingSetBytes);
      if (s.memoryLimitBytes) row.lim = Math.max(row.lim || 0, s.memoryLimitBytes);
      byMin.set(t, row);
    });
    const rows = [...byMin.values()].sort((a, b) => new Date(a.t) - new Date(b.t));
    const pts = (k) => down(rows, 180, (x) => x[k]);
    const times = pts("tok").map((p) => p.t);
    const gib = (v) => (v == null ? null : v / 2 ** 30);

    const mine = (f) => ofModel(f, r.ns, r.name);
    const openHere = open.filter(mine).sort(bySeverity);
    const resolvedHere = resolved.filter(mine);
    const fixesHere = fixList.filter((x) => x.action && x.action.namespace === r.ns && x.action.workloadName === r.name);
    const savingsHere = savings.filter((s) => s.namespace === r.ns && s.workload === r.name);

    const events = [];
    [...openHere, ...resolvedHere].forEach((f) => {
      events.push({ at: f.firstSeen, kind: f.severity === "critical" ? "crit" : "warn", text: `Problem: ${f.title}` });
      if (f.resolvedAt) events.push({ at: f.resolvedAt, kind: "good", text: `Resolved: ${f.resolution ? f.resolution.summary : shortTitle(f)}${f.resolution && f.resolution.approvedBy ? ` (approved by ${f.resolution.approvedBy})` : ""}` });
    });
    fixesHere.forEach((x) => {
      const s = (x.action && x.action.summary) || "fix";
      events.push({ at: x.requestedAt, kind: "info", text: `Fix #${x.id} requested: ${s}` });
      if (x.decidedAt) events.push({ at: x.decidedAt, kind: x.status === "denied" ? "muted" : "info", text: `Fix #${x.id} ${x.status === "denied" ? "denied" : "approved"} by ${x.decidedBy || "policy"}${x.reason ? `: “${x.reason}”` : ""}` });
      if (x.appliedAt) events.push({ at: x.appliedAt, kind: x.status === "failed" ? "crit" : "info", text: `Fix #${x.id} ${x.status === "failed" ? "failed" : "applied"}${x.result ? `: ${x.result}` : ""}` });
      if (x.verifiedAt) events.push({ at: x.verifiedAt, kind: "good", text: `Fix #${x.id} verified: the problem is gone` });
    });
    events.sort((a, b) => new Date(b.at) - new Date(a.at));

    const f = worst(openHere);
    const mm = m || { namespace: r.ns, workload: r.name, pods: pods.length, readyPods: pods.filter((p) => p.ready).length };
    return `<div class="hero card pad"><div class="meta">${modelStatus(mm, f)}<span class="ter">${esc(r.ns)} · ${esc(m ? m.server : pods[0] && pods[0].server)} · ${mm.readyPods}/${mm.pods} ready${pods[0] && pods[0].node ? ` · on ${esc([...new Set(pods.map((p) => p.node))].join(", "))}` : ""}</span></div>
        ${f ? `<div class="callout" style="margin-top:12px"><span class="grow"><b>${esc(f.title)}</b>${f.fix && f.fix.summary ? `<span class="mut"> · ${esc(f.fix.summary)}</span>` : ""}</span><a class="btn sec" href="#/problems">Details</a></div>` : ""}</div>
      <div class="row">
        <div class="card pad kpi grow"><div class="label">Costs</div><div class="value">${money(m && m.costPerHour)}<span class="ter" style="font-size:15px">/hour</span></div><div class="sub">${money(m && m.monthForecast, 0)}/month · ${esc(((m && m.priceSources) || []).join(", ") || "no price")}${m && m.estimate ? " (estimate)" : ""}</div></div>
        <div class="card pad kpi grow"><div class="label">Per 1M output tokens</div><div class="value">${money(m && m.costPer1MOutputTokensBusy)}</div><div class="sub">while busy · ${money(m && m.costPer1MOutputTokens)} including idle time</div></div>
        <div class="card pad kpi grow"><div class="label">Throughput</div><div class="value">${num(m && m.outputTokensPerSecond)}<span class="ter" style="font-size:15px"> tok/s</span></div><div class="sub">peak ${num(m && m.peakOutputTokensPerSecond, 0)} · busy ${pct(m && m.busyShare)} of the time</div></div>
        <div class="card pad kpi grow"><div class="label">Capacity used</div><div class="value">${pct(m && m.capacityUsed)}</div><div class="sub">now vs the 7-day peak</div></div>
      </div>
      <div class="row">
        <div class="card pad grow"><div class="h2">Output tokens</div><div class="sub2" style="margin-bottom:8px">Tokens per second, all pods, last ${state.range}</div>
          ${chart({ times, series: [{ name: "tokens/s", values: pts("tok").map((p) => p.v) }], fmt: (v) => num(v) + " tok/s", label: "tokens per second" })}</div>
        <div class="card pad grow"><div class="h2">Memory</div><div class="sub2" style="margin-bottom:8px">Highest pod, GiB, against its limit</div>
          ${chart({ times, series: [{ name: "used", values: pts("mem").map((p) => gib(p.v)) }, { name: "limit", values: pts("lim").map((p) => gib(p.v)), color: "var(--crit)", dashed: true }], fmt: (v) => num(v, 2) + " GiB", label: "memory used and limit" })}</div>
        <div class="card pad grow"><div class="h2">CPU</div><div class="sub2" style="margin-bottom:8px">Cores used, all pods, against the request</div>
          ${chart({ times, series: [{ name: "used", values: pts("cpu").map((p) => p.v) }, { name: "request", values: pts("req").map((p) => p.v), color: "var(--sage)", dashed: true }], fmt: (v) => num(v, 2) + " cores", label: "CPU used and requested" })}</div>
      </div>
      <div class="card" style="overflow:hidden"><div class="cardhead"><div class="h2">Pods</div></div>
        ${pods.length ? tscroll(`<table class="table"><thead><tr><th>Pod</th><th>State</th><th>Restarts</th><th>Memory</th><th>CPU</th><th>Node</th></tr></thead><tbody>${pods.map((p) => `<tr>
          <td class="mono nowrap">${esc(p.pod)}<div class="sub2">${esc(p.container || "")}${p.revision ? " · rev " + esc(p.revision) : ""}</div></td>
          <td>${p.ready ? `<span class="chip good"><span class="dot"></span>ready</span>` : `<span class="chip warn"><span class="dot"></span>${esc(p.waitingReason || p.phase || "not ready")}</span>`}${p.lastTerminationReason ? `<div class="sub2">last exit: ${esc(p.lastTerminationReason)}</div>` : ""}</td>
          <td class="num">${p.restarts}</td>
          <td class="num">${bytes(p.memoryWorkingSetBytes)} <span class="ter">/ ${p.memoryLimitBytes ? bytes(p.memoryLimitBytes) : "no limit"}</span></td>
          <td class="num">${num(p.cpuCores, 2)} <span class="ter">/ ${num(p.cpuRequestCores, 2)} req</span></td>
          <td>${esc(p.node || "—")}<div class="sub2">${esc(p.nodeInstanceType || "")}${p.gpuRequest ? ` · ${p.gpuRequest} GPU` : ""}</div></td></tr>`).join("")}</tbody></table>`) : `<div class="empty">No pods right now.</div>`}</div>
      <div class="row">
        <div class="card pad grow" style="flex:1.4"><div class="h2">History</div><div class="sub2">Problems, fixes and approvals for this model</div>
          ${events.length ? `<ol class="tl">${events.slice(0, 30).map((e) => `<li class="${e.kind}"><span class="ter nowrap" title="${esc(new Date(e.at).toString())}">${when(e.at)}</span><span>${esc(e.text)}</span></li>`).join("")}</ol>`
            : `<div class="note" style="margin-top:10px">Nothing has happened to this model yet.</div>`}</div>
        <div class="col">
          <div class="card pad"><div class="h2">Self-hosted vs API</div>
            ${m && (m.apiComparison || []).length ? m.apiComparison.map((c) => {
              const cheaper = c.apiCostPerMonth < c.selfCostPerMonth;
              return `<div class="cmp"><div class="meta"><b>${esc(c.api)}</b>${cheaper ? `<span class="chip warn">API cheaper</span>` : `<span class="chip good">self-hosting cheaper</span>`}</div>
                <div class="kv"><span>Self-hosting</span><span class="num">${money(c.selfCostPerMonth, 2)}/month</span><span>The API, same tokens</span><span class="num">${money(c.apiCostPerMonth, 2)}/month</span><span>Break-even</span><span class="num">${num(c.breakEvenTokensPerMonth / 1e6, 1)}M tokens/month</span></div></div>`;
            }).join("") : `<div class="note" style="margin-top:8px">Add <span class="mono">referenceAPIs</span> to the pricing file to compare with APIs you know the prices of.</div>`}
            ${m && (m.notes || []).length ? m.notes.map((n) => `<div class="note" style="margin-top:8px">Note: ${esc(n)}</div>`).join("") : ""}</div>
          <div class="card pad"><div class="h2">Savings for this model</div>
            ${savingsHere.length ? `<div class="list" style="margin-top:4px">${savingsHere.map((s) => `<div><div style="display:flex;justify-content:space-between;gap:10px"><span>${esc(s.title)}</span><b class="num" style="color:var(--good);white-space:nowrap">${s.savingPerMonth > 0 ? money(s.savingPerMonth, 2) + "/mo" : "reliability"}</b></div>${s.snippet ? snip(s.snippet) : ""}</div>`).join("")}</div>`
              : `<div class="note" style="margin-top:8px">Nothing to change: requests match what it uses.</div>`}</div>
        </div></div>`;
  }

  // ---------- login ----------
  function renderLogin() {
    clearTimeout(state.timer);
    app.dataset.page = "login";
    document.title = "Sign in · Sophron";
    const lines = Array.from({ length: 9 }, (_, i) => { const y = 60 + i * 95, a = 26 + i * 3;
      return `<path d="M-20 ${y} C 190 ${y - a}, 340 ${y + a}, 530 ${y - a / 2} S 800 ${y + a / 3}, 800 ${y}" fill="none" stroke="currentColor" stroke-opacity=".16" stroke-width="1.2"/>`; }).join("");
    const me = state.me || {};
    app.innerHTML = `<div class="login"><div class="art"><svg class="lines" viewBox="0 0 760 900" preserveAspectRatio="none" aria-hidden="true">${lines}</svg>
      <div class="brand" style="position:relative;color:inherit">${logo(34)}Sophron</div>
      <h2>Every problem has a price.<br>Every fix has a receipt.</h2>
      <p>Cost, health and safe fixes for the LLMs you run on Kubernetes. Runs in your cluster; nothing leaves it.</p>
      <div class="receipt"><div style="display:flex;gap:10px;align-items:center"><span class="chip good">${icon("check", 14)}verified</span><b>Fix applied</b></div>
        <div class="kv"><span>Change</span><span>memory 512Mi → 768Mi</span><span>Approved by</span><span>an admin</span><span>Outage cost</span><span>$0.07 over 12 min</span></div></div></div>
      <div class="form"><div>
        <div class="brand mobile-only" style="padding-left:0">${logo(30)}Sophron</div>
        <div class="display" style="font-size:28px;font-weight:600">Welcome back</div>
        <div class="mut" style="margin-top:6px">Sign in to <b>${esc(me.cluster || "Sophron")}</b></div>
        ${me.authEnabled === false ? `<div class="card pad" style="margin-top:24px"><b>Sign-in isn't set up</b><div class="mut" style="margin-top:6px">No admin password exists yet, so the dashboard is read-only. Run <span class="mono">make admin-password</span> on the lab machine.</div><a class="btn sec" style="margin-top:14px" href="#/overview">Continue read-only</a></div>` : `
        <form id="loginform"><div class="field"><label for="name">Name</label><input id="name" autocomplete="username" value="admin" required></div>
        <div class="field"><label for="pw">Password</label><input id="pw" type="password" autocomplete="current-password" required></div>
        <button class="btn pri" id="signin" style="width:100%;margin-top:22px;padding:13px" type="submit">Sign in</button><div class="err" id="err" role="alert"></div></form>
        <div class="note" style="margin-top:22px;text-align:center">Admins approve fixes. Viewers can look but not change anything.</div>`}
      </div></div></div>`;
    const form = document.getElementById("loginform");
    if (!form) return;
    document.getElementById("pw").focus();
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = document.getElementById("signin"), err = document.getElementById("err");
      btn.disabled = true; btn.textContent = "Signing in…"; err.textContent = "";
      try {
        await api("/login", { method: "POST", body: JSON.stringify({ name: document.getElementById("name").value, password: document.getElementById("pw").value }) });
        state.me = null;
        app.dataset.page = "";
        if (location.hash === "#/login" || !location.hash) location.hash = "#/overview"; else render();
      } catch (x) {
        err.textContent = x.message === "signin" || /unauthor|wrong|invalid/i.test(x.message) ? "Wrong name or password" : x.message;
        btn.disabled = false; btn.textContent = "Sign in";
        document.getElementById("pw").select();
      }
    });
  }

  // ---------- start ----------
  const saved = store.get("sophron-theme");
  document.documentElement.dataset.theme = saved || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  window.addEventListener("hashchange", () => { app.dataset.page = ""; window.scrollTo(0, 0); render(); });
  document.addEventListener("visibilitychange", () => { if (!document.hidden && app.dataset.page !== "login") render(); });
  render();
})();
