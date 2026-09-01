package admin

// consoleHTML is the single-page admin console embedded into the binary.
// Layout: left sidebar navigation (仪表盘 / 渠道 / 请求轨迹 / 日志 / 设置)
// with a drawer-based channel editor. It polls /admin/api/status every 3
// seconds and talks to the admin API for the master switch, server access
// key, channel CRUD and the event feed.
const consoleHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RelayHub 管理控制台</title>
<script>
// Apply the remembered theme before first paint to avoid a flash.
(function () {
  var stored = null;
  try { stored = localStorage.getItem("consoleTheme"); } catch (e) {}
  var theme = stored || (window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
  document.documentElement.setAttribute("data-theme", theme);
})();
</script>
<style>
  :root {
    --bg: #0b0e14;
    --bg-soft: #0e121a;
    --panel: #121722;
    --panel-2: #171d2b;
    --line: #1f2635;
    --line-soft: #1a2130;
    --text: #e8ebf2;
    --muted: #7d8598;
    --dim: #555e72;
    --accent: #6366f1;
    --accent-dim: rgba(99, 102, 241, .14);
    --accent-border: rgba(99, 102, 241, .4);
    --on-accent: #ffffff;
    --green: #10b981;
    --red: #ef4444;
    --amber: #f59e0b;
    --blue: #60a5fa;
    --orange: #fb923c;
    --grid: #161c29;
    --glow: rgba(99, 102, 241, .07);
    --overlay: rgba(4, 6, 10, .6);
    --shadow: 0 24px 60px rgba(0, 0, 0, .5);
    --scroll-thumb: #2a3245;
    --track-off: #262d3d;
    --knob-off: #8b93a7;
    --dot-off: #3a4254;
    --radius: 12px;
    --mono: "Cascadia Code", "Consolas", "JetBrains Mono", ui-monospace, monospace;
    --sans: "Segoe UI Variable", "Segoe UI", "Microsoft YaHei", "PingFang SC", sans-serif;
  }
  :root[data-theme="light"] {
    --bg: #f2f4f8;
    --bg-soft: #e9edf3;
    --panel: #ffffff;
    --panel-2: #f5f7fa;
    --line: #d8deea;
    --line-soft: #e6eaf1;
    --text: #0f172a;
    --muted: #475569;
    --dim: #8b94a7;
    --accent: #4f46e5;
    --accent-dim: rgba(79, 70, 229, .09);
    --accent-border: rgba(79, 70, 229, .4);
    --on-accent: #ffffff;
    --green: #047857;
    --red: #dc2626;
    --amber: #b45309;
    --blue: #2563eb;
    --orange: #c2410c;
    --grid: rgba(100, 116, 139, .08);
    --glow: rgba(79, 70, 229, .05);
    --overlay: rgba(30, 41, 59, .35);
    --shadow: 0 24px 60px rgba(15, 23, 42, .18);
    --scroll-thumb: #c3cad6;
    --track-off: #cbd5e1;
    --knob-off: #ffffff;
    --dot-off: #b6bfce;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  html { scrollbar-color: var(--scroll-thumb) var(--bg); }
  body {
    background: var(--bg);
    color: var(--text);
    font: 14px/1.6 var(--sans);
    min-height: 100vh;
  }
  button { font-family: inherit; }
  button:focus-visible, a:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible {
    outline: 2px solid var(--accent); outline-offset: 1px;
  }
  @media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }

  /* ---------- layout: sidebar + content ---------- */
  .layout { display: flex; min-height: 100vh; }
  .sidebar {
    position: fixed; inset: 0 auto 0 0; width: 216px; z-index: 8;
    display: flex; flex-direction: column;
    background: var(--panel); border-right: 1px solid var(--line);
    padding: 18px 12px 14px;
  }
  .brand { display: flex; align-items: center; gap: 10px; padding: 2px 8px 16px; border-bottom: 1px solid var(--line-soft); }
  .brand .logo {
    width: 30px; height: 30px; border-radius: 9px; flex-shrink: 0;
    display: grid; place-items: center;
    background: var(--accent-dim); border: 1px solid var(--accent-border);
    color: var(--accent);
  }
  .brand b { font-size: 14px; letter-spacing: .04em; line-height: 1.25; }
  .brand b em { display: block; font-style: normal; color: var(--muted); font-weight: 400; font-size: 11px; }
  .nav { display: flex; flex-direction: column; gap: 2px; margin-top: 14px; }
  .nav-item {
    display: flex; align-items: center; gap: 10px;
    padding: 9px 12px; border-radius: 9px; border: 1px solid transparent;
    background: transparent; color: var(--muted); font-size: 13px;
    cursor: pointer; text-align: left; width: 100%;
    transition: color .15s, background .15s, border-color .15s;
  }
  .nav-item svg { flex-shrink: 0; opacity: .8; }
  .nav-item:hover { color: var(--text); background: var(--panel-2); }
  .nav-item.active { color: var(--accent); background: var(--accent-dim); border-color: var(--accent-border); font-weight: 600; }
  .sidebar-foot { margin-top: auto; display: flex; flex-direction: column; gap: 10px; padding-top: 14px; border-top: 1px solid var(--line-soft); }
  .side-status { display: flex; align-items: center; gap: 8px; padding: 0 8px; font-size: 12.5px; }
  .side-listen { font: 11px var(--mono); color: var(--dim); padding: 0 8px; word-break: break-all; }
  .led { width: 8px; height: 8px; border-radius: 50%; background: var(--red); box-shadow: 0 0 8px var(--red); transition: all .3s; flex-shrink: 0; }
  .led.on { background: var(--green); box-shadow: 0 0 10px var(--green); }
  .toggle {
    position: relative; width: 46px; height: 26px; border-radius: 999px;
    background: var(--track-off); cursor: pointer; transition: background .2s; border: none; flex-shrink: 0;
  }
  .toggle::after {
    content: ""; position: absolute; top: 3px; left: 3px; width: 20px; height: 20px;
    border-radius: 50%; background: var(--knob-off); transition: all .22s cubic-bezier(.4,0,.2,1);
  }
  .toggle.on { background: rgba(16,185,129,.25); }
  .toggle.on::after { left: 23px; background: var(--green); }

  .content { flex: 1; margin-left: 216px; min-width: 0; padding: 22px 26px 70px; }
  .page { display: none; max-width: 1080px; margin: 0 auto; }
  .page.active { display: block; }
  .page-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 16px; }
  .page-head h1 { font-size: 18px; font-weight: 650; letter-spacing: .02em; }
  .page-head .sub { color: var(--muted); font-size: 12.5px; margin-top: 2px; }
  @media (max-width: 760px) {
    .sidebar { width: 60px; padding: 14px 8px; }
    .brand b, .nav-item span, .side-listen, .side-status .st-text { display: none; }
    .content { margin-left: 60px; padding: 16px 14px 60px; }
  }

  .panel { background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); }
  .chip {
    font: 11px var(--mono); color: var(--muted);
    background: var(--panel); border: 1px solid var(--line);
    padding: 4px 10px; border-radius: 999px;
    max-width: 460px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }

  /* ---------- dashboard ---------- */
  .hero { display: grid; grid-template-columns: 280px 1fr; gap: 14px; margin-bottom: 14px; }
  @media (max-width: 900px) { .hero { grid-template-columns: 1fr; } }
  .switch-card { display: flex; flex-direction: column; justify-content: center; padding: 18px 20px; gap: 8px; }
  .switch-card .eyebrow { font-size: 11px; letter-spacing: .14em; color: var(--dim); text-transform: uppercase; }
  .switch-line { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .switch-title { display: flex; align-items: center; gap: 10px; font-size: 17px; font-weight: 650; }
  .listen { font: 11.5px var(--mono); color: var(--muted); }
  .recent-line { font: 11.5px var(--mono); color: var(--dim); font-variant-numeric: tabular-nums; }
  .recent-line b { color: var(--accent); font-weight: 600; }

  .stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1px; background: var(--line); overflow: hidden; }
  @media (max-width: 640px) { .stats { grid-template-columns: repeat(2, 1fr); } }
  .stat { background: var(--panel); padding: 12px 16px 10px; transition: background .2s; position: relative; }
  .stat:hover { background: var(--panel-2); }
  .stat .label { font-size: 11px; color: var(--dim); letter-spacing: .08em; text-transform: uppercase; }
  .stat .num { font: 600 21px var(--mono); margin-top: 2px; font-variant-numeric: tabular-nums; }
  .stat.ok .num { color: var(--green); }
  .stat.bad .num { color: var(--red); }
  .stat.warn .num { color: var(--amber); }
  .stat canvas { display: block; width: 100%; height: 26px; margin-top: 6px; opacity: .9; }

  /* ---------- buttons / fields (shared) ---------- */
  .btn {
    display: inline-flex; align-items: center; gap: 6px;
    border: 1px solid var(--line); background: var(--panel-2); color: var(--text);
    border-radius: 8px; padding: 6px 13px; cursor: pointer; font-size: 12.5px;
    transition: border-color .15s, color .15s, filter .15s; white-space: nowrap;
  }
  .btn:hover { border-color: var(--accent); color: var(--accent); }
  .btn:active { transform: translateY(1px); }
  .btn.primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); font-weight: 650; }
  .btn.primary:hover { filter: brightness(1.1); color: var(--on-accent); }
  .btn.danger { color: var(--red); }
  .btn.danger:hover { border-color: var(--red); color: var(--red); }
  .btn.sm { padding: 3px 10px; font-size: 12px; }
  .btn.ghost { background: transparent; }
  .hint { color: var(--muted); font-size: 11.5px; margin-top: 6px; }
  .field { margin-bottom: 13px; }
  .field label { display: block; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; color: var(--dim); margin-bottom: 6px; }
  .field input, .field select, .field textarea {
    width: 100%; background: var(--panel-2); border: 1px solid var(--line); color: var(--text);
    border-radius: 8px; padding: 8px 10px; font-size: 13px; font-family: inherit;
  }
  .field input:focus, .field textarea:focus, .field select:focus { outline: none; border-color: var(--accent); }
  .key-input { font-family: var(--mono) !important; font-size: 12.5px !important; }
  .mono-line { display: flex; align-items: stretch; }
  .mono-line input { flex: 1; border-radius: 8px 0 0 8px !important; border-right: none !important; font-family: var(--mono) !important; font-size: 12.5px !important; background: var(--bg-soft) !important; }
  .auth-badge {
    display: inline-flex; align-items: center; gap: 6px;
    font-size: 11.5px; padding: 3px 10px; border-radius: 999px; border: 1px solid var(--line);
    color: var(--muted);
  }
  .auth-badge.on { color: var(--green); border-color: rgba(16,185,129,.4); background: rgba(16,185,129,.07); }
  .auth-badge.off { color: var(--amber); border-color: rgba(245,158,11,.4); background: rgba(245,158,11,.06); }

  /* ---------- channels table ---------- */
  .section { margin-bottom: 14px; overflow: hidden; }
  .section-head { display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--line); }
  .section-head h2 { font-size: 14px; font-weight: 600; letter-spacing: .02em; }
  .section-head .meta { color: var(--dim); font-size: 12px; margin-left: 10px; font-weight: 400; }
  .toolbar { display: flex; align-items: center; gap: 8px; padding: 10px 20px; border-bottom: 1px solid var(--line-soft); flex-wrap: wrap; }
  .toolbar input, .toolbar select {
    background: var(--panel-2); border: 1px solid var(--line); color: var(--text);
    border-radius: 8px; padding: 6px 10px; font-size: 12.5px; font-family: inherit;
  }
  .toolbar input:focus, .toolbar select:focus { outline: none; border-color: var(--accent); }
  .toolbar input { flex: 1; min-width: 160px; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 11px 16px; border-bottom: 1px solid var(--line-soft); font-size: 13px; vertical-align: middle; }
  th { color: var(--dim); font-weight: 500; font-size: 11px; letter-spacing: .1em; text-transform: uppercase; }
  tr:last-child td { border-bottom: none; }
  tbody tr { transition: background .15s; }
  tbody tr:hover { background: var(--panel-2); }
  .ch-name { font-weight: 600; }
  .ch-url { font: 11px var(--mono); color: var(--dim); margin-top: 1px; word-break: break-all; }
  .tag {
    display: inline-block; padding: 2px 9px; border-radius: 6px; font-size: 11px;
    border: 1px solid var(--line); font-family: var(--mono); letter-spacing: .03em;
  }
  .tag.openai { color: var(--blue); border-color: rgba(96,165,250,.35); background: rgba(96,165,250,.07); }
  .tag.anthropic { color: var(--orange); border-color: rgba(251,146,60,.35); background: rgba(251,146,60,.07); }
  .tag.gemini { color: var(--accent); border-color: var(--accent-border); background: var(--accent-dim); }
  .models-badge { cursor: default; }
  .pill { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; }
  .pill .dot { width: 7px; height: 7px; border-radius: 50%; }
  .pill.on { color: var(--green); } .pill.on .dot { background: var(--green); box-shadow: 0 0 6px var(--green); }
  .pill.off { color: var(--dim); } .pill.off .dot { background: var(--dot-off); }
  .health { display: inline-flex; align-items: center; gap: 5px; padding: 1px 8px; border-radius: 999px; font-size: 11px; line-height: 17px; border: 1px solid var(--line); color: var(--muted); }
  .health.up { color: var(--green); border-color: rgba(16,185,129,.35); background: rgba(16,185,129,.08); }
  .health.down { color: var(--red); border-color: rgba(239,68,68,.4); background: rgba(239,68,68,.08); }
  .cool-badge { display: inline-flex; padding: 1px 8px; border-radius: 999px; font-size: 11px; line-height: 17px; color: var(--amber); border: 1px solid rgba(245,158,11,.4); background: rgba(245,158,11,.07); }
  .rate-cell { min-width: 110px; }
  .rate-line { display: flex; justify-content: space-between; font: 11px var(--mono); color: var(--muted); font-variant-numeric: tabular-nums; }
  .rate-bar { height: 4px; border-radius: 999px; background: var(--line); margin-top: 4px; overflow: hidden; }
  .rate-bar i { display: block; height: 100%; border-radius: 999px; background: var(--green); transition: width .4s; }
  .rate-bar i.mid { background: var(--amber); }
  .rate-bar i.low { background: var(--red); }
  .prio { font: 12px var(--mono); color: var(--muted); font-variant-numeric: tabular-nums; }
  .actions { white-space: nowrap; text-align: right; }
  .empty { color: var(--muted); padding: 30px 26px; text-align: center; font-size: 13px; line-height: 2; }

  /* ---------- traces ---------- */
  .traces { max-height: calc(100vh - 230px); overflow-y: auto; padding: 6px 20px 12px; }
  .trace { padding: 8px 0; border-bottom: 1px dashed var(--line-soft); font-size: 12.5px; }
  .trace:last-child { border-bottom: none; }
  .trace-head { display: flex; gap: 10px; align-items: baseline; flex-wrap: wrap; cursor: pointer; }
  .trace-head:hover .model { color: var(--accent); }
  .trace-head .t { color: var(--dim); font-family: var(--mono); font-size: 11.5px; }
  .trace-head .model { font-weight: 600; transition: color .15s; }
  .trace-head .ms { color: var(--dim); font-family: var(--mono); font-size: 11.5px; }
  .trace-head .tok { color: var(--muted); font-family: var(--mono); font-size: 11.5px; }
  .trace-hops { margin-top: 5px; display: flex; gap: 6px; align-items: center; flex-wrap: wrap; font-family: var(--mono); font-size: 11px; }
  .trace-hops .arrow { color: var(--dim); }
  .hop { display: inline-flex; gap: 6px; align-items: center; padding: 2px 8px; border-radius: 6px; border: 1px solid var(--line); background: var(--panel-2); }
  .hop .st { font-variant-numeric: tabular-nums; }
  .hop.served { border-color: var(--green); color: var(--green); }
  .hop.failed { border-color: var(--red); color: var(--red); }
  .hop.aborted { border-color: var(--amber); color: var(--amber); }
  .hop-detail { margin: 4px 0 2px 4px; padding: 6px 10px; border-left: 2px solid var(--line); color: var(--muted); font: 11.5px var(--mono); word-break: break-all; white-space: pre-wrap; }
  .status-badge { font-family: var(--mono); font-size: 11px; padding: 1px 7px; border-radius: 5px; border: 1px solid var(--line); }
  .status-badge.ok { color: var(--green); border-color: var(--green); }
  .status-badge.bad { color: var(--red); border-color: var(--red); }

  /* ---------- trace stats strip (summary of the filtered set) ---------- */
  .trace-stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-bottom: 14px; }
  @media (max-width: 720px) { .trace-stats { grid-template-columns: repeat(2, 1fr); } }
  .tstat { background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); padding: 10px 16px; }
  .tstat .label { font-size: 11px; color: var(--dim); letter-spacing: .08em; text-transform: uppercase; }
  .tstat .num { font: 600 17px var(--mono); margin-top: 2px; font-variant-numeric: tabular-nums; }

  /* ---------- trace route chain & waterfall ---------- */
  .trace { border-left: 3px solid var(--line); padding-left: 12px; margin-left: 4px; }
  .trace.ok { border-left-color: var(--green); }
  .trace.bad { border-left-color: var(--red); }
  .trace-time { color: var(--dim); font-family: var(--mono); font-size: 11.5px; font-variant-numeric: tabular-nums; }
  .tok-pill {
    display: inline-flex; align-items: center; gap: 5px;
    font: 11px var(--mono); font-variant-numeric: tabular-nums;
    color: var(--muted); border: 1px solid var(--line); border-radius: 999px; padding: 1px 9px;
  }
  .tok-pill b { color: var(--text); font-weight: 500; }
  .hop-node { display: inline-flex; align-items: center; gap: 6px; padding: 2px 9px; border-radius: 6px; border: 1px solid var(--line); background: var(--panel-2); }
  .hop-node .hdot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
  .hop-node.served { border-color: rgba(16,185,129,.5); }
  .hop-node.served .hdot { background: var(--green); box-shadow: 0 0 5px var(--green); }
  .hop-node.failed { border-color: rgba(239,68,68,.5); }
  .hop-node.failed .hdot { background: var(--red); }
  .hop-node.aborted { border-color: rgba(245,158,11,.5); }
  .hop-node.aborted .hdot { background: var(--amber); }
  .hop-node .hname { color: var(--text); font-weight: 500; }
  .hop-node .hmeta { color: var(--dim); font-variant-numeric: tabular-nums; }
  .hop-link { color: var(--dim); display: inline-flex; align-items: center; }
  .wf { margin: 8px 0 4px 2px; }
  .wf-row { display: grid; grid-template-columns: 180px 1fr 130px; gap: 10px; align-items: center; margin-bottom: 5px; font: 11.5px var(--mono); }
  .wf-name { color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .wf-track { position: relative; height: 14px; background: var(--bg-soft); border: 1px solid var(--line-soft); border-radius: 4px; overflow: hidden; }
  .wf-bar { position: absolute; top: 2px; bottom: 2px; border-radius: 3px; min-width: 3px; }
  .wf-bar.served { background: rgba(16,185,129,.55); }
  .wf-bar.failed { background: rgba(239,68,68,.55); }
  .wf-bar.aborted { background: rgba(245,158,11,.55); }
  .wf-ms { color: var(--muted); text-align: right; font-variant-numeric: tabular-nums; }
  .wf-detail { margin: 0 0 8px; padding: 6px 10px; border-left: 2px solid var(--line); color: var(--muted); font: 11.5px var(--mono); word-break: break-all; white-space: pre-wrap; }

  /* ---------- events ---------- */
  .events { max-height: calc(100vh - 230px); overflow-y: auto; padding: 6px 20px 12px; }
  .event { display: flex; gap: 12px; padding: 6px 0; border-bottom: 1px dashed var(--line-soft); font-size: 12.5px; }
  .event:last-child { border-bottom: none; }
  .event .t { color: var(--dim); flex-shrink: 0; font-family: var(--mono); font-size: 11.5px; padding-top: 1px; }
  .event .c { color: var(--accent); flex-shrink: 0; min-width: 90px; font-family: var(--mono); font-size: 11.5px; }
  .event.warn .m { color: var(--amber); }
  .event.error .m { color: var(--red); }
  .event.info .m { color: var(--text); }
  .level-filter { display: flex; gap: 4px; }
  .level-filter .btn.active { border-color: var(--accent); color: var(--accent); background: var(--accent-dim); }

  /* ---------- settings ---------- */
  .settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; align-items: start; }
  @media (max-width: 860px) { .settings-grid { grid-template-columns: 1fr; } }
  .settings-card { padding: 18px 20px; }
  .settings-card h2 { font-size: 14px; font-weight: 600; margin-bottom: 4px; }
  .settings-card .sub { color: var(--muted); font-size: 12.5px; margin-bottom: 14px; }

  /* ---------- drawer editor ---------- */
  body.drawer-open { overflow: hidden; }
  .drawer-overlay {
    position: fixed; inset: 0; z-index: 18; background: var(--overlay);
    opacity: 0; pointer-events: none; transition: opacity .2s;
  }
  .drawer-overlay.show { opacity: 1; pointer-events: auto; }
  .drawer {
    position: fixed; top: 0; right: 0; bottom: 0; z-index: 19;
    width: min(560px, 100vw);
    background: var(--bg); border-left: 1px solid var(--line);
    transform: translateX(100%); transition: transform .25s cubic-bezier(.4,0,.2,1);
    display: flex; flex-direction: column;
    box-shadow: var(--shadow);
  }
  .drawer.show { transform: translateX(0); }
  .drawer-head {
    display: flex; align-items: center; gap: 12px; flex-shrink: 0;
    padding: 14px 20px; border-bottom: 1px solid var(--line);
  }
  .drawer-head h2 { font-size: 15px; font-weight: 650; }
  .drawer-actions { margin-left: auto; display: flex; gap: 8px; }
  .drawer-body { flex: 1; overflow-y: auto; padding: 16px 20px 40px; }
  .ecard { background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); padding: 16px 18px; margin-bottom: 14px; }
  .ecard-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 12px; }
  .ecard-head h3 { font-size: 13.5px; font-weight: 650; }
  .ecard-head .meta { color: var(--dim); font-size: 12px; font-weight: 400; margin-left: 8px; }
  #fKeys { font-family: var(--mono); font-size: 12px; }

  /* ---------- model list in drawer ---------- */
  .model-toolbar { display: flex; gap: 8px; margin-bottom: 8px; }
  #modelSearch { flex: 1; min-width: 0; background: var(--panel-2); border: 1px solid var(--line); color: var(--text); border-radius: 8px; padding: 7px 10px; font-size: 12.5px; }
  #modelSearch:focus { outline: none; border-color: var(--accent); }
  .manual-add { display: flex; gap: 6px; flex-shrink: 0; }
  #modelManual { width: 180px; background: var(--panel-2); border: 1px solid var(--line); color: var(--text); border-radius: 8px; padding: 7px 9px; font-size: 12px; font-family: var(--mono); }
  #modelManual:focus { outline: none; border-color: var(--accent); }
  .model-toolbar-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
  .model-list { max-height: 300px; overflow-y: auto; border: 1px solid var(--line); border-radius: 8px; background: var(--bg-soft); }
  .model-row { display: flex; align-items: center; gap: 10px; padding: 6px 12px; border-bottom: 1px solid var(--line-soft); cursor: pointer; font-size: 12.5px; user-select: none; }
  .model-row:last-child { border-bottom: none; }
  .model-row:hover { background: var(--panel-2); }
  .model-row input { accent-color: var(--accent); width: 15px; height: 15px; cursor: pointer; flex-shrink: 0; }
  .model-row .mn { font-family: var(--mono); font-size: 12px; word-break: break-all; flex: 1; }
  .tag.fetched { color: var(--green); border-color: rgba(16,185,129,.35); background: rgba(16,185,129,.07); }
  .tag.manual { color: var(--blue); border-color: rgba(96,165,250,.35); background: rgba(96,165,250,.07); }

  /* ---------- pair row editors ---------- */
  .pair-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
  .pair-row input { flex: 1; min-width: 0; background: var(--panel-2); border: 1px solid var(--line); color: var(--text); border-radius: 8px; padding: 7px 9px; font-size: 12px; font-family: var(--mono); }
  .pair-row input:focus { outline: none; border-color: var(--accent); }
  .pair-sep { color: var(--dim); font: 12px var(--mono); flex-shrink: 0; }
  .row-del {
    flex-shrink: 0; width: 26px; height: 26px; border-radius: 7px;
    border: 1px solid var(--line); background: transparent; color: var(--dim);
    cursor: pointer; font-size: 12px; line-height: 1;
  }
  .row-del:hover { border-color: var(--red); color: var(--red); }
  .pair-empty { color: var(--dim); font-size: 12px; padding: 2px 2px 8px; }

  #toast {
    position: fixed; bottom: 26px; left: 50%; transform: translateX(-50%) translateY(8px);
    background: var(--panel-2); border: 1px solid var(--line); color: var(--text);
    padding: 9px 20px; border-radius: 10px; display: none; z-index: 30;
    box-shadow: 0 10px 30px rgba(0,0,0,.45); font-size: 13px;
    transition: transform .2s, opacity .2s; opacity: 0;
  }
  #toast.show { display: block; transform: translateX(-50%) translateY(0); opacity: 1; }
  ::-webkit-scrollbar { width: 10px; height: 10px; }
  ::-webkit-scrollbar-thumb { background: var(--scroll-thumb); border-radius: 6px; border: 2px solid var(--bg); }
  .theme-btn {
    width: 100%; display: flex; align-items: center; gap: 10px; justify-content: flex-start;
    padding: 8px 12px; border-radius: 9px;
    background: transparent; border: 1px solid var(--line); color: var(--muted);
    cursor: pointer; font-size: 12.5px; transition: all .15s;
  }
  .theme-btn:hover { border-color: var(--accent); color: var(--accent); }
