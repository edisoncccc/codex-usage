const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const state = { range: "7d", status: null, filters: {}, chartPoints: [] };
const colors = ["#a989ff", "#61a9ff", "#5bd6b4", "#efb85d", "#ff857c", "#7d8ba8"];

const formatToken = (value = 0) => {
  const n = Number(value || 0);
  if (Math.abs(n) >= 1e9) return `${(n / 1e9).toFixed(n >= 1e10 ? 1 : 2)}B`;
  if (Math.abs(n) >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 1 : 2)}M`;
  if (Math.abs(n) >= 1e3) return `${(n / 1e3).toFixed(n >= 1e4 ? 1 : 2)}K`;
  return n.toLocaleString("zh-CN");
};

const fullToken = (value = 0) => Number(value || 0).toLocaleString("zh-CN");
const localTime = (value) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
const shortId = (value = "") => value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value || "—";
const escapeHTML = (value = "") => String(value).replace(/[&<>"']/g, (char) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[char]));

async function api(path, options) {
  const response = await fetch(path, { cache: "no-store", ...options });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try { message = (await response.json()).error || message; } catch {}
    throw new Error(message);
  }
  return response.json();
}

function filterQuery(extra = {}) {
  const query = new URLSearchParams();
  const values = {
    model: $("#filterModel").value,
    source: $("#filterSource").value,
    agent_type: $("#filterAgent").value,
    project: $("#filterProject").value,
    confidence: $("#filterConfidence").value,
    ...extra
  };
  for (const [key, value] of Object.entries(values)) if (value && value !== "all") query.set(key, value);
  return query.toString();
}

function detailHTML(usage = {}) {
  return [
    `<span>Input <b title="${fullToken(usage.input)}">${formatToken(usage.input)}</b></span>`,
    `<span>Output <b title="${fullToken(usage.output)}">${formatToken(usage.output)}</b></span>`,
    `<span>Cached <b title="${fullToken(usage.cached_input)}">${formatToken(usage.cached_input)}</b></span>`,
    `<span>Reasoning <b title="${fullToken(usage.reasoning_output)}">${formatToken(usage.reasoning_output)}</b></span>`
  ].join("");
}

async function loadStatus() {
  const payload = await api("/api/v1/status");
  state.status = payload;
  const status = payload.status;
  $("#machineLabel").textContent = status.machine.label;
  $("#machineId").textContent = shortId(status.machine.id);
  $("#machineId").title = status.machine.id;
  $("#lastScan").textContent = localTime(status.last_scan);
  $("#otelStatus").textContent = status.otel_active ? `实时 · ${localTime(status.otel_last_received)}` :
    status.otel_last_received ? `最近 ${localTime(status.otel_last_received)}` : "未收到（历史扫描可用）";
  $("#otelDot").className = `status-dot ${status.otel_active ? "live" : status.otel_last_received ? "warn" : "neutral"}`;
  $("#versionLabel").textContent = `v${payload.version}`;
  $("#scanButton").disabled = Boolean(payload.scanning);
  $(".scan-icon").classList.toggle("spin", Boolean(payload.scanning));
  const notes = [];
  if (status.warning_count) notes.push(`${status.warning_count} 条解析或覆盖提示`);
  if ((status.coverage_gaps || []).length) {
    const open = status.coverage_gaps.filter((gap) => gap.open).length;
    notes.push(`实时覆盖记录到 ${status.coverage_gaps.length} 段服务离线缺口${open ? "（当前仍有缺口）" : ""}`);
  }
  for (const home of status.codex_homes || []) if (home.warning) notes.push(home.warning);
  if (notes.length) {
    $("#coverageBanner").classList.remove("hidden");
    $("#coverageText").textContent = notes.join("；");
  }
}

async function loadCards() {
  const periods = [["today", "Today"], ["7d", "7d"], ["30d", "30d"], ["all", "All"]];
  const data = await Promise.all(periods.map(([period]) =>
    api(`/api/v1/summary?${filterQuery({ since: period })}`)));
  data.forEach((summary, index) => {
    const suffix = periods[index][1];
    const total = $(`#total${suffix}`);
    const displayedTotal = summary.grand_total ?? summary.usage.total;
    total.textContent = formatToken(displayedTotal);
    total.title = `${fullToken(displayedTotal)} total tokens`;
    total.classList.remove("skeleton");
    $(`#detail${suffix}`).innerHTML = detailHTML(summary.usage);
  });
  const all = data[3];
  $("#unattributed").textContent = formatToken(all.unattributed.total);
  $("#unattributed").title = `${fullToken(all.unattributed.total)}：只计入累计，不伪造日期`;
  if (all.coverage_incomplete || all.unattributed.total > 0) {
    $("#coverageBanner").classList.remove("hidden");
    const base = $("#coverageText").textContent;
    const extra = all.unattributed.total > 0 ? `有 ${fullToken(all.unattributed.total)} Token 只能归属到历史累计，未分摊到日期` : "部分事件置信度不是 exact";
    $("#coverageText").textContent = [base, extra].filter(Boolean).join("；");
  }
}

