const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const FILTER_FIELDS = {
  model: { selector: "#filterModel", label: "模型" },
  source: { selector: "#filterSource", label: "来源" },
  agent_type: { selector: "#filterAgent", label: "Agent" },
  project: { selector: "#filterProject", label: "项目" },
  confidence: { selector: "#filterConfidence", label: "置信度" }
};
const DIMENSION_LABELS = { model: "模型", source: "来源", agent_type: "Agent", project: "项目", thread: "Thread" };
const RANGE_LABELS = { today: "今天", "7d": "过去 7 个本地自然日", "30d": "过去 30 个本地自然日", all: "本机可归属的全部历史" };
const state = {
  view: "overview",
  overviewRange: "7d",
  detailRange: "30d",
  detailDimension: "model",
  filters: {},
  status: null,
  overview: null,
  pulseDate: "",
  monthCursor: new Date(new Date().getFullYear(), new Date().getMonth(), 1),
  selectedDate: "",
  monthReport: null,
  dailyInitialSelection: true,
  pricing: null,
  dataRevision: null,
  statusQualityNotes: [],
  dataQualityNotes: [],
  requestSerial: { overview: 0, daily: 0, day: 0, details: 0 }
};

const responseCache = new Map();
const inflightRequests = new Map();
const DATA_CACHE_TTL = 5 * 60_000;
let cacheGeneration = 0;