</style>
</head>
<body>
<div class="layout">
  <aside class="sidebar">
    <div class="brand">
      <div class="logo" aria-hidden="true">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h7l-1 8 10-12h-7l1-8z"/></svg>
      </div>
      <b>PROXY RELAY<em>管理控制台</em></b>
    </div>
    <nav class="nav" aria-label="主导航">
      <button class="nav-item active" data-page="dashboard" onclick="switchPage('dashboard')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg>
        <span>仪表盘</span>
      </button>
      <button class="nav-item" data-page="channels" onclick="switchPage('channels')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h16"/><circle cx="9" cy="6" r="2" fill="currentColor" stroke="none"/><circle cx="15" cy="12" r="2" fill="currentColor" stroke="none"/><circle cx="7" cy="18" r="2" fill="currentColor" stroke="none"/></svg>
        <span>渠道</span>
      </button>
      <button class="nav-item" data-page="traces" onclick="switchPage('traces')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>
        <span>请求轨迹</span>
      </button>
      <button class="nav-item" data-page="logs" onclick="switchPage('logs')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h5"/></svg>
        <span>日志</span>
      </button>
      <button class="nav-item" data-page="settings" onclick="switchPage('settings')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51h.01a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
        <span>设置</span>
      </button>
    </nav>
    <div class="sidebar-foot">
      <div class="side-status">
        <span id="proxyLed" class="led" aria-hidden="true"></span>
        <span class="st-text" id="proxyState">--</span>
        <button id="proxyToggle" class="toggle" onclick="toggleProxy()" title="代理总开关" aria-label="代理总开关" style="margin-left:auto"></button>
      </div>
      <div class="side-listen" id="listenAddr">监听 --</div>
      <button id="themeBtn" class="theme-btn" onclick="toggleTheme()" aria-label="切换亮色/暗色主题"></button>
    </div>
  </aside>

  <main class="content">
    <!-- ==================== 仪表盘 ==================== -->
    <div class="page active" id="page-dashboard">
      <div class="page-head">
        <div><h1>仪表盘</h1><div class="sub">代理运行状态与流量概览 · 每 3 秒自动刷新</div></div>
        <div id="configPath" class="chip" title="本窗口读写的配置文件"></div>
      </div>
      <div class="hero">
        <div class="panel switch-card">
          <div class="eyebrow">Proxy Status</div>
          <div class="switch-line">
            <div class="switch-title"><span id="dashLed" class="led" aria-hidden="true"></span><span id="dashState">--</span></div>
          </div>
          <div id="dashListen" class="listen">监听 --</div>
          <div class="recent-line" id="recentLine" title="按最近 20 次采样（约 1 分钟）计算">最近 1 分钟：--</div>
        </div>
        <div class="stats panel">
          <div class="stat"><div class="label">运行时长</div><div class="num" id="statUptime">--</div></div>
          <div class="stat"><div class="label">总请求</div><div class="num" id="statRequests">0</div><canvas id="sparkRequests" width="220" height="26"></canvas></div>
          <div class="stat ok"><div class="label">成功响应</div><div class="num" id="statServed">0</div><canvas id="sparkServed" width="220" height="26"></canvas></div>
          <div class="stat bad"><div class="label">最终失败</div><div class="num" id="statFailed">0</div><canvas id="sparkFailed" width="220" height="26"></canvas></div>
          <div class="stat"><div class="label">Token 用量（入 / 出）</div><div class="num" id="statTokens">0 / 0</div><canvas id="sparkTokens" width="220" height="26"></canvas></div>
          <div class="stat warn"><div class="label">故障转移 / 冷却</div><div class="num" id="statFallovers">0 / 0</div><canvas id="sparkFallovers" width="220" height="26"></canvas></div>
        </div>
      </div>
      <div class="panel section">
        <div class="section-head"><h2>渠道健康一览<span class="meta">成功率按本次运行累计计算</span></h2></div>
        <div id="dashChannels"><div class="empty">加载中…</div></div>
      </div>
      <div class="panel section">
        <div class="section-head"><h2>模型用量排行<span class="meta">按请求数排序 · 最多显示 8 个</span></h2></div>
        <div id="dashModels"><div class="empty">暂无模型流量</div></div>
      </div>
    </div>

    <!-- ==================== 渠道 ==================== -->
    <div class="page" id="page-channels">
      <div class="page-head">
        <div><h1>渠道</h1><div class="sub">管理上游渠道、密钥与模型路由</div></div>
        <button class="btn primary" onclick="openChannelForm()">＋ 新增渠道</button>
      </div>
      <div class="panel section">
        <div class="toolbar">
          <input id="channelSearch" placeholder="搜索名称 / 模型 / Base URL…" oninput="renderChannels(currentStatus ? currentStatus.channels : [])" aria-label="搜索渠道">
          <select id="channelFilter" onchange="renderChannels(currentStatus ? currentStatus.channels : [])" aria-label="按状态筛选">
            <option value="">全部状态</option>
            <option value="enabled">仅启用</option>
            <option value="disabled">仅停用</option>
            <option value="down">探测不可达</option>
          </select>
        </div>
        <table>
          <thead><tr>
            <th>名称</th><th>类型</th><th>模型</th><th>优先级</th><th>成功率</th><th>状态</th><th style="text-align:right">操作</th>
          </tr></thead>
          <tbody id="channelRows"></tbody>
        </table>
      </div>
    </div>

    <!-- ==================== 请求轨迹 ==================== -->
    <div class="page" id="page-traces">
      <div class="page-head">
        <div><h1>请求轨迹</h1><div class="sub">最近 100 条 · 点击任意一行展开耗时瀑布与每一跳详情</div></div>
      </div>
      <div class="trace-stats" id="traceStats"></div>
      <div class="panel section">
        <div class="toolbar">
          <input id="traceSearch" placeholder="按模型名过滤…" oninput="renderTraces(lastTraces)" aria-label="按模型过滤轨迹">
          <select id="traceChannelFilter" onchange="renderTraces(lastTraces)" aria-label="按渠道过滤轨迹">
            <option value="">全部渠道</option>
          </select>
          <select id="traceStatusFilter" onchange="renderTraces(lastTraces)" aria-label="按结果过滤轨迹">
            <option value="">全部结果</option>
            <option value="ok">成功（&lt;400）</option>
            <option value="bad">失败（≥400）</option>
          </select>
        </div>
        <div class="traces" id="traceList"><div class="empty">暂无请求</div></div>
      </div>
    </div>

    <!-- ==================== 日志 ==================== -->
    <div class="page" id="page-logs">
      <div class="page-head">
        <div><h1>日志</h1><div class="sub">系统事件：故障转移、冷却、健康状态变化、管理操作</div></div>
        <div class="level-filter" role="group" aria-label="按级别过滤">
          <button class="btn sm active" data-level="" onclick="setLogLevel(this, '')">全部</button>
          <button class="btn sm" data-level="info" onclick="setLogLevel(this, 'info')">信息</button>
          <button class="btn sm" data-level="warn" onclick="setLogLevel(this, 'warn')">警告</button>
          <button class="btn sm" data-level="error" onclick="setLogLevel(this, 'error')">错误</button>
        </div>
      </div>
      <div class="panel section">
        <div class="events" id="eventList"><div class="empty">暂无事件</div></div>
      </div>
    </div>

    <!-- ==================== 设置 ==================== -->
    <div class="page" id="page-settings">
      <div class="page-head">
        <div><h1>设置</h1><div class="sub">接入方式与本地偏好</div></div>
      </div>
      <div class="settings-grid">
        <div class="panel settings-card">
          <h2>接入配置 <span class="auth-badge" id="authBadge">鉴权未启用</span></h2>
          <div class="sub">第三方工具（Cherry Studio / OpenAI SDK / curl …）按下面的地址接入，模型名与渠道配置一致即可。</div>
          <div class="field">
            <label for="accessBase">API Base URL</label>
            <div class="mono-line">
              <input id="accessBase" readonly tabindex="-1">
              <button class="btn" onclick="copyAccessBase()">复制</button>
            </div>
            <div class="hint">OpenAI 兼容协议；支持 /v1/chat/completions、/v1/responses、/v1/images 等端点</div>
          </div>
          <div class="field">
            <label for="serverKey">接入密钥（server.api_key）</label>
            <div class="mono-line">
              <input id="serverKey" class="key-input" type="text" placeholder="留空 = 不校验密钥" spellcheck="false">
              <button class="btn" onclick="generateServerKey()" title="随机生成一个强密钥">生成</button>
              <button class="btn primary" onclick="saveServerKey()">保存</button>
            </div>
            <div class="hint">留空表示任何连上端口的人都可调用；对外共享前建议生成一个</div>
          </div>
        </div>
        <div class="panel settings-card">
          <h2>Key 轮询策略（server.key_strategy）</h2>
          <div class="sub">多 key 渠道的默认 key 切换策略（全局生效）</div>
          <div class="field">
            <label for="keyStrategy">切换策略</label>
            <select id="keyStrategy" style="max-width:260px">
              <option value="round_robin">round_robin — 轮询切换（默认）</option>
              <option value="preferred_first">preferred_first — 优先首个 key，故障再切换</option>
            </select>
          </div>
          <div class="hint">preferred_first 持续使用第一个 key，仅在其出错/冷却时切到下一个 key，冷却结束自动回到首选 key，适合提升上游 prompt 缓存命中率。</div>
          <button class="btn primary" onclick="saveKeyStrategy()">保存策略</button>
        </div>
        <div class="panel settings-card">
          <h2>安全提示</h2>
          <div class="sub">管理台的访问控制方式</div>
          <div class="hint" style="margin-top:0">
            管理台默认仅允许本机（127.0.0.1）访问。若 <b>server.listen</b> 绑定了 0.0.0.0 且需要远程管理，
            请在 config.yaml 中设置 <b>server.admin_key</b>，远程请求携带
            <span style="font-family:var(--mono)">Authorization: Bearer &lt;admin_key&gt;</span> 访问。
            配置中的密钥支持环境变量引用（如 sk-xxxx 写为 $VAR 形式），避免明文落盘。
          </div>
        </div>
        <div class="panel settings-card">
          <h2>界面</h2>
          <div class="sub">本地显示偏好（仅影响此浏览器）</div>
          <button class="theme-btn" style="max-width:220px" onclick="toggleTheme()" aria-label="切换亮色/暗色主题"><span class="theme-label"></span></button>
        </div>
      </div>
    </div>
  </main>
