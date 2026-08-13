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
let pricingOverrides = {};
const catalog = [
  { model: "gpt-5.6-sol", display_name: "GPT-5.6 Sol", input_usd_per_million: "5.00", cached_input_usd_per_million: "0.50", cache_write_input_usd_per_million: "6.25", output_usd_per_million: "30.00", source: "https://developers.openai.com/api/docs/models/gpt-5.6-sol" },
  { model: "gpt-5.6-terra", display_name: "GPT-5.6 Terra", input_usd_per_million: "2.00", cached_input_usd_per_million: "0.20", cache_write_input_usd_per_million: "2.50", output_usd_per_million: "12.00", source: "https://developers.openai.com/api/docs/models/gpt-5.6-terra" },
  { model: "gpt-5.6-luna", display_name: "GPT-5.6 Luna", input_usd_per_million: "0.20", cached_input_usd_per_million: "0.02", cache_write_input_usd_per_million: "0.25", output_usd_per_million: "1.20", source: "https://developers.openai.com/api/docs/models/gpt-5.6-luna" },
  { model: "gpt-5.5", display_name: "GPT-5.5", input_usd_per_million: "5.00", cached_input_usd_per_million: "0.50", output_usd_per_million: "30.00", source: "https://developers.openai.com/api/docs/models/gpt-5.5" },
  { model: "gpt-5.4", display_name: "GPT-5.4", input_usd_per_million: "2.50", cached_input_usd_per_million: "0.25", output_usd_per_million: "15.00", source: "https://developers.openai.com/api/docs/models/gpt-5.4" }
];