const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;
const pad2 = (value) => String(value).padStart(2, "0");
const dateKey = (date) => `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
const dateFromKey = (value) => {
  const [year, month, day] = String(value).split("-").map(Number);
  return new Date(year, month - 1, day);
};
const addDays = (value, amount) => {
  const date = typeof value === "string" ? dateFromKey(value) : new Date(value);
  date.setDate(date.getDate() + amount);
  return date;
};
const todayKey = () => dateKey(new Date());
const emptyUsage = () => ({ input: 0, cached_input: 0, cache_write_input: 0, output: 0, reasoning_output: 0, total: 0 });
const usageTotal = (usage = {}) => Number(usage.total || (Number(usage.input || 0) + Number(usage.output || 0)) || 0);

function rangeBounds(range) {
  if (range === "all") return { label: RANGE_LABELS.all };
  const today = dateFromKey(todayKey());
  const days = range === "today" ? 1 : range === "7d" ? 7 : 30;
  return {
    since: dateKey(addDays(today, -(days - 1))),
    until: dateKey(addDays(today, 1)),
    label: RANGE_LABELS[range]
  };
}

function monthBounds(cursor = state.monthCursor) {
  const start = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
  const end = new Date(cursor.getFullYear(), cursor.getMonth() + 1, 1);
  return { since: dateKey(start), until: dateKey(end) };
}

function currentBounds() {
  if (state.view === "daily") return monthBounds();
  if (state.view === "details") return rangeBounds(state.detailRange);
  return rangeBounds(state.overviewRange);
}

const formatToken = (value = 0) => {
  const number = Number(value || 0);
  const absolute = Math.abs(number);
  if (absolute >= 1e12) return `${(number / 1e12).toFixed(absolute >= 1e13 ? 1 : 2)}T`;
  if (absolute >= 1e9) return `${(number / 1e9).toFixed(absolute >= 1e10 ? 1 : 2)}B`;
  if (absolute >= 1e6) return `${(number / 1e6).toFixed(absolute >= 1e7 ? 1 : 2)}M`;
  if (absolute >= 1e3) return `${(number / 1e3).toFixed(absolute >= 1e4 ? 1 : 2)}K`;
  return number.toLocaleString("zh-CN");
};
const fullToken = (value = 0) => Number(value || 0).toLocaleString("zh-CN");
const formatUSD = (value = "0") => {
  const amount = Number(value || 0);
  if (!Number.isFinite(amount)) return "—";
  const digits = amount >= 100 ? 2 : amount >= 1 ? 2 : amount >= .01 ? 3 : 6;
  return `$${amount.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: digits })}`;
};
const formatPercent = (value = 0) => `${(Number(value || 0) * 100).toFixed(1)}%`;
const localTime = (value) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
const shortId = (value = "") => value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value || "—";
const shortPath = (value = "") => {
  const parts = String(value).split(/[\\/]/).filter(Boolean);
  return parts.at(-1) || value || "未记录";
};
const escapeHTML = (value = "") => String(value).replace(/[&<>"']/g, (char) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[char]));
const confidenceLabel = (value) => ({ exact: "精确", gap_fallback: "缺口补位", aggregate_only: "仅累计" })[value] || value || "—";

async function api(path, options = {}) {
  const method = String(options.method || "GET").toUpperCase();
  const cacheable = method === "GET" && !path.startsWith("/api/v1/status") && !path.startsWith("/api/v1/warnings");
  const requestGeneration = cacheGeneration;
  const key = `${requestGeneration}:${method} ${path}`;
  if (cacheable) {
    const cached = responseCache.get(key);
    if (cached && cached.expires > Date.now()) return cached.payload;
    if (inflightRequests.has(key)) return inflightRequests.get(key);
  }
  const request = (async () => {
    const response = await fetch(path, { cache: "no-store", ...options });
    const body = await response.text();
    let payload = null;
    if (body) {
      try { payload = JSON.parse(body); } catch { payload = body; }
    }
    if (!response.ok) {
      const message = payload && typeof payload === "object" ? payload.error : payload;
      throw new Error(message || `${response.status} ${response.statusText}`);
    }
    if (cacheable && requestGeneration === cacheGeneration) responseCache.set(key, { payload, expires: Date.now() + DATA_CACHE_TTL });
    return payload;
  })();
  if (cacheable) inflightRequests.set(key, request);
  try {
    return await request;
  } finally {
    if (cacheable) inflightRequests.delete(key);
  }
}

function invalidateDataCache() {
  cacheGeneration += 1;
  responseCache.clear();
}

function filterQuery(extra = {}, filters = state.filters) {
  const query = new URLSearchParams();
  const values = { ...filters, ...extra };
  for (const [key, value] of Object.entries(values)) {
    if (key === "label") continue;
    if (value !== undefined && value !== null && value !== "" && value !== "all") query.set(key, value);
  }
  return query.toString();
}

function apiURL(path, extra = {}, filters = state.filters) {
  const query = filterQuery(extra, filters);
  return query ? `${path}?${query}` : path;
}

function estimateTokens(estimate = {}) {
  return Number(estimate.priced_tokens || 0) + Number(estimate.unpriced_tokens || 0);
}

function estimateLabel(estimate = {}) {
  const total = estimateTokens(estimate);
  if (!total) return "暂无可估算 Token";
  const base = `已定价 ${formatPercent(estimate.coverage_ratio)} Token`;
  return estimate.unpriced_tokens ? `${base} · ${formatToken(estimate.unpriced_tokens)} 未定价` : base;
}

function estimateDisplay(estimate = {}) {
  return estimateTokens(estimate) ? formatUSD(estimate.usd) : "—";
}

function reasonSummary(estimate = {}) {
  const labels = {
    unknown_model: "未知模型",
    missing_token_categories: "只有累计总量",
    invalid_token_categories: "字段矛盾",
    cache_write_rate_missing: "Cache Write 单价未公开",
    long_context_uncertain: "长上下文请求边界不确定"
  };
  const reasons = estimate.reasons || [];
  if (!reasons.length) return "";
  return [...new Set(reasons.map((reason) => labels[reason.kind] || reason.kind))].join("、");
}

function toast(message, isError = false) {
  const node = $("#toast");
  node.textContent = message;
  node.classList.toggle("error", isError);
  node.classList.remove("hidden");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => node.classList.add("hidden"), 4200);
}

function renderQualityBanner() {
  const banner = $("#coverageBanner");
  const notes = [...new Set([...state.statusQualityNotes, ...state.dataQualityNotes].filter(Boolean))];
  banner.classList.toggle("hidden", notes.length === 0);
  $("#coverageText").textContent = notes.join("；");
}

async function loadStatus() {
  const payload = await api("/api/v1/status");
  state.status = payload;
  const status = payload.status || {};
  const revision = status.data_revision == null ? null : String(status.data_revision);
  const changed = state.dataRevision !== null && revision !== null && revision !== state.dataRevision;
  if (changed) invalidateDataCache();
  state.dataRevision = revision;
  const machine = status.machine || {};
  $("#machineLabel").textContent = machine.label || machine.hostname || "本机";
  $("#machinePill").title = `当前电脑：${machine.label || machine.hostname || "本机"}`;
  $("#machinePill").setAttribute("aria-label", `当前电脑：${machine.label || machine.hostname || "本机"}`);
  $("#machineId").textContent = shortId(machine.id || "");
  $("#machineId").title = machine.id || "";
  $("#lastScan").textContent = localTime(status.last_scan);
  $("#otelStatus").textContent = status.otel_active
    ? `实时 · ${localTime(status.otel_last_received)}`
    : status.otel_last_received ? `最近 ${localTime(status.otel_last_received)}` : "未收到 · 历史扫描可用";
  $("#otelDot").className = `status-dot ${status.otel_active ? "live" : status.otel_last_received ? "warn" : "neutral"}`;
  $("#versionLabel").textContent = `v${payload.version || "—"}`;
  $("#scanButton").disabled = Boolean(payload.scanning);
  $(".scan-icon").classList.toggle("spin", Boolean(payload.scanning));

  const notes = [];
  if (status.warning_count) notes.push(`${status.warning_count} 条去重后的数据质量记录需复核`);
  if ((status.coverage_gaps || []).length) {
    const open = status.coverage_gaps.some((gap) => gap.open);
    notes.push(`记录到 ${status.coverage_gaps.length} 段 OTel 覆盖缺口${open ? "，当前仍未恢复" : ""}`);
  }
  for (const home of status.codex_homes || []) if (home.warning) notes.push(home.warning);
  state.statusQualityNotes = notes;
  renderQualityBanner();
  return changed;
}

function hydrateSelect(selector, items, placeholder) {
  const select = $(selector);
  const selected = select.value || state.filters[Object.entries(FILTER_FIELDS).find(([, item]) => item.selector === selector)?.[0]] || "";
  const values = [...new Set((items || []).map((item) => item.key).filter((key) => key && key !== "未知"))];
  select.innerHTML = `<option value="">${escapeHTML(placeholder)}</option>${values.map((value) => `<option value="${escapeHTML(value)}">${escapeHTML(value)}</option>`).join("")}`;
  if (values.includes(selected)) select.value = selected;
}

async function loadFilterOptions() {
  try {
    const [models, sources, projects] = await Promise.all([
      api(apiURL("/api/v1/breakdown", { dimension: "model", limit: 500 }, {})),
      api(apiURL("/api/v1/breakdown", { dimension: "source", limit: 500 }, {})),
      api(apiURL("/api/v1/breakdown", { dimension: "project", limit: 500 }, {}))
    ]);
    hydrateSelect("#filterModel", models.items, "全部模型");
    hydrateSelect("#filterSource", sources.items, "全部来源");
    hydrateSelect("#filterProject", projects.items, "全部项目");
  } catch (error) {
    toast(`筛选选项读取失败：${error.message}`, true);
  }
}

function renderFilterChips() {
  const entries = Object.entries(state.filters).filter(([, value]) => value);
  $("#filterContext").classList.toggle("hidden", entries.length === 0);
  $("#filterCount").classList.toggle("hidden", entries.length === 0);
  $("#filterCount").textContent = entries.length;
  $("#filterChips").innerHTML = entries.map(([key, value]) => {
    const meta = FILTER_FIELDS[key];
    const display = key === "project" ? shortPath(value) : key === "confidence" ? confidenceLabel(value) : value;
    return `<span class="filter-chip" title="${escapeHTML(value)}"><span>${escapeHTML(meta.label)} · ${escapeHTML(display)}</span><button class="pressable" type="button" data-remove-filter="${escapeHTML(key)}" aria-label="移除${escapeHTML(meta.label)}筛选">×</button></span>`;
  }).join("");
  $$('[data-remove-filter]').forEach((button) => button.addEventListener("click", () => {
    delete state.filters[button.dataset.removeFilter];
    resetDataSelections();
    renderFilterChips();
    syncFilterForm();
    loadCurrentView();
  }));
}

function syncFilterForm() {
  for (const [key, meta] of Object.entries(FILTER_FIELDS)) $(meta.selector).value = state.filters[key] || "";
}

function resetDataSelections() {
  state.pulseDate = "";
  state.selectedDate = "";
  state.dailyInitialSelection = true;
}

function applyFiltersFromForm() {
  const filters = {};
  for (const [key, meta] of Object.entries(FILTER_FIELDS)) {
    const value = $(meta.selector).value;
    if (value) filters[key] = value;
  }
  state.filters = filters;
  resetDataSelections();
  renderFilterChips();
  closeDialog($("#filterSheet"));
  loadCurrentView();
}

function clearAllFilters() {
  state.filters = {};
  resetDataSelections();
  syncFilterForm();
  renderFilterChips();
  loadCurrentView();
}

function setLoading(element, label = null) {
  if (label !== null || !element.textContent.trim()) element.textContent = label ?? "—";
  element.classList.add("loading");
}

async function loadOverview({ preserve = false } = {}) {
  const serial = ++state.requestSerial.overview;
  const bounds = rangeBounds(state.overviewRange);
  $("#overviewSubtitle").textContent = bounds.label;
  if (!preserve) {
    setLoading($("#overviewTotal"));
    setLoading($("#overviewCost"));
  }
  $("#view-overview").setAttribute("aria-busy", "true");
  let firstError = null;
  await Promise.all([
    api(apiURL("/api/v1/summary", bounds)).then((summary) => {
      if (serial !== state.requestSerial.overview) return;
      state.overview = { ...(state.overview || {}), summary };
      renderOverviewSummary(summary);
    }).catch((error) => { firstError ||= error; }),
    api(apiURL("/api/v1/cost-estimate", { ...bounds, bucket: "day" })).then((cost) => {
      if (serial !== state.requestSerial.overview) return;
      state.overview = { ...(state.overview || {}), cost };
      renderOverviewCost(cost);
    }).catch((error) => { firstError ||= error; })
  ]);
  if (serial !== state.requestSerial.overview) return;
  $("#view-overview").removeAttribute("aria-busy");
  if (firstError) toast(`概览读取失败：${firstError.message}`, true);
}

function renderOverviewSummary(summary) {
  const total = Number(summary.grand_total ?? summary.usage?.total ?? 0);
  const totalNode = $("#overviewTotal");
  totalNode.textContent = formatToken(total);
  totalNode.title = `${fullToken(total)} Total Token`;
  totalNode.classList.remove("loading");
  const usage = summary.usage || emptyUsage();
  $("#overviewTokenBreakdown").innerHTML = [
    ["Input", usage.input], ["Cached", usage.cached_input], ["Cache Write", usage.cache_write_input],
    ["Output", usage.output], ["Reasoning", usage.reasoning_output]
  ].map(([label, value]) => `<span>${label}<b title="${fullToken(value)}">${formatToken(value)}</b></span>`).join("");
  const unattributed = Number(summary.unattributed?.total || 0);
  $("#unattributed").textContent = formatToken(unattributed);
  $("#unattributed").title = `${fullToken(unattributed)} Token 只归入历史累计`;
  state.dataQualityNotes = unattributed ? [`有 ${fullToken(unattributed)} Token 只能归入历史累计，未分摊到日期`] : [];
  renderQualityBanner();
}

function renderOverviewCost(cost) {
  const estimate = cost.summary || {};
  const costNode = $("#overviewCost");
  costNode.textContent = estimateDisplay(estimate);
  costNode.classList.remove("loading");
  $("#overviewCoverage").textContent = estimateLabel(estimate);
  $("#overviewCoverageBar").style.width = `${Math.max(0, Math.min(100, Number(estimate.coverage_ratio || 0) * 100))}%`;
  const reasons = reasonSummary(estimate);
  $("#overviewCostNote").textContent = reasons
    ? `未覆盖：${reasons} · 不是 OpenAI 真实账单`
    : `价格核对于 ${cost.catalog_as_of || "—"} · 不是 OpenAI 真实账单`;
  renderPulse(cost.points || []);
  renderOverviewModels(cost.models || []);
}

function renderPulse(allPoints) {
  let points = allPoints;
  const limited = points.length > 90;
  if (limited) points = points.slice(-90);
  $("#pulseCaption").textContent = limited ? `展示最近 90 个连续自然日 · 其余日期保留在统计中` : "连续本地自然日 · 零用量日保留位置";
  const belt = $("#pulseBelt");
  if (!points.length) {
    belt.style.minWidth = "100%";
    belt.innerHTML = `<div class="empty-state">当前范围没有可按日期归属的数据</div>`;
    renderPulseInspector(null);
    return;
  }
  const max = Math.max(...points.map((point) => usageTotal(point.usage)), 1);
  const availableDates = new Set(points.map((point) => point.date));
  if (!state.pulseDate || !availableDates.has(state.pulseDate)) {
    state.pulseDate = [...points].reverse().find((point) => usageTotal(point.usage) > 0)?.date || points.at(-1).date;
  }
  belt.style.minWidth = points.length > 18 ? `${points.length * 38}px` : "100%";
  belt.innerHTML = points.map((point) => {
    const total = usageTotal(point.usage);
    const height = total ? Math.max(5, total / max * 118) : 2;
    const selected = point.date === state.pulseDate;
    const date = dateFromKey(point.date);
    const label = `${date.toLocaleDateString("zh-CN", { month: "long", day: "numeric" })}，${fullToken(total)} Token，${estimateLabel(point.estimate)}`;
    return `<button class="pulse-day pressable ${selected ? "selected" : ""} ${total ? "" : "zero"}" type="button" role="option" data-pulse-date="${point.date}" aria-selected="${selected}" tabindex="${selected ? "0" : "-1"}" aria-label="${escapeHTML(label)}"><span class="pulse-bar-space"><i class="pulse-bar" style="--pulse-height:${height}px"></i></span><time datetime="${point.date}">${pad2(date.getMonth() + 1)}/${pad2(date.getDate())}</time></button>`;
  }).join("");
  const selectPoint = (date) => {
    state.pulseDate = date;
    $$('[data-pulse-date]', belt).forEach((button) => {
      const selected = button.dataset.pulseDate === date;
      button.classList.toggle("selected", selected);
      button.setAttribute("aria-selected", String(selected));
      button.tabIndex = selected ? 0 : -1;
    });
    renderPulseInspector(points.find((point) => point.date === date));
  };
  $$('[data-pulse-date]', belt).forEach((button) => {
    button.addEventListener("click", () => selectPoint(button.dataset.pulseDate));
    button.addEventListener("focus", () => selectPoint(button.dataset.pulseDate));
    button.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const buttons = $$('[data-pulse-date]', belt);
      const current = buttons.indexOf(button);
      const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : Math.max(0, Math.min(buttons.length - 1, current + (event.key === "ArrowRight" ? 1 : -1)));
      buttons[next].focus();
    });
  });
  renderPulseInspector(points.find((point) => point.date === state.pulseDate));
  requestAnimationFrame(() => {
    const selected = $('[data-pulse-date].selected', belt);
    const scroller = $("#pulseScroll");
    if (selected && selected.offsetLeft + selected.offsetWidth > scroller.clientWidth) {
      scroller.scrollTo({ left: selected.offsetLeft - scroller.clientWidth + selected.offsetWidth + 8, behavior: reducedMotion() ? "auto" : "smooth" });
    }
  });
}

function renderPulseInspector(point) {
  if (!point) {
    for (const selector of ["#pulseDate", "#pulseTotal", "#pulseIO", "#pulseCost"]) $(selector).textContent = "—";
    return;
  }
  const date = dateFromKey(point.date);
  $("#pulseDate").textContent = date.toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "short" });
  $("#pulseTotal").textContent = formatToken(usageTotal(point.usage));
  $("#pulseTotal").title = fullToken(usageTotal(point.usage));
  $("#pulseIO").textContent = `${formatToken(point.usage?.input)} / ${formatToken(point.usage?.output)}`;
  $("#pulseCost").textContent = `${estimateDisplay(point.estimate)} · ${formatPercent(point.estimate?.coverage_ratio || 0)}`;
}

function renderOverviewModels(models) {
  $("#modelCount").textContent = models.length ? `${models.length} 个模型` : "无模型数据";
  const container = $("#overviewModels");
  if (!models.length) {
    container.innerHTML = `<div class="empty-state">当前范围暂无模型数据</div>`;
    return;
  }
  const visible = models.slice(0, 6);
  const max = Math.max(...visible.map((item) => usageTotal(item.usage)), 1);
  container.innerHTML = visible.map((item) => {
    const total = usageTotal(item.usage);
    const cost = estimateDisplay(item.estimate);
    const coverage = estimateTokens(item.estimate) ? formatPercent(item.estimate.coverage_ratio) : "未定价";
    return `<div class="rank-row"><span class="rank-name" title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><span class="rank-signal" aria-hidden="true"><i style="width:${Math.max(1, total / max * 100)}%"></i></span><span class="rank-value" title="${fullToken(total)} Token">${formatToken(total)}<small>${cost} · ${coverage}</small></span></div>`;
  }).join("");
}

async function loadDaily({ preserve = false } = {}) {
  const serial = ++state.requestSerial.daily;
  const bounds = monthBounds();
  $("#monthLabel").textContent = state.monthCursor.toLocaleDateString("zh-CN", { year: "numeric", month: "long" });
  if (!preserve) $("#calendarGrid").innerHTML = `<div class="empty-state">正在读取这个月…</div>`;
  $("#view-daily").setAttribute("aria-busy", "true");
  try {
    const report = await api(apiURL("/api/v1/cost-estimate", { ...bounds, bucket: "day" }));
    if (serial !== state.requestSerial.daily) return;
    const nonzero = (report.points || []).filter((point) => usageTotal(point.usage) > 0);
    if (state.dailyInitialSelection && !nonzero.some((point) => point.date === todayKey()) && !nonzero.length) {
      // Finding the latest active day only needs the lightweight token series;
      // an all-history cost estimate would unnecessarily price every event.
      const history = await api(apiURL("/api/v1/timeseries", { bucket: "day" }));
      if (serial !== state.requestSerial.daily) return;
      const latestPoint = [...(history.points || [])].reverse().find((point) => usageTotal(point.usage) > 0);
      const latest = latestPoint ? { date: dateKey(new Date(latestPoint.time)) } : null;
      state.dailyInitialSelection = false;
      if (latest && !latest.date.startsWith(bounds.since.slice(0, 7))) {
        const latestDate = dateFromKey(latest.date);
        state.monthCursor = new Date(latestDate.getFullYear(), latestDate.getMonth(), 1);
        state.selectedDate = latest.date;
        await loadDaily();
        return;
      }
    }
    state.dailyInitialSelection = false;
    state.monthReport = report;
    chooseDailySelection(report.points || []);
    renderCalendar(report);
    if (state.selectedDate) loadDailySelection(state.selectedDate);
  } catch (error) {
    if (serial === state.requestSerial.daily) toast(`每日统计读取失败：${error.message}`, true);
  } finally {
    if (serial === state.requestSerial.daily) $("#view-daily").removeAttribute("aria-busy");
  }
}

function chooseDailySelection(points) {
  const dates = new Set(points.map((point) => point.date));
  if (state.selectedDate && dates.has(state.selectedDate)) return;
  const today = points.find((point) => point.date === todayKey());
  if (today && usageTotal(today.usage) > 0) {
    state.selectedDate = today.date;
    return;
  }
  state.selectedDate = [...points].reverse().find((point) => usageTotal(point.usage) > 0)?.date || today?.date || points[0]?.date || "";
}

function renderCalendar(report) {
  const points = report.points || [];
  const byDate = new Map(points.map((point) => [point.date, point]));
  const max = Math.max(...points.map((point) => usageTotal(point.usage)), 1);
  const first = new Date(state.monthCursor.getFullYear(), state.monthCursor.getMonth(), 1);
  const daysInMonth = new Date(first.getFullYear(), first.getMonth() + 1, 0).getDate();
  const leading = (first.getDay() + 6) % 7;
  const cells = [];
  for (let index = 0; index < leading; index++) cells.push(`<span class="calendar-blank" aria-hidden="true"></span>`);
  for (let day = 1; day <= daysInMonth; day++) {
    const date = dateKey(new Date(first.getFullYear(), first.getMonth(), day));
    const point = byDate.get(date) || { date, usage: emptyUsage(), estimate: {} };
    const total = usageTotal(point.usage);
    const strength = total ? Math.max(5, total / max * 100) : 0;
    const selected = date === state.selectedDate;
    const hasCost = Number(point.estimate?.priced_tokens || 0) > 0;
    cells.push(`<button class="calendar-day pressable ${selected ? "selected" : ""} ${date === todayKey() ? "today" : ""} ${total ? "" : "zero"}" type="button" role="gridcell" data-calendar-date="${date}" aria-selected="${selected}" tabindex="${selected ? "0" : "-1"}" aria-label="${day} 日，${fullToken(total)} Token，${estimateLabel(point.estimate)}"><span class="day-number">${day}</span>${hasCost ? '<i class="cost-marker" aria-hidden="true"></i>' : ""}<span class="day-usage" aria-hidden="true"><i style="--day-strength:${strength}%"></i></span><span class="day-amount">${total ? formatToken(total) : "0"}</span></button>`);
  }
  while (cells.length % 7) cells.push(`<span class="calendar-blank" aria-hidden="true"></span>`);
  const grid = $("#calendarGrid");
  grid.innerHTML = cells.join("");
  const monthlyTokens = usageTotal(report.summary?.usage) || points.reduce((sum, point) => sum + usageTotal(point.usage), 0);
  $("#monthSummary").textContent = `${formatToken(monthlyTokens)} Token · ${estimateDisplay(report.summary)}`;
  $$('[data-calendar-date]', grid).forEach((button) => {
    button.addEventListener("click", () => selectCalendarDate(button.dataset.calendarDate));
    button.addEventListener("focus", () => selectCalendarDate(button.dataset.calendarDate));
    button.addEventListener("keydown", calendarKeydown);
  });
}

function calendarKeydown(event) {
  const offsets = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
  if (!(event.key in offsets) && event.key !== "Home" && event.key !== "End") return;
  event.preventDefault();
  const buttons = $$('[data-calendar-date]', $("#calendarGrid"));
  let targetDate;
  if (event.key === "Home") targetDate = buttons[0]?.dataset.calendarDate;
  else if (event.key === "End") targetDate = buttons.at(-1)?.dataset.calendarDate;
  else targetDate = dateKey(addDays(event.currentTarget.dataset.calendarDate, offsets[event.key]));
  const target = buttons.find((button) => button.dataset.calendarDate === targetDate);
  target?.focus();
}

function selectCalendarDate(date) {
  if (!date || date === state.selectedDate) return;
  state.selectedDate = date;
  $$('[data-calendar-date]').forEach((button) => {
    const selected = button.dataset.calendarDate === date;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-selected", String(selected));
    button.tabIndex = selected ? 0 : -1;
  });
  const point = state.monthReport?.points?.find((item) => item.date === date);
  renderDayDetail(point || { date, usage: emptyUsage(), estimate: {} }, null);
  loadDailySelection(date);
}

async function loadDailySelection(date) {
  const serial = ++state.requestSerial.day;
  const point = state.monthReport?.points?.find((item) => item.date === date) || { date, usage: emptyUsage(), estimate: {} };
  renderDayDetail(point, null, true);
  try {
    const report = await api(apiURL("/api/v1/cost-estimate", { since: date, until: dateKey(addDays(date, 1)), bucket: "day" }));
    if (serial !== state.requestSerial.day || date !== state.selectedDate) return;
    renderDayDetail(report.points?.find((item) => item.date === date) || point, report);
  } catch (error) {
    if (serial === state.requestSerial.day) toast(`选中日期读取失败：${error.message}`, true);
  }
}

function renderDayDetail(point, report, loadingModels = false) {
  const date = dateFromKey(point.date);
  const usage = point.usage || emptyUsage();
  const total = usageTotal(usage);
  const estimate = point.estimate || {};
  const title = date.toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "long" });
  $("#selectedDateTitle").textContent = title;
  $("#selectedDateTitle").dataset.loadedDate = report ? point.date : "";
  $("#selectedDayStatus").textContent = total ? "有用量" : "零用量";
  $("#dayTotal").textContent = formatToken(total);
  $("#dayTotal").title = `${fullToken(total)} Token`;
  const regular = Math.max(0, Number(usage.input || 0) - Number(usage.cached_input || 0) - Number(usage.cache_write_input || 0));
  const parts = [
    ["regular", regular, "Regular Input"], ["cached", usage.cached_input, "Cached Input"],
    ["write", usage.cache_write_input, "Cache Write"], ["output", usage.output, "Output"]
  ];
  $("#dayComposition").innerHTML = total ? parts.filter(([, value]) => Number(value || 0) > 0).map(([className, value, label]) => `<i class="${className}" style="width:${Number(value) / total * 100}%" title="${label} ${fullToken(value)}"></i>`).join("") : "";
  $("#dayLedger").innerHTML = [
    ["Input", usage.input], ["Cached", usage.cached_input], ["Cache Write", usage.cache_write_input],
    ["Output", usage.output], ["Reasoning ⊂ Output", usage.reasoning_output]
  ].map(([label, value]) => `<div><dt>${label}</dt><dd title="${fullToken(value)}">${formatToken(value)}</dd></div>`).join("");
  $("#dayCost").textContent = estimateDisplay(estimate);
  $("#dayCoverage").textContent = estimateLabel(estimate);
  if (loadingModels) {
    $("#dayModels").innerHTML = `<div class="empty-state">正在读取模型构成…</div>`;
    $("#dayModelCount").textContent = "";
    return;
  }
  const models = report?.models || [];
  $("#dayModelCount").textContent = models.length ? `${models.length} 个` : "";
  $("#dayModels").innerHTML = models.length ? models.map((item) => `<div class="mini-model-row"><span title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><strong title="${fullToken(usageTotal(item.usage))}">${formatToken(usageTotal(item.usage))}</strong></div>`).join("") : `<div class="empty-state">这一天没有模型用量</div>`;
}

async function loadDetails({ preserve = false } = {}) {
  const serial = ++state.requestSerial.details;
  const bounds = rangeBounds(state.detailRange);
  const dimension = state.detailDimension;
  $("#breakdownTitle").textContent = `按${DIMENSION_LABELS[dimension]}`;
  if (!preserve) {
    $("#detailBreakdown").innerHTML = `<div class="empty-state">正在读取${DIMENSION_LABELS[dimension]}归属…</div>`;
    $("#sessionRows").innerHTML = `<div class="empty-state">正在加载本机 Session…</div>`;
  }
  $("#view-details").setAttribute("aria-busy", "true");
  try {
    const [breakdown, sessions] = await Promise.all([
      api(apiURL("/api/v1/breakdown", { ...bounds, dimension, limit: 100 })),
      api(apiURL("/api/v1/sessions", { ...bounds, limit: 100, compact: 1 }))
    ]);
    if (serial !== state.requestSerial.details) return;
    renderBreakdown(breakdown.items || [], dimension);
    renderSessions(sessions.items || []);
  } catch (error) {
    if (serial === state.requestSerial.details) toast(`明细读取失败：${error.message}`, true);
  } finally {
    if (serial === state.requestSerial.details) $("#view-details").removeAttribute("aria-busy");
  }
}

function renderBreakdown(items, dimension) {
  const container = $("#detailBreakdown");
  const total = items.reduce((sum, item) => sum + usageTotal(item.usage), 0);
  $("#breakdownMeta").textContent = items.length ? `${items.length} 项 · ${formatToken(total)} Token` : "无数据";
  if (!items.length) {
    container.innerHTML = `<div class="empty-state">当前范围没有${DIMENSION_LABELS[dimension]}数据</div>`;
    return;
  }
  const max = Math.max(...items.map((item) => usageTotal(item.usage)), 1);
  const canFilter = dimension !== "thread";
  container.innerHTML = items.map((item) => {
    const totalTokens = usageTotal(item.usage);
    return `<div class="breakdown-row"><span class="breakdown-name" title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><span class="breakdown-track" aria-hidden="true"><i style="width:${Math.max(1, totalTokens / max * 100)}%"></i></span><span class="breakdown-value" title="${fullToken(totalTokens)} Token">${formatToken(totalTokens)}</span>${canFilter ? `<button class="breakdown-filter pressable" type="button" data-drill-value="${escapeHTML(item.key)}" title="仅查看此${DIMENSION_LABELS[dimension]}" aria-label="筛选为 ${escapeHTML(item.key)}">＋</button>` : "<span></span>"}</div>`;
  }).join("");
  $$('[data-drill-value]', container).forEach((button) => button.addEventListener("click", () => {
    state.filters[dimension] = button.dataset.drillValue;
    renderFilterChips();
    syncFilterForm();
    resetDataSelections();
    loadDetails();
  }));
}

function renderSessions(items) {
  const container = $("#sessionRows");
  if (!items.length) {
    container.innerHTML = `<div class="empty-state">当前范围没有 Session 数据</div>`;
    return;
  }
  container.innerHTML = items.map((item) => `<article class="session-row">
    <div class="session-cell"><strong title="${escapeHTML(item.title || item.session_id)}">${escapeHTML(item.title || "无标题 Thread")}</strong><small title="${escapeHTML(item.session_id)}">${escapeHTML(shortId(item.session_id))}</small></div>
    <div class="session-cell"><span title="${escapeHTML(item.project_path || "未记录")}">${escapeHTML(shortPath(item.project_path))}</span><small title="${escapeHTML(item.project_path || "")}">${escapeHTML(item.project_path || "未记录")}</small></div>
    <div class="session-cell"><strong title="${escapeHTML(item.model || "未知模型")}">${escapeHTML(item.model || "未知模型")}</strong><span>${escapeHTML(item.source || "未知来源")}</span></div>
    <div class="session-cell"><span class="agent-badge">${escapeHTML(item.agent_type || "main")}</span><span class="confidence-badge ${escapeHTML(item.confidence)}">${confidenceLabel(item.confidence)}</span></div>
    <div class="session-token" title="${fullToken(usageTotal(item.usage))} Token">${formatToken(usageTotal(item.usage))}</div>
    <div class="session-cell"><span>${escapeHTML(localTime(item.last_usage))}</span></div>
  </article>`).join("");
}

async function loadCurrentView(options = {}) {
  if (state.view === "daily") return loadDaily(options);
  if (state.view === "details") return loadDetails(options);
  return loadOverview(options);
}

async function switchView(next) {
  if (!next || next === state.view) return;
  const currentPanel = $(`[data-view-panel="${state.view}"]`);
  const nextPanel = $(`[data-view-panel="${next}"]`);
  state.view = next;
  $$('.nav-tab').forEach((tab) => {
    const selected = tab.dataset.view === next;
    tab.classList.toggle("active", selected);
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
  });
  history.replaceState(null, "", `#${next}`);
  loadCurrentView();
  currentPanel.classList.add("leaving");
  await new Promise((resolve) => setTimeout(resolve, reducedMotion() ? 0 : 180));
  currentPanel.hidden = true;
  currentPanel.classList.remove("active", "leaving");
  nextPanel.hidden = false;
  nextPanel.classList.add("active", "entering");
  requestAnimationFrame(() => setTimeout(() => nextPanel.classList.remove("entering"), reducedMotion() ? 0 : 220));
}