</div>

<!-- ==================== 渠道编辑抽屉 ==================== -->
<div class="drawer-overlay" id="drawerOverlay" onclick="closeChannelForm()"></div>
<div class="drawer" id="channelEditor" role="dialog" aria-modal="true" aria-labelledby="editorTitle">
  <div class="drawer-head">
    <h2 id="editorTitle">新增渠道</h2>
    <div class="drawer-actions">
      <button class="btn" onclick="closeChannelForm()">取消</button>
      <button class="btn primary" id="btnSaveChannel" onclick="saveChannel()">保存</button>
    </div>
  </div>
  <div class="drawer-body">
    <div class="ecard">
      <div class="ecard-head"><h3>基本信息</h3></div>
      <div class="field"><label for="fName">名称（唯一）</label><input id="fName" placeholder="my-channel"></div>
      <div class="field"><label for="fType">类型</label>
        <select id="fType">
          <option value="openai">openai（兼容站点透传）</option>
          <option value="anthropic">anthropic（Claude 原生）</option>
          <option value="gemini">gemini（Gemini 原生）</option>
        </select>
      </div>
      <div class="field"><label for="fBase">Base URL</label><input id="fBase" placeholder="https://api.example.com"></div>
      <div class="field"><label for="fKeys">API Keys（每行一个，支持环境变量 $VAR）</label>
        <textarea id="fKeys" rows="4"></textarea>
        <div class="hint" id="keysHint"></div>
      </div>
      <div class="field"><label for="fPriority">优先级（越大越优先）</label><input id="fPriority" type="number" value="1"></div>
    </div>
    <div class="ecard">
      <div class="ecard-head">
        <h3>模型列表<span class="meta" id="modelListCount"></span></h3>
        <button class="btn sm primary" id="btnProbe" onclick="probeModels()" title="用 Base URL + Keys 调上游模型列表接口自动带出">一键获取</button>
      </div>
      <div class="hint" id="modelsHint" style="margin:0 0 10px">勾选要路由的模型；「已获取」表示来自上游模型列表。</div>
      <div class="model-toolbar">
        <input id="modelSearch" placeholder="搜索模型…" oninput="renderModelList()" aria-label="搜索模型">
        <div class="manual-add">
          <input id="modelManual" placeholder="手动添加（支持 claude-*）" onkeydown="if(event.key==='Enter'){event.preventDefault();addManualModel()}">
          <button class="btn sm" onclick="addManualModel()">添加</button>
        </div>
      </div>
      <div class="model-toolbar-row">
        <button class="btn sm ghost" onclick="modelSelectAll(true)">全选匹配</button>
        <button class="btn sm ghost" onclick="modelSelectAll(false)">取消全选</button>
      </div>
      <div class="model-list" id="modelList"></div>
    </div>
    <div class="ecard">
      <div class="ecard-head"><h3>模型名映射</h3></div>
      <div class="hint" style="margin-bottom:10px">客户端请求左栏名字，转发时改成右栏名字；留空 = 原样透传。</div>
      <div id="mapRows"></div>
      <button class="btn sm" onclick="addMapRow()">＋ 添加映射</button>
    </div>
    <div class="ecard">
      <div class="ecard-head"><h3>请求头</h3></div>
      <div class="hint" style="margin-bottom:10px">转发给该渠道上游的附加请求头；Authorization 等由代理管理，不可覆盖。</div>
      <div id="headerRows"></div>
      <button class="btn sm" onclick="addHeaderRow()">＋ 添加请求头</button>
    </div>
  </div>