function json(response, value, status = 200) {
  response.writeHead(status, { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" });
  response.end(JSON.stringify(value));
}

function summary(multiplier = 1) {
  const scaled = Object.fromEntries(Object.entries(usage).map(([key, value]) => [key, Math.round(value * multiplier)]));
  return { usage: scaled, unattributed: { input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0, total: 0 }, grand_total: scaled.total, event_count: 114, session_count: 26, coverage_incomplete: false };
}

function localDateKey(date) {
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function localHourKey(date) {
  return `${localDateKey(date)}T${String(date.getHours()).padStart(2, "0")}`;
}

function rangeDate(value) {
  return new Date(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00` : value);
}

function costEstimate(url) {
  const start = url.searchParams.get("since") ? rangeDate(url.searchParams.get("since")) : new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29);
  const end = url.searchParams.get("until") ? rangeDate(url.searchParams.get("until")) : new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
  const points = [];
  let index = 0;
  for (let date = new Date(start); date < end && index < 120; date.setDate(date.getDate() + 1), index++) {
    const seed = Math.floor(date.getTime() / 86400000);
    const isZero = seed % 11 === 3;
    const total = isZero ? 0 : 170_000 + Math.round((Math.sin(seed * .73) + 1.25) * 115_000) + (seed % 31) * 4200;
    const input = Math.round(total * .82);
    const output = total - input;
    const dailyUsage = { input, cached_input: Math.round(input * .56), cache_write_input: Math.round(input * .025), output, reasoning_output: Math.round(output * .37), total };
    const priced = Math.round(total * (pricingOverrides["codex-auto-review"] ? 1 : .916));
    const unpriced = total - priced;
    const usd = total ? (total / 1_000_000 * 3.18).toFixed(9) : "0.000000000";
    points.push({ date: localDateKey(date), time: new Date(date).toISOString(), usage: dailyUsage, estimate: { usd, regular_input_usd: usd, cached_input_usd: "0.000000000", cache_write_input_usd: "0.000000000", output_usd: "0.000000000", priced_tokens: priced, unpriced_tokens: unpriced, coverage_ratio: total ? priced / total : 0, reasons: unpriced ? [{ kind: "unknown_model", model: "codex-auto-review", tokens: unpriced, detail: "没有公开 API 单价或本机定价覆写" }] : [] } });
  }
  const totalUsage = points.reduce((sum, point) => Object.fromEntries(Object.keys(point.usage).map((key) => [key, (sum[key] || 0) + point.usage[key]])), {});
  const pricedTokens = points.reduce((sum, point) => sum + point.estimate.priced_tokens, 0);
  const unpricedTokens = points.reduce((sum, point) => sum + point.estimate.unpriced_tokens, 0);
  const totalCost = points.reduce((sum, point) => sum + Number(point.estimate.usd), 0);
  const estimate = { usd: totalCost.toFixed(9), regular_input_usd: totalCost.toFixed(9), cached_input_usd: "0.000000000", cache_write_input_usd: "0.000000000", output_usd: "0.000000000", priced_tokens: pricedTokens, unpriced_tokens: unpricedTokens, coverage_ratio: pricedTokens + unpricedTokens ? pricedTokens / (pricedTokens + unpricedTokens) : 0, reasons: unpricedTokens ? [{ kind: "unknown_model", model: "codex-auto-review", tokens: unpricedTokens, detail: "没有公开 API 单价或本机定价覆写" }] : [] };
  return {
    basis: "current_standard_api_text_token_prices", currency: "USD", catalog_as_of: "2026-08-04", bucket: "day", summary: estimate, points,
    models: [
      { key: "gpt-5.4", usage: { ...totalUsage, total: Math.round(totalUsage.total * .63) }, estimate: { ...estimate, usd: (totalCost * .71).toFixed(9), priced_tokens: Math.round(totalUsage.total * .63), unpriced_tokens: 0, coverage_ratio: 1, reasons: [] } },
      { key: "gpt-5.6-terra", usage: { ...totalUsage, total: Math.round(totalUsage.total * .26) }, estimate: { ...estimate, usd: (totalCost * .25).toFixed(9), priced_tokens: Math.round(totalUsage.total * .26), unpriced_tokens: 0, coverage_ratio: 1, reasons: [] } },
      { key: "codex-auto-review", usage: { ...totalUsage, total: Math.round(totalUsage.total * .11) }, estimate: { ...estimate, usd: pricingOverrides["codex-auto-review"] ? (totalCost * .04).toFixed(9) : "0.000000000", priced_tokens: pricingOverrides["codex-auto-review"] ? Math.round(totalUsage.total * .11) : 0, unpriced_tokens: pricingOverrides["codex-auto-review"] ? 0 : Math.round(totalUsage.total * .11), coverage_ratio: pricingOverrides["codex-auto-review"] ? 1 : 0 } }
    ]
  };
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, `http://127.0.0.1:${port}`);
  if (url.pathname === "/api/v1/status") return json(response, {
    version: "2.3.4-preview", scanning: false,
    status: {
      machine: { id: "62c0172d-36c4-4ec9-a074-02b9ec2b45e1", label: "WORKSTATION-19 · windows", hostname: "WORKSTATION-19", os: "windows", arch: "amd64" },
      last_scan: now.toISOString(), accounting_mode: "jsonl_only", otel_active: false,
      event_count: 114, session_count: 26, warning_count: 2,
      codex_homes: [{ path: "C:\\Users\\demo\\.codex", last_scan: now.toISOString(), files_scanned: 26 }]
    }
  });
  if (url.pathname === "/api/v1/summary") {
    const since = url.searchParams.get("since");
    const until = url.searchParams.get("until");
    const days = since && until ? Math.max(1, Math.round((new Date(until) - new Date(since)) / 86400000)) : 30;
    return json(response, summary(Math.min(1, days / 30)));
  }
  if (url.pathname === "/api/v1/cost-estimate") return json(response, costEstimate(url));
  if (url.pathname === "/api/v1/pricing" && request.method === "GET") return json(response, {
    basis: "current_standard_api_text_token_prices", currency: "USD", catalog_as_of: "2026-08-04", catalog, overrides: pricingOverrides,
    unpriced_models: pricingOverrides["codex-auto-review"] ? [] : [{ key: "codex-auto-review", usage: { ...usage, total: 438_000 }, events: 9, sessions: 3 }]
  });
  if (url.pathname === "/api/v1/pricing/overrides" && request.method === "PUT") {
    let body = "";
    for await (const chunk of request) body += chunk;
    try {
      pricingOverrides = JSON.parse(body).overrides || {};
      return json(response, { basis: "current_standard_api_text_token_prices", currency: "USD", catalog_as_of: "2026-08-04", catalog, overrides: pricingOverrides, unpriced_models: pricingOverrides["codex-auto-review"] ? [] : [{ key: "codex-auto-review", usage: { ...usage, total: 438_000 }, events: 9, sessions: 3 }] });
    } catch {
      return json(response, { error: "无效请求体" }, 400);
    }
  }
  if (url.pathname === "/api/v1/timeseries") {
    const bucket = url.searchParams.get("bucket") === "hour" ? "hour" : "day";
    const unit = bucket === "hour" ? 3_600_000 : 86_400_000;
    const fallbackCount = bucket === "hour" ? 24 : 30;
    const fallbackEnd = bucket === "hour" ? new Date(new Date(now).setMinutes(0, 0, 0)) : now;
    const start = bucket === "hour" && url.searchParams.get("since") ? new Date(url.searchParams.get("since")) : new Date(fallbackEnd.getTime() - fallbackCount * unit);
    const end = bucket === "hour" && url.searchParams.get("until") ? new Date(url.searchParams.get("until")) : fallbackEnd;
    const count = bucket === "hour" ? Math.max(0, Math.min(744, Math.round((end.getTime() - start.getTime()) / unit))) : fallbackCount;
    const points = Array.from({ length: count }, (_, index) => {
      const time = bucket === "hour" ? new Date(start.getTime() + index * unit) : new Date(now.getTime() - (count - 1 - index) * unit);
      const wave = 150_000 + Math.round((Math.sin(index * .72) + 1.35) * 145_000) + index * 8500;
      return { time: time.toISOString(), date: bucket === "hour" ? localHourKey(time) : localDateKey(time), usage: { input: Math.round(wave * .78), cached_input: Math.round(wave * .42), cache_write_input: 0, output: Math.round(wave * .22), reasoning_output: Math.round(wave * .08), total: wave } };
    });
    return json(response, { bucket, points });
  }
  if (url.pathname === "/api/v1/breakdown") {
    const dimension = url.searchParams.get("dimension");
    if (dimension === "agent_type") return json(response, { dimension, items: agents });
    if (dimension === "source") return json(response, { dimension, items: [{ key: "codex_desktop", usage, events: 70, sessions: 16 }, { key: "codex_cli_rs", usage: { ...usage, total: 2_100_000 }, events: 44, sessions: 10 }] });
    if (dimension === "project") return json(response, { dimension, items: [{ key: "C:\\dev\\render-lab", usage, events: 70, sessions: 12 }, { key: "/srv/inference", usage: { ...usage, total: 2_100_000 }, events: 44, sessions: 8 }] });
    if (dimension === "thread") return json(response, { dimension, items: [{ key: "Turntable occlusion refinement", usage, events: 12, sessions: 1 }, { key: "Linux batch migration", usage: { ...usage, total: 2_100_000 }, events: 9, sessions: 1 }] });
    return json(response, { dimension: "model", items });
  }
  if (url.pathname === "/api/v1/dimensions") return json(response, {
    models: items.map((item) => item.key),
    sources: ["codex_desktop", "codex_cli_rs"],
    projects: ["C:\\dev\\render-lab", "/srv/inference"]
  });
  if (url.pathname === "/api/v1/sessions" || url.pathname === "/api/v1/session-estimates") {
    const sessions = [
      { session_id: "019fb24a-f4dd-7673-9b7d-225d26f2b141", title: "实现逐电脑 Token 统计与可视化", project_path: "C:\\dev\\codex-usage", model: "gpt-5.4", source: "codex_desktop", agent_type: "main", usage: { ...usage, total: 2_920_000 }, confidence: "exact", last_usage: now.toISOString() },
      { session_id: "019fa142-1051-72bd-aa0f-975efd2bf6c2", title: "Linux 渲染任务诊断", project_path: "/srv/inference/render", model: "gpt-5.5-codex", source: "codex_cli_rs", agent_type: "subagent", usage: { ...usage, total: 1_180_000 }, confidence: "gap_fallback", last_usage: new Date(now.getTime() - 3600000).toISOString() },
      { session_id: "019f9821-d272-7812-814d-94cc02dc39a1", title: "本地数据管线检查", project_path: "C:\\dev\\private-data", model: "gpt-5.4", source: "codex_desktop", agent_type: "guardian", usage: { ...usage, total: 760_000 }, confidence: "exact", last_usage: new Date(now.getTime() - 7200000).toISOString() }
    ];
    const query = (url.searchParams.get("q") || "").trim().toLocaleLowerCase();
    const sessionID = url.searchParams.get("session_id");
    let responseItems = sessions.filter((item) => {
      if (sessionID && item.session_id !== sessionID) return false;
      return !query || [item.title, item.session_id, item.project_path, item.model, item.source]
        .some((value) => String(value || "").toLocaleLowerCase().includes(query));
    }).map((item) => ({
      ...item,
      estimate: {
        usd: (item.usage.total / 1_000_000 * 3.18).toFixed(9),
        regular_input_usd: "0.000000000", cached_input_usd: "0.000000000", cache_write_input_usd: "0.000000000", output_usd: "0.000000000",
        priced_tokens: item.usage.total, unpriced_tokens: 0, coverage_ratio: 1, reasons: []
      }
    }));
    if (url.pathname === "/api/v1/session-estimates") {
      responseItems = responseItems.map((item) => ({ session_id: item.session_id, estimate: item.estimate }));
    } else if (["0", "false"].includes((url.searchParams.get("include_estimate") || "").toLowerCase())) {
      responseItems = responseItems.map(({ estimate, ...item }) => item);
    }
    return json(response, { items: responseItems });
  }
  if (url.pathname === "/api/v1/warnings") return json(response, { items: [
    { created_at: now.toISOString(), kind: "fork_replay_detected", path: "rollout-fork.jsonl", detail: "检测到复制的父线程历史前缀，已跳过并重建派生索引" },
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