function openDialog(dialog) {
  if (dialog.open) return;
  dialog.showModal();
  requestAnimationFrame(() => dialog.classList.add("is-open"));
}

function closeDialog(dialog) {
  if (!dialog?.open || dialog.classList.contains("is-closing")) return;
  dialog.classList.remove("is-open");
  dialog.classList.add("is-closing");
  setTimeout(() => {
    dialog.close();
    dialog.classList.remove("is-closing");
  }, reducedMotion() ? 0 : 200);
}

async function loadWarnings() {
  const container = $("#warningList");
  container.innerHTML = `<div class="empty-state">正在读取…</div>`;
  try {
    const payload = await api("/api/v1/warnings?limit=200");
    const items = payload.items || [];
    const labels = {
      state_fallback_suppressed_otel: "防止 OTel 重复计数",
      cumulative_reset: "累计快照回退补位",
      timestamp: "时间戳无法归属",
      jsonl_record: "JSONL 记录无法解析"
    };
    container.innerHTML = items.length ? items.map((item) => {
      const occurrences = Number(item.occurrences || 1);
      const firstSeen = item.first_seen || item.created_at;
      const period = occurrences > 1 ? `首次 ${localTime(firstSeen)} · 累计 ${fullToken(occurrences)} 次` : "首次出现";
      const informational = item.kind === "state_fallback_suppressed_otel" ? " informational" : "";
      return `<article class="warning-row${informational}"><time>最近 ${escapeHTML(localTime(item.created_at))}<small>${escapeHTML(period)}</small></time><code title="${escapeHTML(item.kind)}">${escapeHTML(labels[item.kind] || item.kind)}</code><p title="${escapeHTML(item.path || "")}">${escapeHTML(item.detail)}</p></article>`;
    }).join("") : `<div class="empty-state">没有数据质量记录</div>`;
  } catch (error) {
    container.innerHTML = `<div class="empty-state">${escapeHTML(error.message)}</div>`;
  }
}