</div>
<div id="toast" role="status" aria-live="polite"></div>

<script>
var currentStatus = null;
var editingChannel = null;
var lastTraces = [];
var lastEvents = [];
var logLevel = "";
var expandedTraces = {}; // index -> true while a trace's hop details are open

// ---- theme (dark is default; follows system until the user picks one) ----
function currentTheme() {
  return document.documentElement.getAttribute("data-theme") === "light" ? "light" : "dark";
}

var ICON_SUN = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M2 12h2m16 0h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>';
var ICON_MOON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>';

function applyThemeIcon() {
  var light = currentTheme() === "light";
  var html = (light ? ICON_SUN : ICON_MOON) + "<span>" + (light ? "切换到暗色" : "切换到亮色") + "</span>";
  var button = document.getElementById("themeBtn");
  if (button) button.innerHTML = html;
  document.querySelectorAll(".theme-label").forEach(function (el) {
    el.innerHTML = html;
  });
}

function toggleTheme() {
  var next = currentTheme() === "light" ? "dark" : "light";
  document.documentElement.setAttribute("data-theme", next);
  try { localStorage.setItem("consoleTheme", next); } catch (e) {}
  applyThemeIcon();
}

applyThemeIcon();

// ---- page navigation ----
function switchPage(name) {
  document.querySelectorAll(".nav-item").forEach(function (item) {
    item.classList.toggle("active", item.getAttribute("data-page") === name);
  });
  document.querySelectorAll(".page").forEach(function (page) {
    page.classList.toggle("active", page.id === "page-" + name);
  });
  try { location.hash = name; } catch (e) {}
}
(function () {
  var initial = (location.hash || "").replace("#", "");
  if (["dashboard", "channels", "traces", "logs", "settings"].indexOf(initial) >= 0) switchPage(initial);
})();