async function loadTrend() {
  const bucket = state.range === "7d" ? "hour" : "day";
  const payload = await api(`/api/v1/timeseries?${filterQuery({ since: state.range, bucket })}`);
  state.chartPoints = payload.points || [];
  renderChart(state.chartPoints);
}

function renderChart(points) {
  const svg = $("#trendChart");
  const empty = $("#chartEmpty");
  svg.innerHTML = "";
  if (!points.length) {
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  const width = Math.max(720, svg.clientWidth || 1000);
  const height = 286, left = 50, right = 12, top = 14, bottom = 31;
  const innerW = width - left - right, innerH = height - top - bottom;
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  const max = Math.max(...points.map((p) => Number(p.usage.total || 0)), 1);
  const x = (i) => left + (points.length === 1 ? innerW / 2 : i * innerW / (points.length - 1));
  const y = (v) => top + innerH - (Number(v || 0) / max) * innerH;
  const ns = "http://www.w3.org/2000/svg";
  const add = (name, attrs, parent = svg) => {
    const node = document.createElementNS(ns, name);
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, value);
    parent.appendChild(node);
    return node;
  };
  const defs = add("defs", {});
  const gradient = add("linearGradient", { id: "areaGradient", x1: "0", y1: "0", x2: "0", y2: "1" }, defs);
  add("stop", { offset: "0", "stop-color": "#a989ff", "stop-opacity": ".28" }, gradient);
  add("stop", { offset: "1", "stop-color": "#a989ff", "stop-opacity": "0" }, gradient);
  for (let i = 0; i <= 4; i++) {
    const gy = top + i * innerH / 4;
    add("line", { x1: left, x2: width - right, y1: gy, y2: gy, stroke: "currentColor", "stroke-opacity": ".08", "stroke-width": "1" });
    const label = add("text", { x: left - 9, y: gy + 3, "text-anchor": "end", fill: "currentColor", "fill-opacity": ".46", "font-size": "9" });
    label.textContent = formatToken(max * (4 - i) / 4);
  }
  const pathFor = (key) => points.map((p, i) => `${i ? "L" : "M"}${x(i)},${y(p.usage[key])}`).join(" ");
  const totalPath = pathFor("total");
  const area = `${totalPath} L${x(points.length - 1)},${top + innerH} L${x(0)},${top + innerH} Z`;
  add("path", { d: area, fill: "url(#areaGradient)" });
  add("path", { d: totalPath, fill: "none", stroke: "#a989ff", "stroke-width": "2.2", "stroke-linejoin": "round", "stroke-linecap": "round" });
  add("path", { d: pathFor("input"), fill: "none", stroke: "#61a9ff", "stroke-opacity": ".8", "stroke-width": "1.2" });
  add("path", { d: pathFor("output"), fill: "none", stroke: "#5bd6b4", "stroke-opacity": ".8", "stroke-width": "1.2" });
  const labelCount = Math.min(7, points.length);
  for (let i = 0; i < labelCount; i++) {
    const index = Math.round(i * (points.length - 1) / Math.max(labelCount - 1, 1));
    const label = add("text", { x: x(index), y: height - 8, "text-anchor": i === 0 ? "start" : i === labelCount - 1 ? "end" : "middle", fill: "currentColor", "fill-opacity": ".48", "font-size": "9" });
    const date = new Date(points[index].time);
    label.textContent = state.range === "7d" ? date.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit" }) : date.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
  }
  points.forEach((point, index) => {
    const hit = add("circle", { cx: x(index), cy: y(point.usage.total), r: "8", fill: "transparent", tabindex: "0" });
    const show = (event) => showTooltip(event, point);
    hit.addEventListener("mouseenter", show);
    hit.addEventListener("mousemove", show);
    hit.addEventListener("focus", show);
    hit.addEventListener("mouseleave", hideTooltip);
    hit.addEventListener("blur", hideTooltip);
  });
}