function setExportLinks() {
  const bounds = currentBounds();
  $("#exportCsv").href = apiURL("/api/v1/export", { ...bounds, format: "csv" });
  $("#exportJson").href = apiURL("/api/v1/export", { ...bounds, format: "json" });
}

async function openPricingDialog() {
  openDialog($("#pricingDialog"));
  $("#pricingOverrideList").innerHTML = `<div class="empty-state">正在读取本机定价…</div>`;
  try {
    state.pricing = await api("/api/v1/pricing");
    renderPricing(state.pricing);
  } catch (error) {
    $("#pricingOverrideList").innerHTML = `<div class="empty-state">${escapeHTML(error.message)}</div>`;
  }
}

function renderPricing(payload) {
  const unpriced = payload.unpriced_models || [];
  $("#pricingMeta").textContent = `币种 ${payload.currency} · Standard 文本价格核对于 ${payload.catalog_as_of} · 当前观察到 ${unpriced.length} 个未定价模型`;
  renderCatalog(payload.catalog || []);
  const overrides = payload.overrides || {};
  const models = new Set([...unpriced.map((item) => item.key), ...Object.keys(overrides)]);
  const observed = new Map(unpriced.map((item) => [item.key, usageTotal(item.usage)]));
  const list = $("#pricingOverrideList");
  list.innerHTML = "";
  for (const model of models) {
    if (!model) continue;
    if (model === "未知") {
      const tokens = observed.get(model) || 0;
      list.insertAdjacentHTML("beforeend", `<article class="override-card"><div class="override-card-head"><div><strong>未记录模型名</strong><small>观察到 ${fullToken(tokens)} Token</small></div></div><p class="override-hint">这些事件没有可匹配的模型标识，因此保持未定价；为它们猜测模型会造成虚假的费用精度。</p></article>`);
      continue;
    }
    appendOverrideCard(model, overrides[model], observed.get(model));
  }
  if (!list.children.length) list.innerHTML = `<div class="empty-state">当前没有需要设置的内部或未知模型</div>`;
}