function linesToArray(text) {
  return text.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
}

function escapeHtml(text) {
  return String(text).replace(/[&<>"']/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
  });
}

function formatUptime(seconds) {
  var hours = Math.floor(seconds / 3600);
  var minutes = Math.floor((seconds % 3600) / 60);
  var secs = seconds % 60;
  if (hours > 0) return hours + "h " + minutes + "m";
  if (minutes > 0) return minutes + "m " + secs + "s";
  return secs + "s";
}

// formatCount abbreviates large numbers (12345 -> 12.3k) for compact cards.
function formatCount(value) {
  if (value >= 1e9) return (value / 1e9).toFixed(1).replace(/\.0$/, "") + "b";
  if (value >= 1e6) return (value / 1e6).toFixed(1).replace(/\.0$/, "") + "m";
  if (value >= 1e4) return (value / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
  return String(value);
}

var toastTimer = null;
function toast(message) {
  var element = document.getElementById("toast");
  element.textContent = message;
  element.classList.add("show");
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(function () { element.classList.remove("show"); }, 2200);
}

var lastRenderError = "";
// renderError surfaces console-side failures as a toast (throttled: one
// toast per distinct error) instead of swallowing them silently.
function renderError(err) {
  var message = String((err && err.message) || err);
  if (message !== lastRenderError) {
    lastRenderError = message;
    toast("渲染错误: " + message);
  }
}

function refresh() {
  fetch("/admin/api/status").then(function (r) { return r.json(); }).then(renderStatus).catch(renderError);
  fetch("/admin/api/events").then(function (r) { return r.json(); }).then(function (events) { lastEvents = events; renderEvents(events); }).catch(renderError);
  fetch("/admin/api/traces").then(function (r) { return r.json(); }).then(function (traces) { lastTraces = traces; renderTraces(traces); }).catch(renderError);
}

// ---- sparklines: per-poll deltas drawn as tiny trend lines ----
var SPARK_MAX = 20; // 20 samples * 3s = ~1 minute window
var sparkHistory = { requests: [], served: [], failed: [], tokens: [], fallovers: [] };
var prevTotals = null;

function pushSpark(key, value) {
  var series = sparkHistory[key];
  series.push(value);
  if (series.length > SPARK_MAX) series.shift();
}

// sampleSparklines records the DELTA since the previous poll, so each line
// shows "activity per 3 seconds" instead of a monotone counter ramp.
function sampleSparklines(data) {
  if (prevTotals) {
    pushSpark("requests", Math.max(0, data.total_requests - prevTotals.requests));
    pushSpark("served", Math.max(0, data.total_served - prevTotals.served));
    pushSpark("failed", Math.max(0, data.total_failed - prevTotals.failed));
    pushSpark("tokens", Math.max(0, (data.total_prompt_tokens + data.total_completion_tokens) - prevTotals.tokens));
    pushSpark("fallovers", Math.max(0, data.total_fallovers - prevTotals.fallovers));
  }
  prevTotals = {
    requests: data.total_requests,
    served: data.total_served,
    failed: data.total_failed,
    tokens: data.total_prompt_tokens + data.total_completion_tokens,
    fallovers: data.total_fallovers
  };
}

function drawSpark(canvasId, series, color) {
  var canvas = document.getElementById(canvasId);
  if (!canvas) return;
  var ctx = canvas.getContext("2d");
  var w = canvas.width, h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  if (!series || series.length < 2) return;
  var max = 1;
  for (var i = 0; i < series.length; i++) { if (series[i] > max) max = series[i]; }
  ctx.beginPath();
  for (var j = 0; j < series.length; j++) {
    var x = (j / (SPARK_MAX - 1)) * (w - 2) + 1;
    var y = h - 3 - (series[j] / max) * (h - 6);
    if (j === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.5;
  ctx.stroke();
}

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function renderSparklines() {
  drawSpark("sparkRequests", sparkHistory.requests, cssVar("--blue") || "#60a5fa");
  drawSpark("sparkServed", sparkHistory.served, cssVar("--green") || "#10b981");
  drawSpark("sparkFailed", sparkHistory.failed, cssVar("--red") || "#ef4444");
  drawSpark("sparkTokens", sparkHistory.tokens, cssVar("--accent") || "#6366f1");
  drawSpark("sparkFallovers", sparkHistory.fallovers, cssVar("--amber") || "#f59e0b");
}

// recentLine summarizes the sparkline window ("最近 1 分钟") so the operator
// sees current health, not just since-boot totals.
function renderRecentLine() {
  var reqs = sparkHistory.requests.reduce(function (a, b) { return a + b; }, 0);
  var served = sparkHistory.served.reduce(function (a, b) { return a + b; }, 0);
  var element = document.getElementById("recentLine");
  if (sparkHistory.requests.length === 0) {
    element.innerHTML = "最近 1 分钟：采样中…";
    return;
  }
  var rate = reqs > 0 ? Math.round((served / reqs) * 100) : 100;
  element.innerHTML = "最近 1 分钟：<b>" + reqs + "</b> 请求 · 成功率 <b>" + rate + "%</b>";
}

function renderStatus(data) {
  currentStatus = data;
  var enabled = !!data.enabled;
  document.getElementById("proxyToggle").classList.toggle("on", enabled);
  document.getElementById("proxyState").textContent = enabled ? "运行中" : "已停用";
  document.getElementById("proxyLed").classList.toggle("on", enabled);
  document.getElementById("dashLed").classList.toggle("on", enabled);
  document.getElementById("dashState").textContent = enabled ? "运行中" : "已停用";
  document.getElementById("statUptime").textContent = formatUptime(data.uptime_seconds);
  document.getElementById("statRequests").textContent = data.total_requests;
  document.getElementById("statServed").textContent = data.total_served;
  document.getElementById("statFailed").textContent = data.total_failed;
  var cooldownCount = (data.cooldowns || []).length;
  document.getElementById("statFallovers").textContent = data.total_fallovers + " / " + cooldownCount;
  var promptTokens = data.total_prompt_tokens || 0;
  var completionTokens = data.total_completion_tokens || 0;
  document.getElementById("statTokens").textContent =
    formatCount(promptTokens) + " / " + formatCount(completionTokens);
  document.getElementById("statTokens").title =
    "输入 " + promptTokens + " · 输出 " + completionTokens +
    " · 合计 " + (promptTokens + completionTokens) + " tokens（上游报告，累计）";
  document.getElementById("configPath").textContent = data.config_path || "";
  document.getElementById("listenAddr").textContent = "监听 " + (data.listen || "--") + " · " + location.host;
  document.getElementById("dashListen").textContent = "监听 " + (data.listen || "--") + " · " + location.host;

  // Access config (do not clobber what the user is currently typing).
  var keyInput = document.getElementById("serverKey");
  if (document.activeElement !== keyInput && keyInput.value !== (data.api_key || "")) {
    keyInput.value = data.api_key || "";
  }
  // Global key rotation strategy (do not clobber while the user is editing).
  var strategySelect = document.getElementById("keyStrategy");
  if (document.activeElement !== strategySelect) {
    strategySelect.value = data.key_strategy || "round_robin";
  }
  var badge = document.getElementById("authBadge");
  if (data.api_key) {
    badge.textContent = "鉴权已启用";
    badge.className = "auth-badge on";
  } else {
    badge.textContent = "鉴权未启用";
    badge.className = "auth-badge off";
  }

  sampleSparklines(data);
  renderSparklines();
  renderRecentLine();
  renderChannels(data.channels || []);
  renderDashChannels(data.channels || [], data.cooldowns || []);
  renderModelStats(data.models || []);
  rebuildTraceChannelFilter(data.channels || []);
}

// ---- dashboard model leaderboard (per-model requests / success / tokens) ----
function renderModelStats(models) {
  var container = document.getElementById("dashModels");
  if (!models.length) {
    container.innerHTML = '<div class="empty">暂无模型流量</div>';
    return;
  }
  var rows = models.slice(0, 8).map(function (model, index) {
    var rate = model.requests > 0 ? Math.round((model.ok / model.requests) * 100) : null;
    var cls = rate === null ? "" : (rate >= 95 ? "" : rate >= 80 ? "mid" : "low");
    return '<tr>' +
      '<td class="prio" style="width:28px">' + (index + 1) + '</td>' +
      '<td><span style="font-family:var(--mono);font-size:12px">' + escapeHtml(model.name) + '</span></td>' +
      '<td class="rate-cell"><div class="rate-line"><span>' + (rate === null ? "—" : rate + "%") + '</span><span>' +
      formatCount(model.requests) + ' 请求 · ' + formatCount(model.avg_ms) + 'ms</span></div>' +
      '<div class="rate-bar"><i class="' + cls + '" style="width:' + (rate === null ? 0 : rate) + '%"></i></div></td>' +
      '<td class="prio" style="text-align:right" title="输入 ' + model.prompt_tokens + ' · 输出 ' + model.completion_tokens + ' tokens">' +
      formatCount(model.prompt_tokens) + ' / ' + formatCount(model.completion_tokens) + ' tok</td>' +
      '</tr>';
  }).join("");
  container.innerHTML = '<table><tbody>' + rows + '</tbody></table>';
}

// ---- dashboard channel health summary (compact rows, same data as the table) ----
function renderDashChannels(channels, cooldowns) {
  var container = document.getElementById("dashChannels");
  if (!channels.length) {
    container.innerHTML = '<div class="empty">还没有渠道，到「渠道」页新增一个。</div>';
    return;
  }
  var rows = channels.map(function (channel) {
    var rate = channel.requests > 0 ? Math.round((channel.served / channel.requests) * 100) : null;
    var cls = rate === null ? "" : (rate >= 95 ? "" : rate >= 80 ? "mid" : "low");
    var rateText = rate === null ? "无流量" : rate + "% · " + channel.requests + " 请求";
    var health = "";
    if (channel.enabled && channel.health === "down") health = '<span class="health down">不可达</span>';
    else if (channel.enabled && channel.health === "up") health = '<span class="health up">正常</span>';
    var cooling = cooldowns.filter(function (c) { return c.channel === channel.name; }).length;
    var coolBadge = cooling > 0 ? ' <span class="cool-badge" title="有 key 正在冷却">' + cooling + " 冷却中</span>" : "";
    return '<tr>' +
      '<td class="ch-name">' + escapeHtml(channel.name) + '</td>' +
      '<td><span class="pill ' + (channel.enabled ? "on" : "off") + '"><span class="dot"></span>' + (channel.enabled ? "启用" : "停用") + '</span> ' + health + coolBadge + '</td>' +
      '<td class="rate-cell"><div class="rate-line"><span>' + rateText + '</span></div><div class="rate-bar"><i class="' + cls + '" style="width:' + (rate === null ? 0 : rate) + '%"></i></div></td>' +
      '</tr>';
  }).join("");
  container.innerHTML = '<table><tbody>' + rows + '</tbody></table>';
}

function accessBaseUrl() {
  return location.protocol + "//" + location.host + "/v1";
}

function copyToClipboard(text) {
  var done = function (ok) { toast(ok ? "已复制到剪贴板" : "复制失败，请手动选择复制"); };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(text).then(function () { done(true); }, function () { fallbackCopy(text, done); });
  } else {
    fallbackCopy(text, done);
  }
}

function fallbackCopy(text, done) {
  var area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.appendChild(area);
  area.select();
  var ok = false;
  try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
  document.body.removeChild(area);
  done(ok);
}

function copyAccessBase() {
  copyToClipboard(accessBaseUrl());
}

function generateServerKey() {
  var alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
  var value = "sk-proxy-";
  for (var i = 0; i < 32; i++) value += alphabet.charAt(Math.floor(Math.random() * alphabet.length));
  document.getElementById("serverKey").value = value;
  toast("已生成，点「保存」生效");
}

function saveServerKey() {
  var key = document.getElementById("serverKey").value.trim();
  fetch("/admin/api/server", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ api_key: key })
  }).then(function (response) {
    return response.json().then(function (data) { return { ok: response.ok, data: data }; });
  }).then(function (result) {
    if (!result.ok) { toast("保存失败: " + (result.data.error || "未知错误")); return; }
    toast(key ? "接入密钥已启用，第三方需携带该 key" : "已清除接入密钥（不校验）");
    refresh();
  }).catch(function (err) { toast("保存失败: " + err); });
}