function showTooltip(event, point) {
  const tooltip = $("#chartTooltip");
  tooltip.innerHTML = `<strong>${escapeHTML(localTime(point.time))}</strong><br>Total ${fullToken(point.usage.total)}<br>Input ${fullToken(point.usage.input)} · Output ${fullToken(point.usage.output)}<br>Cached ${fullToken(point.usage.cached_input)} · Reasoning ${fullToken(point.usage.reasoning_output)}`;
  tooltip.classList.remove("hidden");
  const x = event.clientX || event.target.getBoundingClientRect().left;
  const y = event.clientY || event.target.getBoundingClientRect().top;
  tooltip.style.left = `${Math.min(x + 12, window.innerWidth - 250)}px`;
  tooltip.style.top = `${Math.max(8, y - 88)}px`;
}
function hideTooltip() { $("#chartTooltip").classList.add("hidden"); }

async function loadBreakdowns() {
  const [models, agents, sourceDisplay, projectDisplay, threadDisplay, sources, projects] = await Promise.all([
    api(`/api/v1/breakdown?${filterQuery({ since: state.range, dimension: "model", limit: 8 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: state.range, dimension: "agent_type", limit: 8 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: state.range, dimension: "source", limit: 6 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: state.range, dimension: "project", limit: 6 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: state.range, dimension: "thread", limit: 6 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: "all", dimension: "source", limit: 100 })}`),
    api(`/api/v1/breakdown?${filterQuery({ since: "all", dimension: "project", limit: 100 })}`)
  ]);
  renderBars($("#modelBreakdown"), models.items || [], "暂无模型数据");
  renderDonut(agents.items || []);
  renderBars($("#sourceBreakdown"), sourceDisplay.items || [], "暂无来源数据");
  renderBars($("#projectBreakdown"), projectDisplay.items || [], "暂无项目归属数据");
  renderBars($("#threadBreakdown"), threadDisplay.items || [], "暂无 Thread 归属数据");
  hydrateSelect($("#filterModel"), models.items || [], "全部模型");
  hydrateSelect($("#filterSource"), sources.items || [], "全部来源");
  hydrateSelect($("#filterProject"), projects.items || [], "全部项目");
}

function renderBars(container, items, emptyLabel = "暂无数据") {
  if (!items.length) {
    container.innerHTML = `<div class="loading-cell">${escapeHTML(emptyLabel)}</div>`;
    return;
  }
  const max = Math.max(...items.map((item) => item.usage.total), 1);
  container.innerHTML = items.map((item) => `<div class="bar-row">
    <div class="bar-name" title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</div>
    <div class="bar-track"><div class="bar-fill" style="width:${Math.max(1, item.usage.total / max * 100)}%"></div></div>
    <div class="bar-value" title="${fullToken(item.usage.total)}">${formatToken(item.usage.total)}</div>
  </div>`).join("");
}

function renderDonut(items) {
  const total = items.reduce((sum, item) => sum + Number(item.usage.total || 0), 0);
  let cursor = 0;
  const stops = items.map((item, index) => {
    const start = cursor;
    cursor += total ? item.usage.total / total * 100 : 0;
    return `${colors[index % colors.length]} ${start}% ${cursor}%`;
  });
  $("#agentDonut").style.background = stops.length ? `conic-gradient(${stops.join(",")})` : "var(--panel-2)";
  $("#agentDonut strong").textContent = formatToken(total);
  $("#agentBreakdown").innerHTML = items.length ? items.map((item, index) => `<div class="donut-item">
    <i class="donut-swatch" style="background:${colors[index % colors.length]}"></i>
    <span>${escapeHTML(item.key)}</span><strong>${formatToken(item.usage.total)}</strong>
  </div>`).join("") : `<div class="loading-cell">暂无来源数据</div>`;
}

function hydrateSelect(select, items, placeholder) {
  const selected = select.value;
  const values = [...new Set(items.map((item) => item.key).filter((key) => key && key !== "未知"))];
  select.innerHTML = `<option value="">${placeholder}</option>` + values.map((value) =>
    `<option value="${escapeHTML(value)}">${escapeHTML(value)}</option>`).join("");
  if (values.includes(selected)) select.value = selected;
}