function renderCatalog(catalog) {
  $("#pricingCatalog").innerHTML = `<div class="catalog-row header"><span>模型</span><span>Input</span><span>Cached</span><span>Write</span><span>Output</span></div>${catalog.map((entry) => `<div class="catalog-row"><a href="${escapeHTML(entry.source)}" target="_blank" rel="noreferrer">${escapeHTML(entry.display_name)}</a><span>${escapeHTML(entry.input_usd_per_million)}</span><span>${escapeHTML(entry.cached_input_usd_per_million)}</span><span>${escapeHTML(entry.cache_write_input_usd_per_million || "—")}</span><span>${escapeHTML(entry.output_usd_per_million)}</span></div>`).join("")}`;
}

function appendOverrideCard(model, override = null, observedTokens = null) {
  const list = $("#pricingOverrideList");
  if ($('.empty-state', list)) list.innerHTML = "";
  if ($$('[data-pricing-model]', list).some((card) => card.dataset.pricingModel.toLowerCase() === model.toLowerCase())) {
    toast("这个模型已经在列表中", true);
    return;
  }
  const mode = override?.alias_of ? "alias" : override ? "custom" : "unpriced";
  const aliasOptions = (state.pricing?.catalog || []).map((entry) => `<option value="${escapeHTML(entry.model)}" ${override?.alias_of === entry.model ? "selected" : ""}>${escapeHTML(entry.display_name)}</option>`).join("");
  const card = document.createElement("article");
  card.className = "override-card";
  card.dataset.pricingModel = model;
  card.innerHTML = `<div class="override-card-head"><div><strong>${escapeHTML(model)}</strong><small>${observedTokens == null ? "本机自定义模型" : `观察到 ${fullToken(observedTokens)} Token`}</small></div><button class="remove-override pressable" type="button">从设置中移除</button></div>
    <div class="override-mode">
      <label>处理方式<select data-rate-mode><option value="unpriced" ${mode === "unpriced" ? "selected" : ""}>保持未定价</option><option value="alias" ${mode === "alias" ? "selected" : ""}>映射到内置模型</option><option value="custom" ${mode === "custom" ? "selected" : ""}>填写自定义单价</option></select></label>
      <label class="alias-field">内置公开模型<select data-alias><option value="">请选择模型</option>${aliasOptions}</select></label>
      <div class="custom-rate-grid">
        <label>Input<input data-rate="input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Cached<input data-rate="cached_input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.cached_input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Cache Write<input data-rate="cache_write_input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.cache_write_input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Output<input data-rate="output_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.output_usd_per_million || "")}" placeholder="USD / 1M"></label>
      </div>
      <p class="override-hint">单位均为 USD / 1M Token；最多三位小数。别名只能指向内置模型。</p>
    </div>`;
  list.appendChild(card);
  const updateMode = () => {
    const value = $('[data-rate-mode]', card).value;
    $('.alias-field', card).classList.toggle("hidden", value !== "alias");
    $('.custom-rate-grid', card).classList.toggle("hidden", value !== "custom");
    $('.override-hint', card).classList.toggle("hidden", value === "unpriced");
  };
  $('[data-rate-mode]', card).addEventListener("change", updateMode);
  $('.remove-override', card).addEventListener("click", () => card.remove());
  updateMode();
}