// Save the global key rotation strategy (server.key_strategy).
function saveKeyStrategy() {
  var strategy = document.getElementById("keyStrategy").value;
  fetch("/admin/api/key-strategy", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ key_strategy: strategy })
  }).then(function (response) {
    return response.json().then(function (data) { return { ok: response.ok, data: data }; });
  }).then(function (result) {
    if (!result.ok) { toast("保存失败: " + (result.data.error || "未知错误")); return; }
    toast(strategy === "preferred_first" ? "已保存：固定首选 key 策略" : "已保存：轮询切换策略");
  }).catch(function (err) { toast("保存失败: " + err); });
}

// Show the access base url once (location is stable for the session).
document.getElementById("accessBase").value = accessBaseUrl();

// ---- channels table with search/filter, success-rate bar, health & cooldown badges ----
function renderChannels(channels) {
  channels = channels || [];
  var query = (document.getElementById("channelSearch").value || "").trim().toLowerCase();
  var filter = document.getElementById("channelFilter").value;
  var cooldowns = (currentStatus && currentStatus.cooldowns) || [];

  var visible = channels.filter(function (channel) {
    if (filter === "enabled" && !channel.enabled) return false;
    if (filter === "disabled" && channel.enabled) return false;
    if (filter === "down" && channel.health !== "down") return false;
    if (query) {
      var haystack = (channel.name + " " + channel.base_url + " " + (channel.models || []).join(" ")).toLowerCase();
      if (haystack.indexOf(query) < 0) return false;
    }
    return true;
  });

  var tbody = document.getElementById("channelRows");
  if (!channels.length) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty">还没有渠道，点击右上角「＋ 新增渠道」添加。<br><span style="font-size:11px">若 config.yaml 中已有渠道但这里不显示，说明本窗口连的是旧实例：请关闭所有 RelayHub 窗口后重新启动。</span></td></tr>';
    return;
  }
  if (!visible.length) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty">没有匹配筛选条件的渠道。</td></tr>';
    return;
  }
  tbody.innerHTML = visible.map(function (channel) {
    var models = channel.models || [];
    var modelsCell = models.length
      ? '<span class="tag models-badge" title="' + escapeHtml(models.join("\n")) + '">' + models.length + ' 个模型</span> ' +
        '<span style="color:var(--muted);font-size:12px">' + models.slice(0, 2).map(escapeHtml).join(", ") + (models.length > 2 ? " …" : "") + "</span>"
      : '<span style="color:var(--dim)">未配置</span>';

    var healthCell = "";
    if (channel.enabled) {
      if (channel.health === "down") {
        healthCell = '<span class="health down" title="健康探测失败：路由已跳过该渠道">不可达</span>';
      } else if (channel.health === "up") {
        healthCell = '<span class="health up" title="健康探测成功">正常</span>';
      }
    }
    var cooling = cooldowns.filter(function (c) { return c.channel === channel.name; }).length;
    var coolBadge = cooling > 0 ? ' <span class="cool-badge" title="' + cooling + ' 个 key 正在冷却">' + cooling + " 冷却</span>" : "";

    var rate = channel.requests > 0 ? Math.round((channel.served / channel.requests) * 100) : null;
    var rateCls = rate === null ? "" : (rate >= 95 ? "" : rate >= 80 ? "mid" : "low");
    var rateTitle = "请求 " + channel.requests + " · 成功 " + channel.served + " · 失败 " + channel.failed +
      " · 输入 " + (channel.prompt_tokens || 0) + " 输出 " + (channel.completion_tokens || 0) + " tokens";
    var latLine = (channel.p50_ms > 0 || channel.p95_ms > 0)
      ? '<div class="rate-line" style="margin-top:3px;color:var(--dim)"><span>p50 ' + formatCount(channel.p50_ms) + "ms · p95 " + formatCount(channel.p95_ms) + "ms</span></div>"
      : "";
    var rateCell = '<div class="rate-line" title="' + escapeHtml(rateTitle) + '"><span>' +
      (rate === null ? "无流量" : rate + "%") + '</span><span>' + formatCount(channel.requests) + " 请求</span></div>" +
      '<div class="rate-bar"><i class="' + rateCls + '" style="width:' + (rate === null ? 0 : rate) + '%"></i></div>' + latLine;

    var stateCell = '<span class="pill ' + (channel.enabled ? "on" : "off") + '"><span class="dot"></span>' +
      (channel.enabled ? "启用" : "停用") + "</span> " + healthCell + coolBadge;

    var safeName = escapeHtml(channel.name);
    var actions =
      '<button class="btn sm ' + (channel.enabled ? "" : "primary") + '" onclick="toggleChannel(\'' + safeName + '\',' + !channel.enabled + ')">' + (channel.enabled ? "停用" : "启用") + '</button> ' +
      '<button class="btn sm" onclick="openChannelForm(\'' + safeName + '\')">编辑</button> ' +
      '<button class="btn sm danger" onclick="deleteChannel(\'' + safeName + '\')">删除</button>';
    return "<tr>" +
      '<td><div class="ch-name">' + safeName + '</div><div class="ch-url">' + escapeHtml(channel.base_url) + "</div></td>" +
      '<td><span class="tag ' + escapeHtml(channel.type) + '">' + escapeHtml(channel.type) + "</span></td>" +
      "<td>" + modelsCell + "</td>" +
      '<td class="prio">' + channel.priority + "</td>" +
      '<td class="rate-cell">' + rateCell + "</td>" +
      "<td>" + stateCell + "</td>" +
      '<td class="actions">' + actions + "</td>" +
      "</tr>";
  }).join("");
}

function renderEvents(events) {
  var list = document.getElementById("eventList");
  var filtered = (events || []).filter(function (event) { return !logLevel || event.level === logLevel; });
  if (!filtered.length) {
    list.innerHTML = '<div class="empty">' + ((events || []).length ? "该级别暂无事件" : "暂无事件") + "</div>";
    return;
  }
  list.innerHTML = filtered.map(function (event) {
    return '<div class="event ' + escapeHtml(event.level) + '">' +
      '<span class="t">' + escapeHtml(event.time) + "</span>" +
      '<span class="c">' + escapeHtml(event.channel || "-") + "</span>" +
      '<span class="m">' + escapeHtml(event.message) + "</span></div>";
  }).join("");
}

function setLogLevel(button, level) {
  logLevel = level;
  document.querySelectorAll(".level-filter .btn").forEach(function (b) { b.classList.remove("active"); });
  button.classList.add("active");
  renderEvents(lastEvents);
}

// ---- request traces: filters + expandable per-hop details ----
function rebuildTraceChannelFilter(channels) {
  var select = document.getElementById("traceChannelFilter");
  var current = select.value;
  var names = channels.map(function (c) { return c.name; });
  var existing = Array.prototype.map.call(select.options, function (o) { return o.value; }).slice(1);
  if (JSON.stringify(names) === JSON.stringify(existing)) return;
  select.innerHTML = '<option value="">全部渠道</option>' + names.map(function (name) {
    return '<option value="' + escapeHtml(name) + '">' + escapeHtml(name) + "</option>";
  }).join("");
  select.value = names.indexOf(current) >= 0 ? current : "";
}

