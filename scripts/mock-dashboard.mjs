import http from "node:http";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const port = Number(process.argv[2] || 43191);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../internal/web/static");
const now = new Date();
const usage = { input: 8_740_000, cached_input: 5_210_000, cache_write_input: 240_000, output: 1_430_000, reasoning_output: 620_000, total: 10_170_000 };
const items = [
  { key: "gpt-5.4", usage: { ...usage, total: 6_420_000 }, events: 71, sessions: 15 },
  { key: "gpt-5.5-codex", usage: { ...usage, total: 2_810_000 }, events: 32, sessions: 8 },
  { key: "gpt-5.3", usage: { ...usage, total: 940_000 }, events: 11, sessions: 3 }
];
const agents = [
  { key: "main", usage: { ...usage, total: 7_600_000 }, events: 80, sessions: 20 },
  { key: "subagent", usage: { ...usage, total: 1_700_000 }, events: 21, sessions: 6 },
  { key: "guardian", usage: { ...usage, total: 570_000 }, events: 8, sessions: 4 },
  { key: "memory", usage: { ...usage, total: 300_000 }, events: 5, sessions: 2 }
];

function json(response, value, status = 200) {
  response.writeHead(status, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" });
  response.end(JSON.stringify(value));
}

function summary(multiplier = 1) {
  const scaled = Object.fromEntries(Object.entries(usage).map(([key, value]) => [key, Math.round(value * multiplier)]));
  return { usage: scaled, unattributed: { input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0, total: 84_200 }, grand_total: scaled.total + 84_200, event_count: 114, session_count: 26, coverage_incomplete: true };
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, `http://127.0.0.1:${port}`);
  if (url.pathname === "/api/v1/status") return json(response, {
    version: "0.1.0-preview", scanning: false,
    status: {
      machine: { id: "62c0172d-36c4-4ec9-a074-02b9ec2b45e1", label: "WORKSTATION-19 · windows", hostname: "WORKSTATION-19", os: "windows", arch: "amd64" },
      last_scan: now.toISOString(), otel_last_received: now.toISOString(), otel_active: true,
      event_count: 114, session_count: 26, warning_count: 2,
      codex_homes: [{ path: "C:\\Users\\demo\\.codex", last_scan: now.toISOString(), files_scanned: 26 }]
    }
  });
  if (url.pathname === "/api/v1/summary") {
    const since = url.searchParams.get("since");
    return json(response, summary(since === "today" ? .11 : since === "7d" ? .38 : since === "30d" ? .76 : 1));
  }
  if (url.pathname === "/api/v1/timeseries") {
    const points = Array.from({ length: 30 }, (_, index) => {
      const time = new Date(now.getTime() - (29 - index) * 86400000);
      const wave = 150_000 + Math.round((Math.sin(index * .72) + 1.35) * 145_000) + index * 8500;
      return { time: time.toISOString(), usage: { input: Math.round(wave * .78), cached_input: Math.round(wave * .42), cache_write_input: 0, output: Math.round(wave * .22), reasoning_output: Math.round(wave * .08), total: wave } };
    });
    return json(response, { bucket: "day", points });
  }
  if (url.pathname === "/api/v1/breakdown") {
    const dimension = url.searchParams.get("dimension");
    if (dimension === "agent_type") return json(response, { dimension, items: agents });
    if (dimension === "source") return json(response, { dimension, items: [{ key: "codex_desktop", usage, events: 70, sessions: 16 }, { key: "codex_cli_rs", usage: { ...usage, total: 2_100_000 }, events: 44, sessions: 10 }] });
    if (dimension === "project") return json(response, { dimension, items: [{ key: "C:\\dev\\render-lab", usage, events: 70, sessions: 12 }, { key: "/srv/inference", usage: { ...usage, total: 2_100_000 }, events: 44, sessions: 8 }] });
    if (dimension === "thread") return json(response, { dimension, items: [{ key: "Turntable occlusion refinement", usage, events: 12, sessions: 1 }, { key: "Linux batch migration", usage: { ...usage, total: 2_100_000 }, events: 9, sessions: 1 }] });
    return json(response, { dimension: "model", items });
  }
  if (url.pathname === "/api/v1/sessions") return json(response, { items: [
    { session_id: "019fb24a-f4dd-7673-9b7d-225d26f2b141", title: "实现逐电脑 Token 统计与可视化", project_path: "C:\\dev\\codex-meter", model: "gpt-5.4", source: "codex_desktop", agent_type: "main", usage: { ...usage, total: 2_920_000 }, confidence: "exact", last_usage: now.toISOString() },
    { session_id: "019fa142-1051-72bd-aa0f-975efd2bf6c2", title: "Linux 渲染任务诊断", project_path: "/srv/inference/render", model: "gpt-5.5-codex", source: "codex_cli_rs", agent_type: "subagent", usage: { ...usage, total: 1_180_000 }, confidence: "gap_fallback", last_usage: new Date(now.getTime() - 3600000).toISOString() },
    { session_id: "019f9821-d272-7812-814d-94cc02dc39a1", title: "本地数据管线检查", project_path: "C:\\dev\\private-data", model: "gpt-5.4", source: "codex_desktop", agent_type: "guardian", usage: { ...usage, total: 760_000 }, confidence: "exact", last_usage: new Date(now.getTime() - 7200000).toISOString() }
  ] });
  if (url.pathname === "/api/v1/warnings") return json(response, { items: [
    { created_at: now.toISOString(), kind: "state_fallback", path: "state_5.sqlite", detail: "84,200 Token 只能归属到历史累计，未分摊日期" },
    { created_at: now.toISOString(), kind: "cumulative_reset", path: "rollout-example.jsonl", detail: "累计向量回退，已使用 last_token_usage 补位" }
  ] });
  if (url.pathname === "/api/v1/rescan") return json(response, { homes: 1, files: 26, records: 940, events_inserted: 2, duplicates: 14, warnings: 0 });
  if (url.pathname === "/api/v1/export") {
    response.writeHead(200, { "Content-Type": "application/json" });
    return response.end("[]");
  }
  const relative = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
  const file = path.resolve(root, relative);
  if (!file.startsWith(root)) {
    response.writeHead(403);
    return response.end();
  }
  try {
    const content = await readFile(file);
    const type = file.endsWith(".css") ? "text/css" : file.endsWith(".js") ? "text/javascript" : "text/html";
    response.writeHead(200, { "Content-Type": `${type}; charset=utf-8` });
    response.end(content);
  } catch {
    response.writeHead(404);
    response.end("Not Found");
  }
});

server.listen(port, "127.0.0.1", () => console.log(`Mock Dashboard: http://127.0.0.1:${port}`));