function collectPricingOverrides() {
  const overrides = {};
  const decimal = /^\d+(?:\.\d{1,3})?$/;
  for (const card of $$('[data-pricing-model]', $("#pricingOverrideList"))) {
    const model = card.dataset.pricingModel.trim();
    const mode = $('[data-rate-mode]', card).value;
    if (mode === "unpriced") continue;
    if (mode === "alias") {
      const alias = $('[data-alias]', card).value;
      if (!alias) throw new Error(`${model} 还没有选择内置模型`);
      overrides[model] = { alias_of: alias };
      continue;
    }
    const value = {};
    for (const input of $$('[data-rate]', card)) {
      const rate = input.value.trim();
      if (rate) value[input.dataset.rate] = rate;
    }
    for (const key of ["input_usd_per_million", "cached_input_usd_per_million", "cache_write_input_usd_per_million", "output_usd_per_million"]) {
      if (!value[key]) throw new Error(`${model} 缺少 ${key.replace("_usd_per_million", "")} 单价`);
    }
    for (const rate of Object.values(value)) if (!decimal.test(rate)) throw new Error(`${model} 的单价必须是非负十进制，最多三位小数`);
    overrides[model] = value;
  }
  return overrides;
}

async function savePricing() {
  let overrides;
  try { overrides = collectPricingOverrides(); } catch (error) { toast(error.message, true); return; }
  const button = $("#savePricing");
  button.disabled = true;
  try {
    state.pricing = await api("/api/v1/pricing/overrides", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ overrides })
    });
    invalidateDataCache();
    toast("本机定价已保存，费用已重新估算");
    closeDialog($("#pricingDialog"));
    await loadCurrentView();
  } catch (error) {
    toast(error.message, true);
  } finally {
    button.disabled = false;
  }
}