function toggleTraceDetail(index) {
  expandedTraces[index] = !expandedTraces[index];
  renderTraces(lastTraces);
}

function renderTraces(traces) {
  var list = document.getElementById("traceList");
  var query = (document.getElementById("traceSearch").value || "").trim().toLowerCase();
  var channelFilter = document.getElementById("traceChannelFilter").value;
  var statusFilter = document.getElementById("traceStatusFilter").value;

  var filtered = (traces || []).filter(function (trace) {
    if (query && trace.model.toLowerCase().indexOf(query) < 0) return false;
    if (channelFilter) {
      var hit = trace.final_channel === channelFilter ||
        (trace.hops || []).some(function (hop) { return hop.channel === channelFilter; });
      if (!hit) return false;
    }
    if (statusFilter === "ok" && trace.final_status >= 400) return false;
    if (statusFilter === "bad" && trace.final_status < 400) return false;
    return true;
  });

  renderTraceStats(filtered);

  if (!filtered.length) {
    list.innerHTML = '<div class="empty">' + ((traces || []).length ? "没有匹配筛选条件的请求" : "暂无请求") + "</div>";
    return;
  }
  list.innerHTML = filtered.slice(0, 100).map(function (trace) {
    var index = lastTraces.indexOf(trace);
    var ok = trace.final_status < 400;
    var badge = ok
      ? '<span class="status-badge ok">' + trace.final_status + "</span>"
      : '<span class="status-badge bad">' + trace.final_status + "</span>";
    var hops = trace.hops || [];

    // Route chain: ● channelA ✗ ──▶ ● channelB ✓ (the request's full path).
    var routeHtml = hops.map(function (hop, i) {
      var link = i === 0 ? "" : '<span class="hop-link">─▶</span>';
      return link +
        '<span class="hop-node ' + escapeHtml(hop.result) + '" title="' + escapeHtml(hop.detail || hop.result) + '">' +
        '<span class="hdot"></span>' +
        '<span class="hname">' + escapeHtml(hop.channel) + "</span>" +
        '<span class="hmeta">' + (hop.status || "—") + " · " + hop.duration_ms + "ms</span>" +
        "</span>";
    }).join("");
    if (!routeHtml) routeHtml = '<span style="color:var(--dim);font-size:11px">未进入上游（无可用渠道）</span>';

    // Token usage pill: in → out (only when the upstream reported usage).
    var hasTokens = (trace.prompt_tokens + trace.completion_tokens) > 0;
    var tokenPill = hasTokens
      ? '<span class="tok-pill" title="输入 ' + trace.prompt_tokens + " · 输出 " + trace.completion_tokens + ' tokens">' +
        "tok <b>" + formatCount(trace.prompt_tokens) + " → " + formatCount(trace.completion_tokens) + "</b></span>"
      : "";

    // Expanded view: a duration waterfall (each hop positioned by its start
    // offset inside the request) plus per-hop error details.
    var detailHtml = "";
    if (expandedTraces[index] && hops.length) {
      var total = Math.max(trace.total_ms, 1);
      var offset = 0;
      detailHtml = '<div class="wf">' + hops.map(function (hop) {
        var left = Math.min(99, (offset / total) * 100);
        var width = Math.max(1.5, (hop.duration_ms / total) * 100);
        if (left + width > 100) width = 100 - left;
        offset += hop.duration_ms;
        return '<div class="wf-row">' +
          '<span class="wf-name" title="' + escapeHtml(hop.channel) + '">' + escapeHtml(hop.channel) +
          (hop.key_tail ? " ···" + escapeHtml(hop.key_tail) : "") + "</span>" +
          '<span class="wf-track"><span class="wf-bar ' + escapeHtml(hop.result) + '" style="left:' + left.toFixed(1) + "%;width:" + width.toFixed(1) + '%"></span></span>' +
          '<span class="wf-ms">' + hop.duration_ms + "ms · " + escapeHtml(hop.result) + " " + (hop.status || "—") + "</span>" +
          "</div>";
      }).join("") + "</div>" +
      hops.filter(function (hop) { return hop.detail; }).map(function (hop) {
        return '<div class="wf-detail">[' + escapeHtml(hop.channel) + "] " + escapeHtml(hop.detail) + "</div>";
      }).join("");
    }

    return '<div class="trace ' + (ok ? "ok" : "bad") + '">' +
      '<div class="trace-head" onclick="toggleTraceDetail(' + index + ')" title="点击展开/收起耗时瀑布与详情">' +
      '<span class="trace-time">' + escapeHtml(trace.time) + "</span>" +
      '<span class="model">' + escapeHtml(trace.model) + "</span>" +
      (trace.stream ? ' <span class="tok">stream</span>' : "") +
      tokenPill +
      '<span style="flex:1"></span>' +
      badge +
      ' <span class="ms">' + trace.total_ms + "ms</span>" +
      "</div>" +
      '<div class="trace-hops">' + routeHtml + "</div>" +
      detailHtml +
      "</div>";
  }).join("");
}

// renderTraceStats summarizes the FILTERED trace set so the operator can
// answer "how is traffic doing right now" without scanning every row.
function renderTraceStats(traces) {
  var container = document.getElementById("traceStats");
  var total = traces.length;
  if (!total) {
    container.innerHTML = "";
    return;
  }
  var okCount = traces.filter(function (t) { return t.final_status < 400; }).length;
  var avgMs = Math.round(traces.reduce(function (a, t) { return a + t.total_ms; }, 0) / total);
  var promptSum = traces.reduce(function (a, t) { return a + t.prompt_tokens; }, 0);
  var completionSum = traces.reduce(function (a, t) { return a + t.completion_tokens; }, 0);
  var rate = Math.round((okCount / total) * 100);
  container.innerHTML =
    '<div class="tstat"><div class="label">样本请求</div><div class="num">' + total + "</div></div>" +
    '<div class="tstat"><div class="label">成功率</div><div class="num" style="color:' + (rate >= 95 ? "var(--green)" : rate >= 80 ? "var(--amber)" : "var(--red)") + '">' + rate + "%</div></div>" +
    '<div class="tstat"><div class="label">平均耗时</div><div class="num">' + formatCount(avgMs) + "ms</div></div>" +
    '<div class="tstat"><div class="label">Token 入 / 出</div><div class="num">' + formatCount(promptSum) + " / " + formatCount(completionSum) + "</div></div>";
}

function apiPost(url, payload) {
  return fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload || {})
  }).then(function (response) { return response.json().then(function (data) { return { ok: response.ok, data: data }; }); });
}

function toggleProxy() {
  var nextState = !currentStatus.enabled;
  apiPost("/admin/api/proxy/enable", { enabled: nextState }).then(function (result) {
    if (!result.ok) { toast("操作失败: " + (result.data.error || "未知错误")); return; }
    toast(nextState ? "代理已开启" : "代理已停用");
    refresh();
  });
}

function toggleChannel(name, enabled) {
  apiPost("/admin/api/channels/" + encodeURIComponent(name) + "/toggle", { enabled: enabled }).then(function (result) {
    if (!result.ok) { toast("操作失败: " + (result.data.error || "未知错误")); return; }
    refresh();
  });
}

function deleteChannel(name) {
  if (!confirm("确定删除渠道 " + name + " 吗？该操作会立即写回 config.yaml。")) return;
  fetch("/admin/api/channels/" + encodeURIComponent(name), { method: "DELETE" })
    .then(function (response) { return response.json(); })
    .then(function () { toast("已删除"); refresh(); });
}

// ---- channel editor drawer ----
function openChannelForm(name) {
  editingChannel = name || null;
  document.getElementById("editorTitle").textContent = name ? "编辑渠道 · " + name : "新增渠道";
  var nameInput = document.getElementById("fName");
  var keysArea = document.getElementById("fKeys");
  var hint = document.getElementById("keysHint");

  if (name && currentStatus) {
    var channel = currentStatus.channels.find(function (c) { return c.name === name; });
    if (!channel) return;
    nameInput.value = channel.name;
    nameInput.readOnly = true;
    document.getElementById("fType").value = channel.type;
    document.getElementById("fBase").value = channel.base_url;
    setModelRows(channel.models || []);
    renderPairRows("mapRows", Object.keys(channel.model_map || {}).map(function (k) { return [k, channel.model_map[k]]; }));
    renderPairRows("headerRows", Object.keys(channel.headers || {}).map(function (k) { return [k, channel.headers[k]]; }));
    document.getElementById("fPriority").value = channel.priority;
    hint.textContent = "正在读取已保存的 API Key…";
    // This is a LOCAL admin tool: repopulate the real (unmasked) keys so
    // the user can see/edit them and "one-click fetch" works in edit mode.
    fetch("/admin/api/channels/" + encodeURIComponent(name) + "/keys")
      .then(function (response) { return response.json().then(function (data) { return { ok: response.ok, data: data }; }); })
      .then(function (result) {
        if (!result.ok) {
          hint.textContent = "无法读取已保存的 key：" + (result.data.error || "未知错误");
          keysArea.value = "";
          return;
        }
        keysArea.value = (result.data.api_keys || []).join("\n");
        hint.textContent = (result.data.api_keys || []).length > 0
          ? "已加载保存的 API Key，可直接修改；保存时以这里的内容为准"
          : "该渠道尚未配置 API Key";
      })
      .catch(function (err) { hint.textContent = "读取 key 失败：" + err; });
  } else {
    nameInput.value = "";
    nameInput.readOnly = false;
    document.getElementById("fType").value = "openai";
    document.getElementById("fBase").value = "";
    keysArea.value = "";
    keysArea.placeholder = "sk-xxxx，每行一个";
    setModelRows([]);
    renderPairRows("mapRows", []);
    renderPairRows("headerRows", []);
    document.getElementById("fPriority").value = "1";
    hint.textContent = "";
  }
  document.getElementById("modelSearch").value = "";
  document.getElementById("modelManual").value = "";
  renderModelList();
  setProbeState("勾选要路由的模型；「一键获取」按 Base URL + Keys 从上游拉取模型列表。", false);
  document.body.classList.add("drawer-open");
  document.getElementById("drawerOverlay").classList.add("show");
  document.getElementById("channelEditor").classList.add("show");
}