async function loadSessions() {
  const payload = await api(`/api/v1/sessions?${filterQuery({ since: state.range, limit: 100 })}`);
  const items = payload.items || [];
  $("#sessionRows").innerHTML = items.length ? items.map((item) => `<tr>
    <td><div class="cell-main"><strong title="${escapeHTML(item.title || item.session_id)}">${escapeHTML(item.title || "无标题 thread")}</strong><span title="${escapeHTML(item.session_id)}">${escapeHTML(shortId(item.session_id))}</span></div></td>
    <td><div class="path-cell" title="${escapeHTML(item.project_path || "未记录")}">${escapeHTML(item.project_path || "未记录")}</div></td>
    <td><div class="cell-main"><strong>${escapeHTML(item.model || "未知模型")}</strong><span>${escapeHTML(item.source || "未知来源")}</span></div></td>
    <td><span class="agent-badge">${escapeHTML(item.agent_type || "main")}</span></td>
    <td title="${fullToken(item.usage.total)}">${formatToken(item.usage.total)}</td>
    <td><span class="confidence-badge ${escapeHTML(item.confidence)}">${confidenceLabel(item.confidence)}</span></td>
    <td class="cell-sub">${escapeHTML(localTime(item.last_usage))}</td>
  </tr>`).join("") : `<tr><td colspan="7" class="loading-cell">当前筛选没有 session 数据</td></tr>`;
}

function confidenceLabel(value) {
  return ({ exact: "精确", gap_fallback: "缺口补位", aggregate_only: "仅累计" })[value] || value || "—";
}

async function loadWarnings() {
  const payload = await api("/api/v1/warnings?limit=200");
  const items = payload.items || [];
  $("#warningList").innerHTML = items.length ? items.map((item) => `<div class="warning-row">
    <span>${escapeHTML(localTime(item.created_at))}</span>
    <code title="${escapeHTML(item.kind)}">${escapeHTML(item.kind)}</code>
    <span title="${escapeHTML(item.path || "")}">${escapeHTML(item.detail)}</span>
  </div>`).join("") : `<div class="loading-cell">没有异常记录</div>`;
}

async function refreshData({ includeStatus = false } = {}) {
  try {
    if (includeStatus) await loadStatus();
    await Promise.all([loadCards(), loadTrend(), loadBreakdowns(), loadSessions()]);
  } catch (error) {
    toast(error.message, true);
  }
}

function toast(message, isError = false) {
  const node = $("#toast");
  node.textContent = message;
  node.style.borderColor = isError ? "rgba(255,133,124,.45)" : "";
  node.classList.remove("hidden");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => node.classList.add("hidden"), 4200);
}

function setupEvents() {
  $$(".segmented button").forEach((button) => button.addEventListener("click", () => {
    state.range = button.dataset.range;
    $$(".segmented button").forEach((b) => b.classList.toggle("active", b === button));
    refreshData();
  }));
  $$(".filter-bar select").forEach((select) => select.addEventListener("change", () => refreshData()));
  $("#resetFilters").addEventListener("click", () => {
    $$(".filter-bar select").forEach((select) => { select.value = ""; });
    refreshData();
  });
  $("#scanButton").addEventListener("click", async () => {
    const button = $("#scanButton");
    button.disabled = true;
    $(".scan-icon").classList.add("spin");
    try {
      const result = await api("/api/v1/rescan", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
      toast(`扫描完成：新增 ${result.events_inserted} 个事件，${result.duplicates} 个重复已忽略`);
      await refreshData({ includeStatus: true });
    } catch (error) { toast(error.message, true); }
    finally { button.disabled = false; $(".scan-icon").classList.remove("spin"); }
  });
  $("#warningButton").addEventListener("click", async () => {
    $("#warningsPanel").classList.remove("hidden");
    await loadWarnings();
    $("#warningsPanel").scrollIntoView({ behavior: "smooth" });
  });
  $("#closeWarnings").addEventListener("click", () => $("#warningsPanel").classList.add("hidden"));
  $("#exportButton").addEventListener("click", () => {
    const common = filterQuery({ since: state.range });
    $("#exportCsv").href = `/api/v1/export?${common}&format=csv`;
    $("#exportJson").href = `/api/v1/export?${common}&format=json`;
    $("#exportDialog").showModal();
  });
  $("#themeButton").addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "light" ? "dark" : "light";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("codex-usage-theme", next);
    requestAnimationFrame(() => renderChart(state.chartPoints));
  });
  window.addEventListener("resize", () => renderChart(state.chartPoints));
}

async function boot() {
  const savedTheme = localStorage.getItem("codex-usage-theme");
  if (savedTheme) document.documentElement.dataset.theme = savedTheme;
  setupEvents();
  await loadStatus().catch((error) => toast(error.message, true));
  await refreshData();
  setInterval(() => loadStatus().catch(() => {}), 30000);
  setInterval(() => refreshData({ includeStatus: true }), 60000);
}

boot();