function setupDialogBehavior() {
  $$('dialog').forEach((dialog) => {
    dialog.addEventListener("cancel", (event) => { event.preventDefault(); closeDialog(dialog); });
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog && !dialog.classList.contains("sheet-dialog")) closeDialog(dialog);
    });
    $$('[data-close]', dialog).forEach((button) => button.addEventListener("click", () => closeDialog(dialog)));
  });
}

function setupPressFeedback() {
  let pressed = null;
  document.addEventListener("pointerdown", (event) => {
    const target = event.target.closest(".pressable");
    if (!target || target.disabled) return;
    pressed?.classList.remove("is-pressed");
    pressed = target;
    pressed.classList.add("is-pressed");
  }, { passive: true });
  const clear = () => { pressed?.classList.remove("is-pressed"); pressed = null; };
  window.addEventListener("pointerup", clear, { passive: true });
  window.addEventListener("pointercancel", clear, { passive: true });
}

function tablistKeydown(event, tabs, activate) {
  if (!tabs.includes(event.target)) return;
  const keys = ["ArrowLeft", "ArrowRight", "Home", "End"];
  if (!keys.includes(event.key)) return;
  event.preventDefault();
  const current = Math.max(0, tabs.indexOf(event.target));
  const next = event.key === "Home" ? 0
    : event.key === "End" ? tabs.length - 1
      : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
  tabs[next].focus();
  activate(tabs[next]);
}