function closeChannelForm() {
  document.getElementById("channelEditor").classList.remove("show");
  document.getElementById("drawerOverlay").classList.remove("show");
  document.body.classList.remove("drawer-open");
}

function setProbeState(text, busy) {
  var hint = document.getElementById("modelsHint");
  var button = document.getElementById("btnProbe");
  hint.textContent = text;
  hint.style.color = busy ? "var(--accent)" : "";
  button.disabled = busy;
  button.textContent = busy ? "获取中…" : "一键获取";
}

// ---- model list: inline editor (search + checkbox + manual add) ----
// modelRows is the single source of truth for the channel's model list while
// the editor is open. "fetched" marks rows that came from the upstream probe,
// so the UI can show where each model name came from.
var modelRows = []; // { name, checked, fetched }

// Per-container layout for the two pair row editors (model map / headers).
var pairConfigs = {
  mapRows: { keyPh: "客户端模型名", valPh: "上游模型名", sep: "→", emptyText: "暂无映射：模型名原样透传" },
  headerRows: { keyPh: "请求头名（如 X-Title）", valPh: "请求头的值", sep: ":", emptyText: "暂无附加请求头" }
};

function setModelRows(names) {
  modelRows = (names || []).map(function (n) { return { name: n, checked: true, fetched: false }; });
}

function modelQuery() {
  return document.getElementById("modelSearch").value.trim().toLowerCase();
}

function modelMatches(row) {
  var q = modelQuery();
  return !q || row.name.toLowerCase().indexOf(q) >= 0;
}

function renderModelList() {
  var html = "";
  modelRows.forEach(function (row, index) {
    if (!modelMatches(row)) return;
    html += '<label class="model-row">' +
      '<input type="checkbox" data-i="' + index + '"' + (row.checked ? " checked" : "") + ' onchange="modelToggle(this)">' +
      '<span class="mn">' + escapeHtml(row.name) + "</span>" +
      '<span class="tag ' + (row.fetched ? "fetched" : "manual") + '">' + (row.fetched ? "已获取" : "手动") + "</span>" +
      "</label>";
  });
  document.getElementById("modelList").innerHTML =
    html || '<div class="empty">' +
      (modelRows.length ? "没有匹配的模型" : "暂无模型：点「一键获取」从上游拉取，或在上方手动添加") +
      "</div>";
  updateModelCount();
}

function updateModelCount() {
  var checked = modelRows.filter(function (r) { return r.checked; }).length;
  document.getElementById("modelListCount").textContent = modelRows.length
    ? checked + " / " + modelRows.length + " 已选"
    : "";
}

// modelToggle only updates state + the counter; the list is deliberately NOT
// re-rendered (that would cancel the click and break keyboard navigation).
function modelToggle(input) {
  var row = modelRows[parseInt(input.getAttribute("data-i"), 10)];
  if (row) {
    row.checked = input.checked;
    updateModelCount();
  }
}

function modelSelectAll(select) {
  modelRows.forEach(function (row) {
    if (modelMatches(row)) row.checked = select;
  });
  renderModelList();
}

function addManualModel() {
  var input = document.getElementById("modelManual");
  var name = input.value.trim();
  if (!name) return;
  var existing = modelRows.find(function (r) { return r.name === name; });
  if (existing) {
    existing.checked = true;
    toast("模型已在列表中，已勾选");
  } else {
    modelRows.push({ name: name, checked: true, fetched: false });
  }
  input.value = "";
  renderModelList();
  input.focus();
}

// ---- pair row editors: model map (client → upstream) & headers (Name: value) ----
function appendPairRow(container, cfg, keyVal, valVal) {
  var row = document.createElement("div");
  row.className = "pair-row";
  row.innerHTML =
    '<input class="pair-key" spellcheck="false">' +
    '<span class="pair-sep">' + cfg.sep + "</span>" +
    '<input class="pair-val" spellcheck="false">' +
    '<button type="button" class="row-del" title="删除该行" aria-label="删除该行">✕</button>';
  var keyInput = row.querySelector(".pair-key");
  var valInput = row.querySelector(".pair-val");
  keyInput.placeholder = cfg.keyPh;
  valInput.placeholder = cfg.valPh;
  keyInput.value = keyVal;
  valInput.value = valVal;
  row.querySelector(".row-del").onclick = function () {
    row.remove();
    if (!container.querySelector(".pair-row")) {
      var empty = document.createElement("div");
      empty.className = "pair-empty";
      empty.textContent = cfg.emptyText;
      container.appendChild(empty);
    }
  };
  container.appendChild(row);
  return row;
}

function renderPairRows(containerId, entries) {
  var cfg = pairConfigs[containerId];
  var container = document.getElementById(containerId);
  container.innerHTML = "";
  (entries || []).forEach(function (entry) { appendPairRow(container, cfg, entry[0], entry[1]); });
  if (!container.querySelector(".pair-row")) {
    var empty = document.createElement("div");
    empty.className = "pair-empty";
    empty.textContent = cfg.emptyText;
    container.appendChild(empty);
  }
}

function addPairRow(containerId) {
  var cfg = pairConfigs[containerId];
  var container = document.getElementById(containerId);
  var empty = container.querySelector(".pair-empty");
  if (empty) empty.remove();
  var row = appendPairRow(container, cfg, "", "");
  row.querySelector(".pair-key").focus();
}

function addMapRow() { addPairRow("mapRows"); }
function addHeaderRow() { addPairRow("headerRows"); }

// collectPairRows reads the current rows as [key, value] pairs; fully blank
// rows are skipped silently, half-empty rows are kept for the caller to
// report as a validation error.
function collectPairRows(containerId) {
  var entries = [];
  var rows = document.getElementById(containerId).querySelectorAll(".pair-row");
  for (var i = 0; i < rows.length; i++) {
    var inputs = rows[i].querySelectorAll("input");
    var key = inputs[0].value.trim();
    var value = inputs[1].value.trim();
    if (!key && !value) continue;
    entries.push([key, value]);
  }
  return entries;
}

// probeModels calls the upstream's model-list API with the form's
// base_url + keys and merges the result into the inline model list so the
// user only ticks the models they actually want.
function probeModels() {
  var type = document.getElementById("fType").value;
  var base = document.getElementById("fBase").value.trim();
  var keys = linesToArray(document.getElementById("fKeys").value);
  if (!base) { setProbeState("先填写 Base URL", false); return; }
  // Keys may be empty in edit mode: the server falls back to the keys
  // already stored for this channel (editingChannel holds its name).

  setProbeState("正在向 " + base + " 查询模型列表…", true);
  fetch("/admin/api/probe-models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ channel: editingChannel || "", type: type, base_url: base, api_keys: keys })
  }).then(function (response) {
    return response.json().then(function (data) { return { ok: response.ok, data: data }; });
  }).then(function (result) {
    if (!result.ok) {
      setProbeState("获取失败: " + (result.data.error || "未知错误"), false);
      return;
    }
    var fetched = result.data.models || [];
    if (fetched.length === 0) {
      setProbeState("上游返回了 0 个模型，请手动填写", false);
      return;
    }
    // Merge the fetched models into the inline list: rows that were already
    // configured keep their state, new rows appear unchecked so the user
    // only ticks what they actually want.
    var added = 0, kept = 0;
    fetched.forEach(function (m) {
      var existing = modelRows.find(function (r) { return r.name === m; });
      if (existing) {
        existing.fetched = true;
        if (existing.checked) kept++;
      } else {
        modelRows.push({ name: m, checked: false, fetched: true });
        added++;
      }
    });
    renderModelList();
    setProbeState("已带出 " + fetched.length + " 个模型（新增 " + added + " 个，已勾选 " + kept + " 个），请勾选要路由的", false);
  }).catch(function (err) {
    setProbeState("获取失败: " + err, false);
  });
}

function saveChannel() {
  // Validate the pair row editors before building the payload.
  var modelMap = {};
  var mapEntries = collectPairRows("mapRows");
  for (var mi = 0; mi < mapEntries.length; mi++) {
    var mk = mapEntries[mi][0], mv = mapEntries[mi][1];
    if (!mk) { toast("模型名映射：存在未填写客户端名的行"); return; }
    if (!mv) { toast("模型名映射：「" + mk + "」缺少上游模型名"); return; }
    modelMap[mk] = mv;
  }
  var headers = {};
  var headerEntries = collectPairRows("headerRows");
  for (var hi = 0; hi < headerEntries.length; hi++) {
    var hk = headerEntries[hi][0], hv = headerEntries[hi][1];
    if (!hk) { toast("请求头：存在未填写头名的行"); return; }
    if (hk.toLowerCase() === "authorization") { toast("Authorization 由 API Keys 管理，不能写在请求头里"); return; }
    headers[hk.toLowerCase()] = hv;
  }

  var payload = {
    name: document.getElementById("fName").value.trim(),
    type: document.getElementById("fType").value,
    base_url: document.getElementById("fBase").value.trim(),
    api_keys: linesToArray(document.getElementById("fKeys").value),
    models: modelRows.filter(function (r) { return r.checked; }).map(function (r) { return r.name; }),
    model_map: modelMap,
    headers: headers,
    priority: parseInt(document.getElementById("fPriority").value, 10) || 0
  };
  if (!payload.name) { toast("名称不能为空"); return; }

  var url = editingChannel
    ? "/admin/api/channels/" + encodeURIComponent(editingChannel)
    : "/admin/api/channels";
  var method = editingChannel ? "PUT" : "POST";
  var saveButton = document.getElementById("btnSaveChannel");
  saveButton.disabled = true;

  fetch(url, {
    method: method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  }).then(function (response) {
    return response.json().then(function (data) { return { ok: response.ok, data: data }; });
  }).then(function (result) {
    saveButton.disabled = false;
    if (!result.ok) { toast("保存失败: " + (result.data.error || "未知错误")); return; }
    toast("已保存");
    closeChannelForm();
    refresh();
  }).catch(function (err) {
    saveButton.disabled = false;
    toast("保存失败: " + err);
  });
}

// Escape closes the drawer without saving.
document.addEventListener("keydown", function (e) {
  if (e.key === "Escape") {
    var drawer = document.getElementById("channelEditor");
    if (drawer && drawer.classList.contains("show")) closeChannelForm();
  }
});

refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`
