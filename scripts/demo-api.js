(function (root) {
  "use strict";

  root.CODEX_USAGE_DEMO = true;
  const now = new Date();
  let pricingOverrides = {};

  const baseUsage = {
    input: 8_740_000,
    cached_input: 5_210_000,
    cache_write_input: 240_000,
    output: 1_430_000,
    reasoning_output: 620_000,
    total: 10_170_000
  };
  const catalog = [
    { model: "gpt-5.6-sol", display_name: "GPT-5.6 Sol", input_usd_per_million: "5.00", cached_input_usd_per_million: "0.50", cache_write_input_usd_per_million: "6.25", output_usd_per_million: "30.00", source: "#synthetic-pricing" },
    { model: "gpt-5.6-terra", display_name: "GPT-5.6 Terra", input_usd_per_million: "2.00", cached_input_usd_per_million: "0.20", cache_write_input_usd_per_million: "2.50", output_usd_per_million: "12.00", source: "#synthetic-pricing" },
    { model: "gpt-5.4", display_name: "GPT-5.4", input_usd_per_million: "2.50", cached_input_usd_per_million: "0.25", output_usd_per_million: "15.00", source: "#synthetic-pricing" }
  ];
  const models = [
    { key: "gpt-5.4", share: .57, events: 71, sessions: 15 },
    { key: "gpt-5.6-terra", share: .31, events: 32, sessions: 8 },
    { key: "codex-auto-review", share: .12, events: 11, sessions: 3 }
  ];
  const agents = [
    { key: "main", share: .75, events: 80, sessions: 20 },
    { key: "subagent", share: .17, events: 21, sessions: 6 },
    { key: "guardian", share: .05, events: 8, sessions: 4 },
    { key: "memory", share: .03, events: 5, sessions: 2 }
  ];
  const sources = [
    { key: "codex_desktop", share: .68, events: 70, sessions: 16 },
    { key: "codex_cli_rs", share: .32, events: 44, sessions: 10 }
  ];
  const projects = [
    { key: "synthetic://visual-lab", share: .62, events: 70, sessions: 12 },
    { key: "synthetic://service-api", share: .38, events: 44, sessions: 8 }
  ];
  const threads = [
    { key: "Dashboard localization", share: .61, events: 12, sessions: 1 },
    { key: "JSONL fork replay audit", share: .39, events: 9, sessions: 1 }
  ];
  const sessions = [
    { session_id: "demo-session-a", title: "Dashboard localization", project_path: "synthetic://visual-lab", model: "gpt-5.4", source: "codex_desktop", agent_type: "main", share: .48, confidence: "exact", hoursAgo: 0 },
    { session_id: "demo-session-b", title: "JSONL fork replay audit", project_path: "synthetic://service-api", model: "gpt-5.6-terra", source: "codex_cli_rs", agent_type: "subagent", share: .31, confidence: "gap_fallback", hoursAgo: 2 },
    { session_id: "demo-session-c", title: "Local data-quality review", project_path: "synthetic://visual-lab", model: "gpt-5.4", source: "codex_desktop", agent_type: "guardian", share: .21, confidence: "exact", hoursAgo: 7 }
  ];

  const pad = (value) => String(value).padStart(2, "0");
  const dateKey = (date) => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  const hourKey = (date) => `${dateKey(date)}T${pad(date.getHours())}`;
  const rangeDate = (value) => new Date(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T00:00:00` : value);
  function requestedBounds(url, fallbackStart = null, fallbackEnd = null) {
    let start = url.searchParams.get("since") ? rangeDate(url.searchParams.get("since")) : null;
    let end = url.searchParams.get("until") ? rangeDate(url.searchParams.get("until")) : null;
    const selectedDate = url.searchParams.get("date");
    if (/^\d{4}-\d{2}-\d{2}$/.test(selectedDate || "")) {
      const dayStart = rangeDate(selectedDate);
      const dayEnd = new Date(dayStart);
      dayEnd.setDate(dayEnd.getDate() + 1);
      if (!start || dayStart > start) start = dayStart;
      if (!end || dayEnd < end) end = dayEnd;
    }
    return { start: start || fallbackStart, end: end || fallbackEnd };
  }
  const scaledUsage = (usage, multiplier) => Object.fromEntries(Object.entries(usage).map(([key, value]) => [key, Math.round(value * multiplier)]));
  const jsonResponse = (value, status = 200) => new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" }
  });

  function filterScale(url) {
    let scale = 1;
    for (const key of ["model", "source", "agent_type", "project", "confidence"]) {
      if (url.searchParams.get(key)) scale *= .42;
    }
    return scale;
  }

  function summary(url) {
    const { start, end } = requestedBounds(url);
    const dayScale = start && end ? Math.min(1, Math.max(1 / 24, (end - start) / 86_400_000) / 30) : 1;
    const usage = scaledUsage(baseUsage, dayScale * filterScale(url));
    return {
      usage,
      unattributed: scaledUsage(baseUsage, 0),
      grand_total: usage.total,
      event_count: Math.max(1, Math.round(114 * dayScale * filterScale(url))),
      session_count: Math.max(1, Math.round(26 * dayScale * filterScale(url))),
      coverage_incomplete: false
    };
  }

  function dailyPoints(url) {
    const { start, end } = requestedBounds(
      url,
      new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29),
      new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
    );
    const points = [];
    let index = 0;
    for (let date = new Date(start); date < end && index < 120; date.setDate(date.getDate() + 1), index++) {
      const seed = Math.floor(date.getTime() / 86_400_000);
      const total = seed % 11 === 3 ? 0 : Math.round((180_000 + (Math.sin(seed * .73) + 1.25) * 118_000 + (seed % 31) * 4_100) * filterScale(url));
      const input = Math.round(total * .82);
      const output = total - input;
      const usage = { input, cached_input: Math.round(input * .56), cache_write_input: Math.round(input * .025), output, reasoning_output: Math.round(output * .37), total };
      const custom = Boolean(pricingOverrides["codex-auto-review"]);
      const priced = Math.round(total * (custom ? 1 : .88));
      const unpriced = total - priced;
      const usd = (priced / 1_000_000 * 3.18).toFixed(9);
      points.push({
        date: dateKey(date),
        time: new Date(date).toISOString(),
        usage,
        estimate: {
          usd,
          regular_input_usd: usd,
          cached_input_usd: "0.000000000",
          cache_write_input_usd: "0.000000000",
          output_usd: "0.000000000",
          priced_tokens: priced,
          unpriced_tokens: unpriced,
          coverage_ratio: total ? priced / total : 0,
          reasons: unpriced ? [{ kind: "unknown_model", model: "codex-auto-review", tokens: unpriced, detail: "Synthetic model has no public API rate or local override." }] : []
        }
      });
    }
    return points;
  }

  function hourlyPoints(url) {
    const currentHour = new Date(now);
    currentHour.setMinutes(0, 0, 0);
    const { start, end } = requestedBounds(url, new Date(currentHour.getTime() - 24 * 60 * 60_000), currentHour);
    const points = [];
    let index = 0;
    for (let date = new Date(start); date < end && index < 168; date = new Date(date.getTime() + 60 * 60_000), index++) {
      const seed = Math.floor(date.getTime() / 3_600_000);
      const total = seed % 13 === 4 ? 0 : Math.round((38_000 + (Math.sin(seed * .61) + 1.2) * 29_000 + (seed % 17) * 900) * filterScale(url));
      const input = Math.round(total * .81);
      const output = total - input;
      points.push({
        date: hourKey(date),
        time: date.toISOString(),
        usage: { input, cached_input: Math.round(input * .54), cache_write_input: Math.round(input * .02), output, reasoning_output: Math.round(output * .35), total }
      });
    }
    return points;
  }

  function costEstimate(url) {
    const points = dailyPoints(url);
    const usage = points.reduce((sum, point) => {
      for (const key of Object.keys(point.usage)) sum[key] = (sum[key] || 0) + point.usage[key];
      return sum;
    }, {});
    const pricedTokens = points.reduce((sum, point) => sum + point.estimate.priced_tokens, 0);
    const unpricedTokens = points.reduce((sum, point) => sum + point.estimate.unpriced_tokens, 0);
    const totalCost = points.reduce((sum, point) => sum + Number(point.estimate.usd), 0);
    const estimate = {
      usd: totalCost.toFixed(9), regular_input_usd: totalCost.toFixed(9), cached_input_usd: "0.000000000", cache_write_input_usd: "0.000000000", output_usd: "0.000000000",
      priced_tokens: pricedTokens, unpriced_tokens: unpricedTokens,
      coverage_ratio: pricedTokens + unpricedTokens ? pricedTokens / (pricedTokens + unpricedTokens) : 0,
      reasons: unpricedTokens ? [{ kind: "unknown_model", model: "codex-auto-review", tokens: unpricedTokens, detail: "Synthetic model has no public API rate or local override." }] : []
    };
    return {
      basis: "current_standard_api_text_token_prices", currency: "USD", catalog_as_of: "2026-08-04", bucket: "day", summary: estimate, points,
      models: models.map((item) => {
        const itemUsage = scaledUsage(usage, item.share);
        const unknown = item.key === "codex-auto-review" && !pricingOverrides[item.key];
        return { key: item.key, usage: itemUsage, estimate: { ...estimate, usd: unknown ? "0.000000000" : (totalCost * item.share).toFixed(9), priced_tokens: unknown ? 0 : itemUsage.total, unpriced_tokens: unknown ? itemUsage.total : 0, coverage_ratio: unknown ? 0 : 1, reasons: unknown ? estimate.reasons : [] } };
      })
    };
  }

  function breakdown(url) {
    const dimension = url.searchParams.get("dimension") || "model";
    const source = { model: models, source: sources, agent_type: agents, project: projects, thread: threads }[dimension] || models;
    const requested = url.searchParams.get(dimension);
    const entries = requested ? source.filter((item) => item.key === requested) : source;
    return { dimension, items: entries.map((item) => ({ key: item.key, usage: scaledUsage(baseUsage, item.share * filterScale(url)), events: item.events, sessions: item.sessions })) };
  }

  function sessionPayload(url) {
    return { items: sessions.filter((item) => {
      for (const key of ["model", "source", "agent_type", "project", "confidence"]) {
        const expected = url.searchParams.get(key);
        const actual = key === "project" ? item.project_path : item[key];
        if (expected && actual !== expected) return false;
      }
      const sessionID = url.searchParams.get("session_id");
      if (sessionID && item.session_id !== sessionID) return false;
      const query = (url.searchParams.get("q") || "").trim().toLocaleLowerCase();
      if (query && ![item.title, item.session_id, item.project_path, item.model, item.source]
        .some((value) => String(value || "").toLocaleLowerCase().includes(query))) return false;
      return true;
    }).map(({ share, hoursAgo, ...item }) => {
      const usage = scaledUsage(baseUsage, share);
      const usd = (usage.total / 1_000_000 * 3.18).toFixed(9);
      return {
        ...item,
        usage,
        estimate: {
          usd, regular_input_usd: usd, cached_input_usd: "0.000000000", cache_write_input_usd: "0.000000000", output_usd: "0.000000000",
          priced_tokens: usage.total, unpriced_tokens: 0, coverage_ratio: 1, reasons: []
        },
        last_usage: new Date(now.getTime() - hoursAgo * 3_600_000).toISOString()
      };
    }) };
  }

  function pricingPayload() {
    return {
      basis: "current_standard_api_text_token_prices",
      currency: "USD",
      catalog_as_of: "2026-08-04",
      catalog,
      overrides: pricingOverrides,
      unpriced_models: pricingOverrides["codex-auto-review"] ? [] : [{ key: "codex-auto-review", usage: scaledUsage(baseUsage, .12), events: 9, sessions: 3 }]
    };
  }

  async function syntheticFetch(input, init = {}) {
    const raw = typeof input === "string" ? input : input.url;
    const url = new URL(raw, root.location.href);
    if (!url.pathname.includes("/api/v1/")) return root.fetch.__codexUsageOriginal(input, init);
    const endpoint = url.pathname.slice(url.pathname.indexOf("/api/v1/"));
    const method = String(init.method || (typeof input !== "string" && input.method) || "GET").toUpperCase();
    if (endpoint === "/api/v1/status") return jsonResponse({
      version: "2.3.5-demo", scanning: false,
      status: {
        machine: { id: "synthetic-machine", label: "Synthetic Windows · demo", hostname: "synthetic-host", os: "windows", arch: "amd64" },
        last_scan: now.toISOString(), accounting_mode: "jsonl_only", otel_active: false,
        event_count: 114, session_count: 26, warning_count: 2, data_revision: "demo-1",
        codex_homes: [{ path: "synthetic://codex-home", last_scan: now.toISOString(), files_scanned: 26 }]
      }
    });
    if (endpoint === "/api/v1/summary") return jsonResponse(summary(url));
    if (endpoint === "/api/v1/cost-estimate") return jsonResponse(costEstimate(url));
    if (endpoint === "/api/v1/timeseries") {
      const bucket = url.searchParams.get("bucket") === "hour" ? "hour" : "day";
      const points = bucket === "hour" ? hourlyPoints(url) : dailyPoints(url).map((point) => ({ time: point.time, date: point.date, usage: point.usage }));
      return jsonResponse({ bucket, points });
    }
    if (endpoint === "/api/v1/breakdown") return jsonResponse(breakdown(url));
    if (endpoint === "/api/v1/dimensions") return jsonResponse({
      models: models.map((item) => item.key),
      sources: sources.map((item) => item.key),
      projects: projects.map((item) => item.key)
    });
    if (endpoint === "/api/v1/sessions") {
      const payload = sessionPayload(url);
      if (["0", "false"].includes((url.searchParams.get("include_estimate") || "").toLowerCase())) {
        payload.items = payload.items.map(({ estimate, ...item }) => item);
      }
      return jsonResponse(payload);
    }
    if (endpoint === "/api/v1/session-estimates") {
      const payload = sessionPayload(url);
      return jsonResponse({ items: payload.items.map((item) => ({ session_id: item.session_id, estimate: item.estimate })) });
    }
    if (endpoint === "/api/v1/warnings") return jsonResponse({ items: [
      { created_at: now.toISOString(), first_seen: new Date(now.getTime() - 86_400_000).toISOString(), occurrences: 1, kind: "fork_replay_detected", path: "synthetic://rollout", detail: "Synthetic copied parent history was skipped and the JSONL index was rebuilt." },
      { created_at: now.toISOString(), occurrences: 1, kind: "cumulative_reset", path: "synthetic://rollout", detail: "Synthetic cumulative vector moved backward; last_token_usage filled the delta." }
    ] });
    if (endpoint === "/api/v1/pricing" && method === "GET") return jsonResponse(pricingPayload());
    if (endpoint === "/api/v1/pricing/overrides" && method === "PUT") {
      try {
        pricingOverrides = JSON.parse(init.body || "{}").overrides || {};
        return jsonResponse(pricingPayload());
      } catch {
        return jsonResponse({ error: "Invalid synthetic request body" }, 400);
      }
    }
    if (endpoint === "/api/v1/rescan" && method === "POST") return jsonResponse({ homes: 1, files: 26, records: 940, events_inserted: 2, duplicates: 14, warnings: 0 });
    if (endpoint === "/api/v1/export") return jsonResponse(sessionPayload(url).items);
    return jsonResponse({ error: `No synthetic adapter for ${method} ${endpoint}` }, 404);
  }

  const originalFetch = root.fetch.bind(root);
  syntheticFetch.__codexUsageOriginal = originalFetch;
  root.fetch = syntheticFetch;

  function exportData(format, requestPath) {
    const rows = sessionPayload(new URL(requestPath || root.location.href, root.location.href)).items.map((item) => ({
      session_id: item.session_id,
      project_path: item.project_path,
      model: item.model,
      source: item.source,
      total: item.usage.total
    }));
    if (format === "csv") {
      const columns = ["session_id", "project_path", "model", "source", "total"];
      const csv = [columns.join(","), ...rows.map((row) => columns.map((key) => JSON.stringify(row[key])).join(","))].join("\n");
      return `data:text/csv;charset=utf-8,${encodeURIComponent(csv)}`;
    }
    return `data:application/json;charset=utf-8,${encodeURIComponent(JSON.stringify(rows, null, 2))}`;
  }

  root.CodexUsageDemo = {
    synthetic: true,
    createExportURL: (format, requestPath) => exportData(format, requestPath),
    reset: () => { pricingOverrides = {}; }
  };
})(globalThis);