function setupEvents() {
  setupPressFeedback();
  setupDialogBehavior();
  $$('.nav-tab').forEach((button) => button.addEventListener("click", () => switchView(button.dataset.view)));
  $(".primary-nav").addEventListener("keydown", (event) => tablistKeydown(event, $$('.nav-tab'), (button) => switchView(button.dataset.view)));
  $("#brandButton").addEventListener("click", () => switchView("overview"));
  $$('[data-overview-range]').forEach((button) => button.addEventListener("click", () => {
    state.overviewRange = button.dataset.overviewRange;
    $$('[data-overview-range]').forEach((item) => {
      const selected = item === button;
      item.classList.toggle("selected", selected);
      item.setAttribute("aria-pressed", String(selected));
    });
    state.pulseDate = "";
    loadOverview();
  }));
  $$('[data-detail-range]').forEach((button) => button.addEventListener("click", () => {
    state.detailRange = button.dataset.detailRange;
    $$('[data-detail-range]').forEach((item) => {
      const selected = item === button;
      item.classList.toggle("selected", selected);
      item.setAttribute("aria-pressed", String(selected));
    });
    loadDetails();
  }));
  $$('[data-dimension]').forEach((button) => button.addEventListener("click", () => {
    state.detailDimension = button.dataset.dimension;
    $$('[data-dimension]').forEach((item) => {
      const selected = item === button;
      item.classList.toggle("selected", selected);
      item.setAttribute("aria-selected", String(selected));
      item.tabIndex = selected ? 0 : -1;
    });
    loadDetails();
  }));
  $(".dimension-tabs").addEventListener("keydown", (event) => tablistKeydown(event, $$('[data-dimension]'), (button) => button.click()));
  $("#previousMonth").addEventListener("click", () => {
    state.monthCursor = new Date(state.monthCursor.getFullYear(), state.monthCursor.getMonth() - 1, 1);
    state.selectedDate = "";
    loadDaily();
  });
  $("#nextMonth").addEventListener("click", () => {
    state.monthCursor = new Date(state.monthCursor.getFullYear(), state.monthCursor.getMonth() + 1, 1);
    state.selectedDate = "";
    loadDaily();
  });
  $("#filterButton").addEventListener("click", () => { syncFilterForm(); openDialog($("#filterSheet")); });
  $("#applyFilters").addEventListener("click", applyFiltersFromForm);
  $("#filterForm").addEventListener("submit", (event) => { event.preventDefault(); applyFiltersFromForm(); });
  $("#resetFilters").addEventListener("click", () => {
    for (const meta of Object.values(FILTER_FIELDS)) $(meta.selector).value = "";
  });
  $("#clearFilters").addEventListener("click", clearAllFilters);
  $("#warningButton").addEventListener("click", () => { openDialog($("#warningsDialog")); loadWarnings(); });
  $("#pricingButton").addEventListener("click", openPricingDialog);
  $("#addOverride").addEventListener("click", () => {
    const input = $("#newOverrideModel");
    const model = input.value.trim().toLowerCase();
    if (!model) { toast("请输入要覆写的模型名", true); return; }
    appendOverrideCard(model);
    input.value = "";
  });
  $("#newOverrideModel").addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); $("#addOverride").click(); }
  });
  $("#savePricing").addEventListener("click", savePricing);
  $("#exportButton").addEventListener("click", () => { setExportLinks(); openDialog($("#exportDialog")); });
  $("#scanButton").addEventListener("click", async () => {
    const button = $("#scanButton");
    button.disabled = true;
    $(".scan-icon").classList.add("spin");
    try {
      const result = await api("/api/v1/rescan", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
      invalidateDataCache();
      toast(`扫描完成：新增 ${result.events_inserted || 0} 个事件，忽略 ${result.duplicates || 0} 个重复`);
      await loadStatus();
      await Promise.all([loadFilterOptions(), loadCurrentView()]);
    } catch (error) {
      toast(error.message, true);
    } finally {
      button.disabled = false;
      $(".scan-icon").classList.remove("spin");
    }
  });
  $("#themeButton").addEventListener("click", () => {
    const explicit = document.documentElement.dataset.theme;
    const dark = explicit ? explicit === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
    const next = dark ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("codex-usage-theme", next);
  });
}

async function boot() {
  const savedTheme = localStorage.getItem("codex-usage-theme");
  if (savedTheme === "light" || savedTheme === "dark") document.documentElement.dataset.theme = savedTheme;
  setupEvents();
  renderFilterChips();
  const requestedView = location.hash.slice(1);
  if (["daily", "details"].includes(requestedView)) {
    const initial = state.view;
    state.view = requestedView;
    $(`[data-view-panel="${initial}"]`).hidden = true;
    $(`[data-view-panel="${requestedView}"]`).hidden = false;
    $$('.nav-tab').forEach((tab) => {
      const selected = tab.dataset.view === requestedView;
      tab.classList.toggle("active", selected);
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
    });
  }
  await Promise.all([
    loadStatus().catch((error) => toast(error.message, true)),
    loadFilterOptions(),
    loadCurrentView()
  ]);
  setInterval(async () => {
    try {
      const changed = await loadStatus();
      if (changed && document.visibilityState === "visible") await loadCurrentView({ preserve: true });
    } catch {}
  }, 30_000);
}

boot();
