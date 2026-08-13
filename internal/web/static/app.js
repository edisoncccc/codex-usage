const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const i18n = window.CodexUsageI18n;
const t = (key, values) => i18n.t(key, values);

const FILTER_FIELDS = {
  date: { selector: "#filterDate", labelKey: "filter.date" },
  model: { selector: "#filterModel", labelKey: "filter.model" },
  source: { selector: "#filterSource", labelKey: "filter.source" },
  agent_type: { selector: "#filterAgent", labelKey: "filter.agent" },
  project: { selector: "#filterProject", labelKey: "filter.project" },
  confidence: { selector: "#filterConfidence", labelKey: "filter.confidence" }
};
const FILTER_LABEL_KEYS = { session_id: "filter.session" };
const fieldLabel = (key) => t(FILTER_FIELDS[key]?.labelKey || FILTER_LABEL_KEYS[key] || `dimension.${key}`);
const dimensionLabel = (key) => t(`dimension.${key}`);
const rangeLabel = (key) => t(`range.${key}Long`);
const DISPLAY_PREFERENCES_KEY = "codex-usage-display-preferences";
const DISPLAY_PREFERENCE_DEFAULTS = Object.freeze({
  fontSize: "comfortable",
  density: "balanced",
  theme: "system",
  motion: "system"
});
const DISPLAY_PREFERENCE_VALUES = Object.freeze({
  fontSize: ["compact", "comfortable", "large"],
  density: ["compact", "balanced", "relaxed"],
  theme: ["system", "light", "dark"],
  motion: ["system", "reduce"]
});

function readStorage(key) {
  try { return localStorage.getItem(key); } catch { return null; }
}

function writeStorage(key, value) {
  try { localStorage.setItem(key, value); } catch {}
}

function removeStorage(key) {
  try { localStorage.removeItem(key); } catch {}
}

function normalizeDisplayPreferences(value = {}) {
  const normalized = { ...DISPLAY_PREFERENCE_DEFAULTS };
  for (const [key, allowed] of Object.entries(DISPLAY_PREFERENCE_VALUES)) {
    if (allowed.includes(value?.[key])) normalized[key] = value[key];
  }
  return normalized;
}

function loadDisplayPreferences() {
  let saved = {};
  try { saved = JSON.parse(readStorage(DISPLAY_PREFERENCES_KEY) || "{}"); } catch {}
  if (!saved || typeof saved !== "object" || Array.isArray(saved)) saved = {};
  const legacyTheme = readStorage("codex-usage-theme");
  if (!DISPLAY_PREFERENCE_VALUES.theme.includes(saved?.theme) && ["light", "dark"].includes(legacyTheme)) saved.theme = legacyTheme;
  return normalizeDisplayPreferences(saved);
}

let displayPreferences = loadDisplayPreferences();

function syncSettingsForm() {
  $$('[data-setting]').forEach((input) => {
    const key = input.dataset.setting;
    const value = key === "locale" ? i18n.getLocale() : displayPreferences[key];
    input.checked = input.value === value;
  });
}

function applyDisplayPreferences({ persist = false } = {}) {
  const root = document.documentElement;
  root.dataset.fontSize = displayPreferences.fontSize;
  root.dataset.density = displayPreferences.density;
  if (displayPreferences.theme === "system") root.removeAttribute("data-theme");
  else root.dataset.theme = displayPreferences.theme;
  if (displayPreferences.motion === "system") root.removeAttribute("data-motion");
  else root.dataset.motion = displayPreferences.motion;

  if (persist) {
    writeStorage(DISPLAY_PREFERENCES_KEY, JSON.stringify(displayPreferences));
    if (displayPreferences.theme === "system") removeStorage("codex-usage-theme");
    else writeStorage("codex-usage-theme", displayPreferences.theme);
  }
  syncSettingsForm();
}

function updateDisplayPreference(key, value) {
  if (!DISPLAY_PREFERENCE_VALUES[key]?.includes(value)) return;
  displayPreferences = normalizeDisplayPreferences({ ...displayPreferences, [key]: value });
  applyDisplayPreferences({ persist: true });
}

function resetDisplayPreferences() {
  displayPreferences = { ...DISPLAY_PREFERENCE_DEFAULTS };
  removeStorage(DISPLAY_PREFERENCES_KEY);
  removeStorage("codex-usage-theme");
  applyDisplayPreferences();
  toast(t("settings.resetComplete"));
}

const state = {
  view: "overview",
  viewVisited: { overview: true, daily: false, details: false },
  overviewRange: "7d",
  detailRange: "30d",
  detailDimension: "model",
  filters: {},
  status: null,
  overview: null,
  hourlyLoaded: false,
  hourlyDate: "",
  hourlyNavigationDirection: 0,
  hourlyPoints: [],
  hourlyPointDate: "",
  pulsePoints: [],
  pulseDate: "",
  monthCursor: new Date(new Date().getFullYear(), new Date().getMonth(), 1),
  selectedDate: "",
  monthReport: null,
  dailyInitialSelection: true,
  pricing: null,
  filterOptions: null,
  dataRevision: null,
  pendingDataRefresh: false,
  statusQualityNotes: [],
  dataQualityNotes: [],
  sessionSearch: "",
  requestSerial: { overview: 0, hourly: 0, hourlyContext: 0, daily: 0, day: 0, breakdown: 0, sessions: 0, estimates: 0 }
};

const responseCache = new Map();
const inflightRequests = new Map();
const DATA_CACHE_TTL = 5 * 60_000;
const VISIBLE_STATUS_POLL_MS = 30_000;
const HIDDEN_STATUS_POLL_MS = 10 * 60_000;
let cacheGeneration = 0;
let statusPollTimer = null;
let statusPollRunning = false;
let sessionSearchTimer = null;
let hourlyContextTimer = null;

const reducedMotion = () => displayPreferences.motion === "reduce" || window.matchMedia("(prefers-reduced-motion: reduce)").matches;
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
const hourKey = (date) => `${dateKey(date)}T${pad2(date.getHours())}`;

function latestCompleteHour(now = new Date()) {
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);
  return new Date(currentHour.getTime() - 60 * 60_000);
}

function normalizeHourlyDate(value, now = new Date()) {
  const fallback = dateKey(latestCompleteHour(now));
  if (!/^\d{4}-\d{2}-\d{2}$/.test(String(value || ""))) return fallback;
  const parsed = dateFromKey(value);
  if (Number.isNaN(parsed.getTime()) || dateKey(parsed) !== value || value > dateKey(now)) return fallback;
  return value;
}

function hourlyWindow(selectedDate = state.hourlyDate, now = new Date()) {
  const date = normalizeHourlyDate(selectedDate, now);
  const chartStart = dateFromKey(date);
  const dayEnd = addDays(chartStart, 1);
  const currentHour = new Date(now);
  currentHour.setMinutes(0, 0, 0);
  const chartEnd = date === dateKey(now) && currentHour < dayEnd ? currentHour : dayEnd;
  return {
    date,
    chartStart,
    chartEnd,
    completeHours: Math.max(0, Math.round((chartEnd.getTime() - chartStart.getTime()) / 60 / 60_000))
  };
}

function formatClock(value) {
  return value.toLocaleTimeString(i18n.getLocale(), { hour: "2-digit", minute: "2-digit", hour12: false });
}

function formatHourWindow(start, end) {
  const dateTime = (value) => `${i18n.formatDate(value, { month: "short", day: "numeric" })} ${formatClock(value)}`;
  return `${dateTime(start)}–${dateKey(start) === dateKey(end) ? formatClock(end) : dateTime(end)}`;
}

function fillCompleteHours(rawPoints, windows) {
  const byHour = new Map((rawPoints || []).map((point) => [point.date, point]));
  return Array.from({ length: windows.completeHours }, (_, index) => {
    const start = new Date(windows.chartStart.getTime() + index * 60 * 60_000);
    const existing = byHour.get(hourKey(start));
    return { date: hourKey(start), start, usage: existing?.usage || emptyUsage() };
  });
}

function rangeBounds(range) {
  if (range === "all") return { label: rangeLabel("all") };
  const today = dateFromKey(todayKey());
  const days = range === "today" ? 1 : range === "7d" ? 7 : 30;
  return {
    since: dateKey(addDays(today, -(days - 1))),
    until: dateKey(addDays(today, 1)),
    label: rangeLabel(range)
  };
}

function syncRangeControls() {
  const selectedDate = state.filters.date || "";
  $$('[data-overview-range]').forEach((button) => {
    const selected = !selectedDate && button.dataset.overviewRange === state.overviewRange;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
  });
  $$('[data-detail-range]').forEach((button) => {
    const selected = !selectedDate && button.dataset.detailRange === state.detailRange;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-pressed", String(selected));
  });
  const picker = $("#detailDatePicker");
  if (!picker) return;
  picker.value = selectedDate;
  picker.max = todayKey();
  $(".details-date-picker").classList.toggle("selected", Boolean(selectedDate));
  $("#detailDateLabel").textContent = selectedDate
    ? i18n.formatDate(dateFromKey(selectedDate), { year: "numeric", month: "short", day: "numeric" })
    : t("details.pickDate");
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
  return i18n.formatNumber(number);
};
const fullToken = (value = 0) => i18n.formatNumber(Number(value || 0));
const formatUSD = (value = "0") => {
  const amount = Number(value || 0);
  if (!Number.isFinite(amount)) return "—";
  const digits = amount >= 100 ? 2 : amount >= 1 ? 2 : amount >= .01 ? 3 : 6;
  return `$${amount.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: digits })}`;
};
const formatPercent = (value = 0) => `${(Number(value || 0) * 100).toFixed(1)}%`;
const localTime = (value) => value ? i18n.formatDateTime(value) : "—";
const shortId = (value = "") => value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value || "—";
const shortPath = (value = "") => {
  const parts = String(value).split(/[\\/]/).filter(Boolean);
  return parts.at(-1) || value || t("common.notRecorded");
};
const escapeHTML = (value = "") => String(value).replace(/[&<>"']/g, (char) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[char]));
const confidenceLabel = (value) => {
  if (!value) return "—";
  const key = `confidence.${value}`;
  const translated = t(key);
  return translated === key ? value : translated;
};

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
      const error = new Error(message || `${response.status} ${response.statusText}`);
      error.status = response.status;
      error.payload = payload;
      throw error;
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
  if (filters.date) {
    if (/^\d{4}-\d{2}-\d{2}$/.test(String(values.since || ""))) delete values.since;
    if (/^\d{4}-\d{2}-\d{2}$/.test(String(values.until || ""))) delete values.until;
  }
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
  if (!total) return t("dynamic.estimateNone");
  const base = t("dynamic.priced", { coverage: formatPercent(estimate.coverage_ratio) });
  return estimate.unpriced_tokens ? `${base} · ${t("dynamic.unpricedSuffix", { tokens: formatToken(estimate.unpriced_tokens) })}` : base;
}

function estimateDisplay(estimate = {}) {
  return estimateTokens(estimate) ? formatUSD(estimate.usd) : "—";
}

function reasonSummary(estimate = {}) {
  const reasons = estimate.reasons || [];
  if (!reasons.length) return "";
  return [...new Set(reasons.map((reason) => {
    const key = `reason.${reason.kind}`;
    const translated = t(key);
    return translated === key ? reason.kind : translated;
  }))].join(i18n.getLocale() === "en" ? ", " : "、");
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
  if (changed) {
    invalidateDataCache();
    state.filterOptions = null;
  }
  state.dataRevision = revision;
  const machine = status.machine || {};
  const machineName = machine.label || machine.hostname || t("common.localMachine");
  $("#machineLabel").textContent = machineName;
  $("#machinePill").title = t("dynamic.machineAria", { machine: machineName });
  $("#machinePill").setAttribute("aria-label", t("dynamic.machineAria", { machine: machineName }));
  $("#machineId").textContent = shortId(machine.id || "");
  $("#machineId").title = machine.id || "";
  $("#lastScan").textContent = localTime(status.last_scan);
  $("#sourceStatus").textContent = t("dynamic.jsonlOnly");
  $("#sourceDot").className = "status-dot live";
  $("#versionLabel").textContent = `v${payload.version || "—"}`;
  $("#scanButton").disabled = Boolean(payload.scanning);
  $(".scan-icon").classList.toggle("spin", Boolean(payload.scanning));

  const notes = [];
  if (status.warning_count) notes.push(t("dynamic.warningCount", { count: i18n.formatNumber(status.warning_count) }));
  for (const home of status.codex_homes || []) {
    if (home.warning) notes.push(i18n.getLocale() === "en" ? `${t("warning.raw")}: ${home.warning}` : home.warning);
  }
  state.statusQualityNotes = notes;
  renderQualityBanner();
  return changed;
}

function hydrateSelect(selector, items, placeholder) {
  const select = $(selector);
  const selected = select.value || state.filters[Object.entries(FILTER_FIELDS).find(([, item]) => item.selector === selector)?.[0]] || "";
  const values = [...new Set((items || []).map((item) => typeof item === "string" ? item : item.key).filter((key) => key && key !== "未知" && key !== "Unknown"))];
  select.innerHTML = `<option value="">${escapeHTML(placeholder)}</option>${values.map((value) => `<option value="${escapeHTML(value)}">${escapeHTML(value)}</option>`).join("")}`;
  if (values.includes(selected)) select.value = selected;
}

function renderFilterOptions() {
  if (!state.filterOptions) return;
  hydrateSelect("#filterModel", state.filterOptions.models, t("filter.allModels"));
  hydrateSelect("#filterSource", state.filterOptions.sources, t("filter.allSources"));
  hydrateSelect("#filterProject", state.filterOptions.projects, t("filter.allProjects"));
}

async function loadFilterOptions({ force = false } = {}) {
  if (state.filterOptions && !force) {
    renderFilterOptions();
    return;
  }
  try {
    state.filterOptions = await api("/api/v1/dimensions");
    renderFilterOptions();
  } catch (error) {
    toast(t("dynamic.filterLoadError", { error: error.message }), true);
  }
}

function renderFilterChips() {
  const entries = Object.entries(state.filters).filter(([, value]) => value);
  $("#filterContext").classList.toggle("hidden", entries.length === 0);
  $("#filterCount").classList.toggle("hidden", entries.length === 0);
  $("#filterCount").textContent = entries.length;
  $("#filterChips").innerHTML = entries.map(([key, value]) => {
    const display = key === "date" ? i18n.formatDate(dateFromKey(value), { year: "numeric", month: "short", day: "numeric" })
      : key === "project" ? shortPath(value)
      : key === "confidence" ? confidenceLabel(value)
        : key === "session_id" ? shortId(value) : value;
    const label = fieldLabel(key);
    return `<span class="filter-chip" title="${escapeHTML(value)}"><span>${escapeHTML(label)} · ${escapeHTML(display)}</span><button class="pressable" type="button" data-remove-filter="${escapeHTML(key)}" aria-label="${escapeHTML(t("dynamic.removeFilter", { label }))}">×</button></span>`;
  }).join("");
  $$('[data-remove-filter]').forEach((button) => button.addEventListener("click", () => {
    delete state.filters[button.dataset.removeFilter];
    resetDataSelections();
    renderFilterChips();
    syncFilterForm();
    loadCurrentView();
  }));
  syncRangeControls();
}

function syncFilterForm() {
  for (const [key, meta] of Object.entries(FILTER_FIELDS)) $(meta.selector).value = state.filters[key] || "";
  $("#filterDate").max = todayKey();
}

function resetDataSelections() {
  state.hourlyPointDate = "";
  state.pulseDate = "";
  const filteredDate = state.filters.date || "";
  const parsed = dateFromKey(filteredDate);
  if (filteredDate && !Number.isNaN(parsed.getTime()) && dateKey(parsed) === filteredDate) {
    state.hourlyDate = filteredDate;
    state.monthCursor = new Date(parsed.getFullYear(), parsed.getMonth(), 1);
    state.selectedDate = filteredDate;
    state.dailyInitialSelection = false;
    return;
  }
  state.selectedDate = "";
  state.dailyInitialSelection = true;
}

function applyFiltersFromForm() {
  if (!$("#filterForm").reportValidity()) return;
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
  $("#overviewSubtitle").textContent = state.filters.date
    ? i18n.formatDate(dateFromKey(state.filters.date), { year: "numeric", month: "long", day: "numeric" })
    : bounds.label;
  if (!preserve) {
    setLoading($("#overviewTotal"));
    setLoading($("#overviewCost"));
  }
  loadHourlyUsage({ preserve: preserve || state.hourlyLoaded });
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
  if (firstError) toast(t("dynamic.overviewError", { error: firstError.message }), true);
}

async function loadHourlyUsage({ preserve = false } = {}) {
  const serial = ++state.requestSerial.hourly;
  clearTimeout(hourlyContextTimer);
  state.requestSerial.hourlyContext += 1;
  const windows = hourlyWindow();
  state.hourlyDate = windows.date;
  syncHourlyNavigator(windows);
  const chart = $("#hourlyChart");
  const summary = $(".hourly-summary");
  const context = $("#hourlyContext");
  if (!preserve) {
    setLoading($("#hourlyTotal"));
    $("#hourlyLedger").innerHTML = `<span class="hourly-placeholder">${escapeHTML(t("common.loading"))}</span>`;
    setLoading($("#hourlyCost"));
    $("#hourlyCostNote").textContent = t("hourly.costNote");
    $("#hourlyModels").innerHTML = `<span class="hourly-placeholder">${escapeHTML(t("common.loading"))}</span>`;
    $("#hourlyCostCoverage").textContent = "—";
    $("#hourlyCostCoverageNote").textContent = "—";
    $("#hourlyLine").innerHTML = "";
    $("#hourlyAxis").innerHTML = "";
    $("#hourlyPoints").innerHTML = `<div class="empty-state">${escapeHTML(t("common.loading"))}</div>`;
  } else {
    chart.classList.add("is-updating");
    summary.classList.add("is-updating");
    context.classList.add("is-updating");
  }
  summary.setAttribute("aria-busy", "true");
  $("#trendHourlyPane").setAttribute("aria-busy", "true");
  try {
    const series = await api(apiURL("/api/v1/timeseries", {
      since: windows.chartStart.toISOString(),
      until: windows.chartEnd.toISOString(),
      bucket: "hour"
    }));
    if (serial !== state.requestSerial.hourly) return;
    state.hourlyPoints = fillCompleteHours(series.points || [], windows);
    state.hourlyLoaded = true;
    renderHourlyUsage(state.hourlyPoints, windows);
    animateHourlyChart(state.hourlyNavigationDirection);
    state.hourlyNavigationDirection = 0;
    prefetchHourlyNeighbors(windows);
  } catch (error) {
    if (serial === state.requestSerial.hourly) {
      state.hourlyPoints = [];
      renderHourlyUsage([], windows);
      state.hourlyNavigationDirection = 0;
      toast(t("dynamic.hourlyError", { error: error.message }), true);
    }
  } finally {
    if (serial === state.requestSerial.hourly) {
      chart.classList.remove("is-updating");
      summary.classList.remove("is-updating");
      summary.removeAttribute("aria-busy");
      $("#trendHourlyPane").removeAttribute("aria-busy");
    }
  }
}

function syncHourlyNavigator(windows) {
  const picker = $("#hourlyDatePicker");
  picker.value = windows.date;
  picker.max = todayKey();
  syncDatePickerDisplay("hourlyDate", windows.date);
  $("#nextHourDay").disabled = windows.date >= todayKey();
  $("#currentHourDay").disabled = windows.date === todayKey();
  const displayDate = i18n.formatDate(windows.chartStart, { year: "numeric", month: "long", day: "numeric" });
  $("#hourlyCaption").textContent = t("hourly.completeHours", { date: displayDate, count: windows.completeHours });
  $("#hourlyPoints").setAttribute("aria-label", t("hourly.rulerAria", { date: displayDate }));
}

function prefetchHourlyNeighbors(windows) {
  const dates = [dateKey(addDays(windows.date, -1)), dateKey(addDays(windows.date, 1))]
    .filter((value) => value <= todayKey());
  for (const date of dates) {
    const neighbor = hourlyWindow(date);
    api(apiURL("/api/v1/timeseries", {
      since: neighbor.chartStart.toISOString(),
      until: neighbor.chartEnd.toISOString(),
      bucket: "hour"
    })).catch(() => {});
  }
}

function animateHourlyChart(direction) {
  const chart = $("#hourlyChart");
  chart.classList.remove("enter-previous", "enter-next", "is-updating");
  if (!direction || reducedMotion()) return;
  chart.classList.add(direction < 0 ? "enter-previous" : "enter-next");
  requestAnimationFrame(() => requestAnimationFrame(() => chart.classList.remove("enter-previous", "enter-next")));
}

function navigateHourlyDate(value, direction = 0) {
  const next = normalizeHourlyDate(value);
  if (next === state.hourlyDate && state.hourlyLoaded) {
    syncHourlyNavigator(hourlyWindow(next));
    return;
  }
  state.hourlyNavigationDirection = direction || (next < state.hourlyDate ? -1 : 1);
  state.hourlyDate = next;
  state.hourlyPointDate = "";
  syncHourlyNavigator(hourlyWindow(next));
  loadHourlyUsage({ preserve: state.hourlyLoaded });
}

function renderHourlySelection(point) {
  const totalNode = $("#hourlyTotal");
  if (!point) {
    $("#hourlyWindowLabel").textContent = "—";
    totalNode.textContent = "—";
    totalNode.removeAttribute("title");
    totalNode.classList.remove("loading");
    $("#hourlyLedger").innerHTML = `<span class="hourly-placeholder">${escapeHTML(t("dynamic.hourlyEmpty"))}</span>`;
    return;
  }
  const usage = point.usage || emptyUsage();
  const total = usageTotal(usage);
  $("#hourlyWindowLabel").textContent = formatHourWindow(point.start, new Date(point.start.getTime() + 60 * 60_000));
  totalNode.textContent = formatToken(total);
  totalNode.title = `${fullToken(total)} Total Token`;
  totalNode.classList.remove("loading");
  $("#hourlyLedger").innerHTML = [
    ["hourlyInput", "Input", usage.input], ["hourlyCached", "Cached", usage.cached_input],
    ["hourlyCacheWrite", "Cache Write", usage.cache_write_input], ["hourlyOutput", "Output", usage.output],
    ["hourlyReasoning", "Reasoning", usage.reasoning_output]
  ].map(([id, name, value]) => `<span><small>${name}</small><strong id="${id}" title="${fullToken(value)}">${formatToken(value)}</strong></span>`).join("");
}

function finishHourlyContextUpdate() {
  const context = $("#hourlyContext");
  context.classList.remove("is-updating");
  context.removeAttribute("aria-busy");
}

function renderHourlyContext(report, point) {
  const estimate = report?.summary || {};
  const pointUsage = point?.usage || emptyUsage();
  const hasUsage = usageTotal(pointUsage) > 0 || estimateTokens(estimate) > 0;
  const pricedTokens = Number(estimate.priced_tokens || 0);
  const costNode = $("#hourlyCost");
  costNode.textContent = !hasUsage ? formatUSD(0)
    : estimateTokens(estimate) && !pricedTokens ? t("dynamic.unpriced")
      : estimateDisplay(estimate);
  costNode.title = hasUsage ? `${formatUSD(estimate.usd)} · ${estimateLabel(estimate)}` : t("hourly.noUsage");
  costNode.classList.remove("loading");
  const reasons = reasonSummary(estimate);
  $("#hourlyCostNote").textContent = t("hourly.costNote");
  $("#hourlyCostNote").title = reasons || t("hourly.costNote");

  const models = (report?.models || []).filter((item) => usageTotal(item.usage) > 0);
  const visibleModels = models.slice(0, 3);
  $("#hourlyModels").innerHTML = visibleModels.length ? visibleModels.map((item) => {
    const tokens = usageTotal(item.usage);
    const modelEstimate = item.estimate || {};
    const modelCost = estimateTokens(modelEstimate) && !Number(modelEstimate.priced_tokens || 0)
      ? t("dynamic.unpriced") : estimateDisplay(modelEstimate);
    return `<span class="hourly-model-chip" title="${escapeHTML(`${item.key} · ${fullToken(tokens)} Token · ${modelCost}`)}"><strong>${escapeHTML(item.key || t("common.unknownModel"))}</strong><small>${escapeHTML(`${formatToken(tokens)} · ${modelCost}`)}</small></span>`;
  }).join("") + (models.length > visibleModels.length ? `<span class="hourly-model-more">${escapeHTML(t("hourly.moreModels", { count: i18n.formatNumber(models.length - visibleModels.length) }))}</span>` : "")
    : `<span class="hourly-placeholder">${escapeHTML(t("hourly.noModels"))}</span>`;

  $("#hourlyCostCoverage").textContent = hasUsage ? formatPercent(estimate.coverage_ratio) : "—";
  $("#hourlyCostCoverageNote").textContent = !hasUsage ? t("hourly.noUsage")
    : Number(estimate.unpriced_tokens || 0) > 0
      ? t("dynamic.unpricedSuffix", { tokens: formatToken(estimate.unpriced_tokens) })
      : t("hourly.catalog", { date: report?.catalog_as_of || "—" });
  finishHourlyContextUpdate();
}

function renderHourlyContextError(error) {
  $("#hourlyCost").textContent = "—";
  $("#hourlyCost").removeAttribute("title");
  $("#hourlyCost").classList.remove("loading");
  $("#hourlyCostNote").textContent = t("dynamic.hourlyContextError", { error: error.message });
  $("#hourlyCostNote").removeAttribute("title");
  $("#hourlyModels").innerHTML = `<span class="hourly-placeholder">—</span>`;
  $("#hourlyCostCoverage").textContent = "—";
  $("#hourlyCostCoverageNote").textContent = "—";
  finishHourlyContextUpdate();
}

function scheduleHourlyContext(point, { immediate = false } = {}) {
  clearTimeout(hourlyContextTimer);
  const serial = ++state.requestSerial.hourlyContext;
  const context = $("#hourlyContext");
  context.classList.add("is-updating");
  context.setAttribute("aria-busy", "true");
  if (!point || usageTotal(point.usage) === 0) {
    renderHourlyContext({ summary: {}, models: [] }, point);
    return;
  }
  const load = async () => {
    const end = new Date(point.start.getTime() + 60 * 60_000);
    try {
      const report = await api(apiURL("/api/v1/cost-estimate", {
        since: point.start.toISOString(),
        until: end.toISOString()
      }));
      if (serial !== state.requestSerial.hourlyContext || state.hourlyPointDate !== point.date) return;
      renderHourlyContext(report, point);
    } catch (error) {
      if (serial !== state.requestSerial.hourlyContext || state.hourlyPointDate !== point.date) return;
      renderHourlyContextError(error);
      toast(t("dynamic.hourlyContextError", { error: error.message }), true);
    }
  };
  if (immediate) void load();
  else hourlyContextTimer = setTimeout(() => { void load(); }, 110);
}

function renderHourlyUsage(points, windows) {
  syncHourlyNavigator(windows);
  renderHourlyLine(points);
  $("#trendHourlyPane").title = t("hourly.updated", { time: localTime(new Date().toISOString()) });
}

function renderHourlyLine(points) {
  const line = $("#hourlyLine");
  const pointLayer = $("#hourlyPoints");
  const axis = $("#hourlyAxis");
  cancelAnimationFrame(pointLayer.hourlyScrubFrame || 0);
  const selectedWindow = hourlyWindow(state.hourlyDate);
  const displayDate = i18n.formatDate(selectedWindow.chartStart, { year: "numeric", month: "long", day: "numeric" });
  pointLayer.setAttribute("aria-label", t("hourly.rulerAria", { date: displayDate }));
  if (!points.length) {
    line.innerHTML = "";
    axis.innerHTML = "";
    pointLayer.onpointermove = null;
    pointLayer.innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.hourlyEmpty"))}</div>`;
    renderHourlySelection(null);
    scheduleHourlyContext(null, { immediate: true });
    return;
  }
  const plot = { left: 28, right: 972, top: 16, bottom: 142 };
  const max = Math.max(...points.map((point) => usageTotal(point.usage)), 1);
  const coordinates = points.map((point, index) => {
    const x = plot.left + (plot.right - plot.left) * (points.length === 1 ? .5 : index / (points.length - 1));
    const total = usageTotal(point.usage);
    const y = total ? plot.bottom - (plot.bottom - plot.top) * total / max : plot.bottom;
    return { point, index, total, x, y };
  });
  const polyline = coordinates.map(({ x, y }) => `${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
  const area = `M ${coordinates[0].x.toFixed(2)} ${plot.bottom} L ${polyline.replaceAll(",", " ")} L ${coordinates.at(-1).x.toFixed(2)} ${plot.bottom} Z`;
  line.innerHTML = `<path class="hour-line-area" d="${area}"></path><polyline class="hour-line-path" points="${polyline}"></polyline><line class="hour-selection-guide" id="hourSelectionGuide" x1="0" x2="0" y1="${plot.top}" y2="${plot.bottom}"></line>`;

  const availableDates = new Set(points.map((point) => point.date));
  if (!state.hourlyPointDate || !availableDates.has(state.hourlyPointDate)) state.hourlyPointDate = points.at(-1).date;
  pointLayer.innerHTML = coordinates.map(({ point, index, total, x, y }) => {
    const windowLabel = formatHourWindow(point.start, new Date(point.start.getTime() + 60 * 60_000));
    const selected = point.date === state.hourlyPointDate;
    return `<button class="hour-point pressable ${selected ? "selected" : ""} ${total ? "" : "zero"}" type="button" data-hour-point="${index}" style="left:${(x / 10).toFixed(3)}%;top:${y.toFixed(2)}px" aria-pressed="${selected}" tabindex="${selected ? "0" : "-1"}" aria-label="${escapeHTML(t("hourly.barAria", { time: windowLabel, tokens: fullToken(total) }))}" title="${escapeHTML(t("hourly.barAria", { time: windowLabel, tokens: fullToken(total) }))}"></button>`;
  }).join("");
  axis.innerHTML = coordinates.filter(({ index }) => index % 3 === 0 || index === points.length - 1).map(({ point, index, x }) => {
    const clock = formatClock(point.start);
    const edge = index === 0 ? "edge-start" : index === points.length - 1 ? "edge-end" : "";
    return `<time class="${edge}" datetime="${escapeHTML(point.date)}:00" style="--label-x:${(x / 10).toFixed(3)}%">${escapeHTML(clock)}</time>`;
  }).join("");

  const selectPoint = (index, { immediateContext = false } = {}) => {
    const selectedPoint = points[index];
    if (!selectedPoint) return;
    state.hourlyPointDate = selectedPoint.date;
    $$('[data-hour-point]', pointLayer).forEach((button) => {
      const selected = Number(button.dataset.hourPoint) === index;
      button.classList.toggle("selected", selected);
      button.setAttribute("aria-pressed", String(selected));
      button.tabIndex = selected ? 0 : -1;
    });
    const guide = $("#hourSelectionGuide");
    guide?.setAttribute("x1", coordinates[index].x.toFixed(2));
    guide?.setAttribute("x2", coordinates[index].x.toFixed(2));
    renderHourlySelection(selectedPoint);
    scheduleHourlyContext(selectedPoint, { immediate: immediateContext });
  };
  $$('[data-hour-point]', pointLayer).forEach((button) => {
    const index = Number(button.dataset.hourPoint);
    button.addEventListener("mouseenter", () => selectPoint(index));
    button.addEventListener("focus", () => selectPoint(index, { immediateContext: true }));
    button.addEventListener("click", () => selectPoint(index, { immediateContext: true }));
    button.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? points.length - 1 : Math.max(0, Math.min(points.length - 1, index + (event.key === "ArrowRight" ? 1 : -1)));
      $(`[data-hour-point="${next}"]`, pointLayer)?.focus();
    });
  });
  let scrubIndex = -1;
  pointLayer.onpointermove = (event) => {
    if (event.pointerType && event.pointerType !== "mouse" && event.pointerType !== "pen") return;
    const rect = pointLayer.getBoundingClientRect();
    if (!rect.width) return;
    const localX = (event.clientX - rect.left) / rect.width * 1000;
    const next = coordinates.reduce((best, coordinate, index) => Math.abs(coordinate.x - localX) < Math.abs(coordinates[best].x - localX) ? index : best, 0);
    if (next === scrubIndex) return;
    scrubIndex = next;
    cancelAnimationFrame(pointLayer.hourlyScrubFrame || 0);
    pointLayer.hourlyScrubFrame = requestAnimationFrame(() => selectPoint(next));
  };
  selectPoint(Math.max(0, points.findIndex((point) => point.date === state.hourlyPointDate)), { immediateContext: true });
  requestAnimationFrame(() => {
    const selected = $('[data-hour-point].selected', pointLayer);
    const scroller = $(".hourly-chart-scroll");
    if (selected && selected.offsetLeft + selected.offsetWidth > scroller.clientWidth + scroller.scrollLeft) {
      scroller.scrollTo({ left: selected.offsetLeft - scroller.clientWidth + selected.offsetWidth + 12, behavior: reducedMotion() ? "auto" : "smooth" });
    }
  });
}

function switchTrendView(next) {
  if (!["hourly", "daily"].includes(next)) return;
  $$('[data-trend-view]').forEach((button) => {
    const selected = button.dataset.trendView === next;
    button.classList.toggle("selected", selected);
    button.setAttribute("aria-selected", String(selected));
    button.tabIndex = selected ? 0 : -1;
  });
  $("#trendHourlyPane").hidden = next !== "hourly";
  $("#trendDailyPane").hidden = next !== "daily";
  if (next === "hourly" && state.hourlyPoints.length) renderHourlyLine(state.hourlyPoints);
  if (next === "daily" && state.pulsePoints.length) renderPulse(state.pulsePoints);
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
  state.dataQualityNotes = [];
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
    ? t("dynamic.uncovered", { reasons })
    : t("dynamic.catalogAsOf", { date: cost.catalog_as_of || "—" });
  renderPulse(cost.points || []);
  renderOverviewModels(cost.models || []);
}

function renderPulse(allPoints) {
  state.pulsePoints = allPoints;
  let points = allPoints;
  const limited = points.length > 90;
  if (limited) points = points.slice(-90);
  const caption = $("#pulseCaption");
  if (caption) caption.textContent = limited ? t("dynamic.pulseLimited") : t("pulse.caption");
  const belt = $("#pulseBelt");
  if (!points.length) {
    belt.style.minWidth = "100%";
    belt.innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.pulseEmpty"))}</div>`;
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
    const label = t("dynamic.dayAria", { date: i18n.formatDate(date, { month: "long", day: "numeric" }), tokens: fullToken(total), estimate: estimateLabel(point.estimate) });
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
  $("#pulseDate").textContent = i18n.formatDate(date, { month: "long", day: "numeric", weekday: "short" });
  $("#pulseTotal").textContent = formatToken(usageTotal(point.usage));
  $("#pulseTotal").title = fullToken(usageTotal(point.usage));
  $("#pulseIO").textContent = `${formatToken(point.usage?.input)} / ${formatToken(point.usage?.output)}`;
  $("#pulseCost").textContent = `${estimateDisplay(point.estimate)} · ${formatPercent(point.estimate?.coverage_ratio || 0)}`;
}

function renderOverviewModels(models) {
  $("#modelCount").textContent = models.length ? t("common.models", { count: i18n.formatNumber(models.length) }) : t("dynamic.noModelData");
  const container = $("#overviewModels");
  if (!models.length) {
    container.innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.noModelsRange"))}</div>`;
    return;
  }
  const visible = models.slice(0, 6);
  const max = Math.max(...visible.map((item) => usageTotal(item.usage)), 1);
  container.innerHTML = visible.map((item) => {
    const total = usageTotal(item.usage);
    const cost = estimateDisplay(item.estimate);
    const coverage = estimateTokens(item.estimate) ? formatPercent(item.estimate.coverage_ratio) : t("dynamic.unpriced");
    return `<div class="rank-row"><span class="rank-name" title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><span class="rank-signal" aria-hidden="true"><i style="width:${Math.max(1, total / max * 100)}%"></i></span><span class="rank-value" title="${fullToken(total)} Token">${formatToken(total)}<small>${cost} · ${coverage}</small></span></div>`;
  }).join("");
}

async function loadDaily({ preserve = false } = {}) {
  const serial = ++state.requestSerial.daily;
  const bounds = monthBounds();
  $("#monthLabel").textContent = i18n.formatDate(state.monthCursor, { year: "numeric", month: "long" });
  syncDailyNavigator();
  if (!preserve) $("#calendarGrid").innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.monthLoading"))}</div>`;
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
      const latest = latestPoint ? { date: latestPoint.date || dateKey(new Date(latestPoint.time)) } : null;
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
    syncDailyNavigator();
    renderCalendar(report);
    if (state.selectedDate) loadDailySelection(state.selectedDate);
  } catch (error) {
    if (serial === state.requestSerial.daily) toast(t("dynamic.dailyError", { error: error.message }), true);
  } finally {
    if (serial === state.requestSerial.daily) $("#view-daily").removeAttribute("aria-busy");
  }
}

function chooseDailySelection(points) {
  const selected = state.selectedDate && dateFromKey(state.selectedDate);
  if (selected && !Number.isNaN(selected.getTime())
    && selected.getFullYear() === state.monthCursor.getFullYear()
    && selected.getMonth() === state.monthCursor.getMonth()) return;
  const today = points.find((point) => point.date === todayKey());
  if (today && usageTotal(today.usage) > 0) {
    state.selectedDate = today.date;
    return;
  }
  state.selectedDate = [...points].reverse().find((point) => usageTotal(point.usage) > 0)?.date || today?.date || points[0]?.date || "";
}

function syncDailyNavigator() {
  const picker = $("#dailyDatePicker");
  const fallback = dateKey(state.monthCursor);
  picker.value = state.selectedDate || fallback;
  picker.max = todayKey();
  syncDatePickerDisplay("dailyDate", picker.value);
  const currentMonth = new Date(new Date().getFullYear(), new Date().getMonth(), 1);
  $("#nextMonth").disabled = state.monthCursor >= currentMonth;
  $("#currentDay").disabled = state.selectedDate === todayKey();
}

function syncDatePickerDisplay(prefix, value) {
  const date = dateFromKey(value);
  if (!value || Number.isNaN(date.getTime()) || dateKey(date) !== value) return;
  $(`#${prefix}Year`).textContent = i18n.formatDate(date, { year: "numeric" });
  $(`#${prefix}Label`).textContent = i18n.formatDate(date, { month: "short", day: "numeric" });
}

function enableNativeDatePicker(picker) {
  const open = () => {
    try { picker.showPicker?.(); } catch {}
  };
  picker.addEventListener("click", open);
  picker.addEventListener("keydown", (event) => {
    if (!["Enter", " "].includes(event.key)) return;
    event.preventDefault();
    open();
  });
}

function navigateDailyDate(value) {
  const parsed = dateFromKey(value);
  if (!value || Number.isNaN(parsed.getTime()) || dateKey(parsed) !== value || value > todayKey()) {
    syncDailyNavigator();
    return;
  }
  const sameMonth = parsed.getFullYear() === state.monthCursor.getFullYear() && parsed.getMonth() === state.monthCursor.getMonth();
  state.dailyInitialSelection = false;
  if (sameMonth && state.monthReport) {
    selectCalendarDate(value);
    syncDailyNavigator();
    return;
  }
  state.monthCursor = new Date(parsed.getFullYear(), parsed.getMonth(), 1);
  state.selectedDate = value;
  loadDaily();
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
    const aria = t("dynamic.calendarDayAria", { day: i18n.formatNumber(day), tokens: fullToken(total), estimate: estimateLabel(point.estimate) });
    cells.push(`<button class="calendar-day pressable ${selected ? "selected" : ""} ${date === todayKey() ? "today" : ""} ${total ? "" : "zero"}" type="button" role="gridcell" data-calendar-date="${date}" aria-selected="${selected}" tabindex="${selected ? "0" : "-1"}" aria-label="${escapeHTML(aria)}"><span class="day-number">${i18n.formatNumber(day)}</span>${hasCost ? '<i class="cost-marker" aria-hidden="true"></i>' : ""}<span class="day-usage" aria-hidden="true"><i style="--day-strength:${strength}%"></i></span><span class="day-amount">${total ? formatToken(total) : "0"}</span></button>`);
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
  if (!date || date === state.selectedDate) {
    syncDailyNavigator();
    return;
  }
  state.selectedDate = date;
  syncDailyNavigator();
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
    if (serial === state.requestSerial.day) toast(t("dynamic.selectedError", { error: error.message }), true);
  }
}

function renderDayDetail(point, report, loadingModels = false) {
  const date = dateFromKey(point.date);
  const usage = point.usage || emptyUsage();
  const total = usageTotal(usage);
  const estimate = point.estimate || {};
  const title = i18n.formatDate(date, { month: "long", day: "numeric", weekday: "long" });
  $("#selectedDateTitle").textContent = title;
  $("#selectedDateTitle").dataset.loadedDate = report ? point.date : "";
  $("#selectedDayStatus").textContent = total ? t("dynamic.hasUsage") : t("dynamic.zeroUsage");
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
    $("#dayModels").innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.modelsLoading"))}</div>`;
    $("#dayModelCount").textContent = "";
    return;
  }
  const models = report?.models || [];
  $("#dayModelCount").textContent = models.length ? t("common.modelShort", { count: i18n.formatNumber(models.length) }) : "";
  $("#dayModels").innerHTML = models.length ? models.map((item) => `<div class="mini-model-row"><span title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><strong title="${fullToken(usageTotal(item.usage))}">${formatToken(usageTotal(item.usage))}</strong></div>`).join("") : `<div class="empty-state">${escapeHTML(t("dynamic.noModelsDay"))}</div>`;
}

async function loadBreakdown({ preserve = false } = {}) {
  const serial = ++state.requestSerial.breakdown;
  const bounds = rangeBounds(state.detailRange);
  const dimension = state.detailDimension;
  $("#breakdownTitle").textContent = t("dynamic.breakdownBy", { dimension: dimensionLabel(dimension) });
  if (!preserve) $("#detailBreakdown").innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.breakdownLoading", { dimension: dimensionLabel(dimension) }))}</div>`;
  $("#breakdownPanel").setAttribute("aria-busy", "true");
  try {
    const breakdown = await api(apiURL("/api/v1/breakdown", { ...bounds, dimension, limit: 100 }));
    if (serial !== state.requestSerial.breakdown) return;
    renderBreakdown(breakdown.items || [], dimension);
  } catch (error) {
    if (serial === state.requestSerial.breakdown) toast(t("dynamic.breakdownError", { error: error.message }), true);
  } finally {
    if (serial === state.requestSerial.breakdown) $("#breakdownPanel").removeAttribute("aria-busy");
  }
}

async function loadSessions({ preserve = false } = {}) {
  const serial = ++state.requestSerial.sessions;
  const estimateSerial = ++state.requestSerial.estimates;
  const bounds = rangeBounds(state.detailRange);
  const query = { ...bounds, limit: 100, compact: 1, q: state.sessionSearch };
  const rowsURL = apiURL("/api/v1/sessions", { ...query, include_estimate: 0 });
  const estimatesURL = apiURL("/api/v1/session-estimates", query);
  if (!preserve) $("#sessionRows").innerHTML = `<div class="empty-state">${escapeHTML(t("details.sessionsLoading"))}</div>`;
  $("#sessionsPanel").setAttribute("aria-busy", "true");
  try {
    const sessions = await api(rowsURL);
    if (serial !== state.requestSerial.sessions) return;
    const items = sessions.items || [];
    renderSessions(items, { estimatesPending: items.length > 0 });
    if (items.length) loadSessionEstimates(estimatesURL, serial, estimateSerial);
  } catch (error) {
    if (serial === state.requestSerial.sessions) toast(t("dynamic.sessionsError", { error: error.message }), true);
  } finally {
    if (serial === state.requestSerial.sessions) $("#sessionsPanel").removeAttribute("aria-busy");
  }
}

async function loadSessionEstimates(url, sessionSerial, estimateSerial) {
  try {
    const payload = await api(url);
    if (sessionSerial !== state.requestSerial.sessions || estimateSerial !== state.requestSerial.estimates) return;
    patchSessionEstimates(payload.items || []);
  } catch (error) {
    if (sessionSerial !== state.requestSerial.sessions || estimateSerial !== state.requestSerial.estimates) return;
    markSessionEstimatesUnavailable();
    toast(t("dynamic.sessionEstimateError", { error: error.message }), true);
  }
}

async function loadDetails({ preserve = false, breakdown = true, sessions = true } = {}) {
  const tasks = [];
  if (breakdown) tasks.push(loadBreakdown({ preserve }));
  if (sessions) tasks.push(loadSessions({ preserve }));
  await Promise.all(tasks);
}

function renderBreakdown(items, dimension) {
  const container = $("#detailBreakdown");
  const total = items.reduce((sum, item) => sum + usageTotal(item.usage), 0);
  $("#breakdownMeta").textContent = items.length ? t("dynamic.breakdownMeta", { count: i18n.formatNumber(items.length), tokens: formatToken(total) }) : t("common.noData");
  if (!items.length) {
    container.innerHTML = `<div class="empty-state">${escapeHTML(t("dynamic.breakdownEmpty", { dimension: dimensionLabel(dimension) }))}</div>`;
    return;
  }
  const max = Math.max(...items.map((item) => usageTotal(item.usage)), 1);
  const canFilter = dimension !== "thread";
  container.innerHTML = items.map((item) => {
    const totalTokens = usageTotal(item.usage);
    const active = state.filters[dimension] === item.key;
    const title = active
      ? t("dynamic.drillCancelTitle", { dimension: dimensionLabel(dimension) })
      : t("dynamic.drillTitle", { dimension: dimensionLabel(dimension) });
    const aria = active
      ? t("dynamic.drillCancelAria", { value: item.key })
      : t("dynamic.drillAria", { value: item.key });
    return `<div class="breakdown-row"><span class="breakdown-name" title="${escapeHTML(item.key)}">${escapeHTML(item.key)}</span><span class="breakdown-track" aria-hidden="true"><i style="width:${Math.max(1, totalTokens / max * 100)}%"></i></span><span class="breakdown-value" title="${fullToken(totalTokens)} Token">${formatToken(totalTokens)}</span>${canFilter ? `<button class="breakdown-filter pressable ${active ? "active" : ""}" type="button" data-drill-value="${escapeHTML(item.key)}" title="${escapeHTML(title)}" aria-label="${escapeHTML(aria)}" aria-pressed="${active}">${active ? "×" : "＋"}</button>` : "<span></span>"}</div>`;
  }).join("");
  $$('[data-drill-value]', container).forEach((button) => button.addEventListener("click", () => {
    if (state.filters[dimension] === button.dataset.drillValue) delete state.filters[dimension];
    else state.filters[dimension] = button.dataset.drillValue;
    renderFilterChips();
    syncFilterForm();
    resetDataSelections();
    loadDetails();
  }));
}

function sessionEstimatePresentation(estimate = {}) {
  const totalEstimateTokens = estimateTokens(estimate);
  const pricedTokens = Number(estimate.priced_tokens || 0);
  const cost = totalEstimateTokens && !pricedTokens ? t("dynamic.unpriced") : estimateDisplay(estimate);
  const partialCoverage = pricedTokens > 0 && Number(estimate.coverage_ratio || 0) < 1
    ? formatPercent(estimate.coverage_ratio) : "";
  const title = [estimateLabel(estimate), reasonSummary(estimate)].filter(Boolean).join(" · ");
  return { cost, partialCoverage, title };
}

function renderSessionEstimate(cell, estimate) {
  const presentation = sessionEstimatePresentation(estimate);
  cell.classList.remove("pending", "unavailable");
  cell.removeAttribute("aria-busy");
  cell.title = presentation.title;
  cell.innerHTML = `<small class="session-mobile-label">API</small><strong>${escapeHTML(presentation.cost)}</strong>${presentation.partialCoverage ? `<small>${escapeHTML(presentation.partialCoverage)}</small>` : ""}`;
}

function patchSessionEstimates(items) {
  const rows = new Map($$('[data-session-id]', $("#sessionRows")).map((row) => [row.dataset.sessionId, row]));
  const received = new Set();
  for (const item of items) {
    const row = rows.get(item.session_id);
    if (!row) continue;
    const cell = $('[data-session-cost]', row);
    if (cell) {
      received.add(item.session_id);
      renderSessionEstimate(cell, item.estimate || {});
    }
  }
  for (const [sessionID, row] of rows) {
    if (received.has(sessionID)) continue;
    const cell = $('[data-session-cost].pending', row);
    if (cell) renderSessionEstimateUnavailable(cell);
  }
}

function renderSessionEstimateUnavailable(cell) {
  cell.classList.remove("pending");
  cell.classList.add("unavailable");
  cell.removeAttribute("aria-busy");
  cell.title = t("details.costUnavailable");
  cell.innerHTML = `<small class="session-mobile-label">API</small><strong>${escapeHTML(t("details.costUnavailable"))}</strong>`;
}

function markSessionEstimatesUnavailable() {
  $$('[data-session-cost].pending', $("#sessionRows")).forEach(renderSessionEstimateUnavailable);
}

function renderSessions(items, { estimatesPending = false } = {}) {
  const container = $("#sessionRows");
  if (!items.length) {
    const key = state.sessionSearch ? "dynamic.sessionsSearchEmpty" : "dynamic.sessionsEmpty";
    container.innerHTML = `<div class="empty-state">${escapeHTML(t(key))}</div>`;
    return;
  }
  container.innerHTML = items.map((item) => {
    const presentation = sessionEstimatePresentation(item.estimate || {});
    const active = state.filters.session_id === item.session_id;
    const costCell = estimatesPending
      ? `<div class="session-metric session-cost pending" data-session-cost aria-busy="true"><small class="session-mobile-label">API</small><strong>${escapeHTML(t("details.costPending"))}</strong></div>`
      : `<div class="session-metric session-cost" data-session-cost title="${escapeHTML(presentation.title)}"><small class="session-mobile-label">API</small><strong>${escapeHTML(presentation.cost)}</strong>${presentation.partialCoverage ? `<small>${escapeHTML(presentation.partialCoverage)}</small>` : ""}</div>`;
    return `<article class="session-row" data-session-id="${escapeHTML(item.session_id)}">
    <div class="session-cell"><strong title="${escapeHTML(item.title || item.session_id)}">${escapeHTML(item.title || t("dynamic.untitledThread"))}</strong><small title="${escapeHTML(item.session_id)}">${escapeHTML(shortId(item.session_id))}</small></div>
    <div class="session-cell"><span title="${escapeHTML(item.project_path || t("common.notRecorded"))}">${escapeHTML(shortPath(item.project_path))}</span><small title="${escapeHTML(item.project_path || "")}">${escapeHTML(item.project_path || t("common.notRecorded"))}</small></div>
    <div class="session-cell"><strong title="${escapeHTML(item.model || t("common.unknownModel"))}">${escapeHTML(item.model || t("common.unknownModel"))}</strong><span>${escapeHTML(item.source || t("common.unknownSource"))}</span></div>
    <div class="session-cell"><span class="agent-badge">${escapeHTML(item.agent_type || "main")}</span><span class="confidence-badge ${escapeHTML(item.confidence)}">${confidenceLabel(item.confidence)}</span></div>
    <div class="session-metric session-token" title="${fullToken(usageTotal(item.usage))} Token"><small class="session-mobile-label">Token</small><strong>${formatToken(usageTotal(item.usage))}</strong></div>
    ${costCell}
    <div class="session-cell"><span>${escapeHTML(localTime(item.last_usage))}</span></div>
    <button class="session-filter pressable ${active ? "active" : ""}" type="button" data-session-filter="${escapeHTML(item.session_id)}" aria-pressed="${active}">${escapeHTML(t(active ? "details.cancelSessionFilter" : "details.onlySession"))}</button>
  </article>`;
  }).join("");
  $$('[data-session-filter]', container).forEach((button) => button.addEventListener("click", () => {
    if (state.filters.session_id === button.dataset.sessionFilter) delete state.filters.session_id;
    else state.filters.session_id = button.dataset.sessionFilter;
    renderFilterChips();
    syncFilterForm();
    resetDataSelections();
    loadDetails();
  }));
}

function applySessionSearch() {
  clearTimeout(sessionSearchTimer);
  const value = $("#sessionSearch").value.trim();
  $("#sessionSearchClear").classList.toggle("hidden", !value);
  if (value === state.sessionSearch) return;
  state.sessionSearch = value;
  loadSessions();
}

async function loadCurrentView(options = {}) {
  if (state.view === "daily") return loadDaily(options);
  if (state.view === "details") return loadDetails(options);
  return loadOverview(options);
}

function switchView(next) {
  if (!next || next === state.view) return;
  const currentPanel = $(`[data-view-panel="${state.view}"]`);
  const nextPanel = $(`[data-view-panel="${next}"]`);
  const preserve = Boolean(state.viewVisited[next]);
  state.view = next;
  $$('.nav-tab').forEach((tab) => {
    const selected = tab.dataset.view === next;
    tab.classList.toggle("active", selected);
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
  });
  history.replaceState(null, "", `#${next}`);
  currentPanel.hidden = true;
  currentPanel.classList.remove("active", "entering");
  nextPanel.hidden = false;
  nextPanel.classList.add("active", "entering");
  state.viewVisited[next] = true;
  loadCurrentView({ preserve });
  requestAnimationFrame(() => setTimeout(() => nextPanel.classList.remove("entering"), reducedMotion() ? 0 : 140));
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
  container.innerHTML = `<div class="empty-state">${escapeHTML(t("common.loading"))}</div>`;
  try {
    const payload = await api("/api/v1/warnings?limit=200");
    const items = payload.items || [];
    container.innerHTML = items.length ? items.map((item) => {
      const occurrences = Number(item.occurrences || 1);
      const firstSeen = item.first_seen || item.created_at;
      const period = occurrences > 1 ? t("warning.firstSeenMany", { time: localTime(firstSeen), count: fullToken(occurrences) }) : t("warning.firstSeen");
      const key = `warning.${item.kind}`;
      const label = t(key) === key ? item.kind : t(key);
      return `<article class="warning-row"><time>${escapeHTML(t("warning.recent", { time: localTime(item.created_at) }))}<small>${escapeHTML(period)}</small></time><code title="${escapeHTML(item.kind)}">${escapeHTML(label)}</code><details><summary>${escapeHTML(t("warning.raw"))}</summary><p title="${escapeHTML(item.path || "")}">${escapeHTML(item.detail)}</p></details></article>`;
    }).join("") : `<div class="empty-state">${escapeHTML(t("warning.none"))}</div>`;
  } catch (error) {
    container.innerHTML = `<div class="empty-state">${escapeHTML(error.message)}</div>`;
  }
}

function setExportLinks() {
  const bounds = currentBounds();
  const csvPath = apiURL("/api/v1/export", { ...bounds, format: "csv" });
  const jsonPath = apiURL("/api/v1/export", { ...bounds, format: "json" });
  $("#exportCsv").href = window.CodexUsageDemo?.createExportURL?.("csv", csvPath) || csvPath;
  $("#exportJson").href = window.CodexUsageDemo?.createExportURL?.("json", jsonPath) || jsonPath;
}

async function openPricingDialog() {
  openDialog($("#pricingDialog"));
  $("#pricingOverrideList").innerHTML = `<div class="empty-state">${escapeHTML(t("pricing.loading"))}</div>`;
  try {
    state.pricing = await api("/api/v1/pricing");
    renderPricing(state.pricing);
  } catch (error) {
    $("#pricingOverrideList").innerHTML = `<div class="empty-state">${escapeHTML(error.message)}</div>`;
  }
}

function renderPricing(payload) {
  const unpriced = payload.unpriced_models || [];
  $("#pricingMeta").textContent = t("pricing.meta", { currency: payload.currency, date: payload.catalog_as_of, count: i18n.formatNumber(unpriced.length) });
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
      list.insertAdjacentHTML("beforeend", `<article class="override-card"><div class="override-card-head"><div><strong>${escapeHTML(t("pricing.unknownName"))}</strong><small>${escapeHTML(t("pricing.observed", { tokens: fullToken(tokens) }))}</small></div></div><p class="override-hint">${escapeHTML(t("pricing.unknownHint"))}</p></article>`);
      continue;
    }
    appendOverrideCard(model, overrides[model], observed.get(model));
  }
  if (!list.children.length) list.innerHTML = `<div class="empty-state">${escapeHTML(t("pricing.empty"))}</div>`;
}

function renderCatalog(catalog) {
  $("#pricingCatalog").innerHTML = `<div class="catalog-row header"><span>${escapeHTML(t("pricing.catalogModel"))}</span><span>Input</span><span>Cached</span><span>Write</span><span>Output</span></div>${catalog.map((entry) => `<div class="catalog-row"><a href="${escapeHTML(entry.source)}" target="_blank" rel="noreferrer">${escapeHTML(entry.display_name)}</a><span>${escapeHTML(entry.input_usd_per_million)}</span><span>${escapeHTML(entry.cached_input_usd_per_million)}</span><span>${escapeHTML(entry.cache_write_input_usd_per_million || "—")}</span><span>${escapeHTML(entry.output_usd_per_million)}</span></div>`).join("")}`;
}

function appendOverrideCard(model, override = null, observedTokens = null) {
  const list = $("#pricingOverrideList");
  if ($('.empty-state', list)) list.innerHTML = "";
  if ($$('[data-pricing-model]', list).some((card) => card.dataset.pricingModel.toLowerCase() === model.toLowerCase())) {
    toast(t("pricing.duplicate"), true);
    return;
  }
  const mode = override?.alias_of ? "alias" : override ? "custom" : "unpriced";
  const aliasOptions = (state.pricing?.catalog || []).map((entry) => `<option value="${escapeHTML(entry.model)}" ${override?.alias_of === entry.model ? "selected" : ""}>${escapeHTML(entry.display_name)}</option>`).join("");
  const card = document.createElement("article");
  card.className = "override-card";
  card.dataset.pricingModel = model;
  card.innerHTML = `<div class="override-card-head"><div><strong>${escapeHTML(model)}</strong><small>${observedTokens == null ? escapeHTML(t("pricing.localCustom")) : escapeHTML(t("pricing.observed", { tokens: fullToken(observedTokens) }))}</small></div><button class="remove-override pressable" type="button">${escapeHTML(t("action.remove"))}</button></div>
    <div class="override-mode">
      <label>${escapeHTML(t("pricing.mode"))}<select data-rate-mode><option value="unpriced" ${mode === "unpriced" ? "selected" : ""}>${escapeHTML(t("pricing.keepUnpriced"))}</option><option value="alias" ${mode === "alias" ? "selected" : ""}>${escapeHTML(t("pricing.alias"))}</option><option value="custom" ${mode === "custom" ? "selected" : ""}>${escapeHTML(t("pricing.custom"))}</option></select></label>
      <label class="alias-field">${escapeHTML(t("pricing.publicModel"))}<select data-alias><option value="">${escapeHTML(t("pricing.chooseModel"))}</option>${aliasOptions}</select></label>
      <div class="custom-rate-grid">
        <label>Input<input data-rate="input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Cached<input data-rate="cached_input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.cached_input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Cache Write<input data-rate="cache_write_input_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.cache_write_input_usd_per_million || "")}" placeholder="USD / 1M"></label>
        <label>Output<input data-rate="output_usd_per_million" inputmode="decimal" value="${escapeHTML(override?.output_usd_per_million || "")}" placeholder="USD / 1M"></label>
      </div>
      <p class="override-hint">${escapeHTML(t("pricing.rateHint"))}</p>
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
      if (!alias) throw new Error(t("pricing.aliasMissing", { model }));
      overrides[model] = { alias_of: alias };
      continue;
    }
    const value = {};
    for (const input of $$('[data-rate]', card)) {
      const rate = input.value.trim();
      if (rate) value[input.dataset.rate] = rate;
    }
    for (const key of ["input_usd_per_million", "cached_input_usd_per_million", "cache_write_input_usd_per_million", "output_usd_per_million"]) {
      if (!value[key]) throw new Error(t("pricing.rateMissing", { model, rate: key.replace("_usd_per_million", "") }));
    }
    for (const rate of Object.values(value)) if (!decimal.test(rate)) throw new Error(t("pricing.rateInvalid", { model }));
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
    toast(t("pricing.saved"));
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
  $$('[data-trend-view]').forEach((button) => button.addEventListener("click", () => switchTrendView(button.dataset.trendView)));
  $("#usageTrendPanel").querySelector('[role="tablist"]').addEventListener("keydown", (event) => tablistKeydown(event, $$('[data-trend-view]'), (button) => switchTrendView(button.dataset.trendView)));
  $("#previousHourDay").addEventListener("click", () => navigateHourlyDate(dateKey(addDays(state.hourlyDate, -1)), -1));
  $("#nextHourDay").addEventListener("click", () => navigateHourlyDate(dateKey(addDays(state.hourlyDate, 1)), 1));
  $("#currentHourDay").addEventListener("click", () => navigateHourlyDate(todayKey(), 1));
  $("#hourlyDatePicker").addEventListener("change", (event) => navigateHourlyDate(event.target.value));
  enableNativeDatePicker($("#hourlyDatePicker"));
  $$('[data-overview-range]').forEach((button) => button.addEventListener("click", () => {
    state.overviewRange = button.dataset.overviewRange;
    delete state.filters.date;
    resetDataSelections();
    renderFilterChips();
    syncFilterForm();
    loadOverview();
  }));
  $$('[data-detail-range]').forEach((button) => button.addEventListener("click", () => {
    state.detailRange = button.dataset.detailRange;
    delete state.filters.date;
    resetDataSelections();
    renderFilterChips();
    syncFilterForm();
    loadDetails();
  }));
  $("#detailDatePicker").addEventListener("change", (event) => {
    const value = event.target.value;
    const parsed = dateFromKey(value);
    if (value && (Number.isNaN(parsed.getTime()) || dateKey(parsed) !== value || value > todayKey())) {
      syncRangeControls();
      return;
    }
    if (value) state.filters.date = value;
    else delete state.filters.date;
    resetDataSelections();
    renderFilterChips();
    syncFilterForm();
    loadDetails();
  });
  enableNativeDatePicker($("#detailDatePicker"));
  $$('[data-dimension]').forEach((button) => button.addEventListener("click", () => {
    state.detailDimension = button.dataset.dimension;
    $$('[data-dimension]').forEach((item) => {
      const selected = item === button;
      item.classList.toggle("selected", selected);
      item.setAttribute("aria-selected", String(selected));
      item.tabIndex = selected ? 0 : -1;
    });
    loadBreakdown();
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
  $("#dailyDatePicker").addEventListener("change", (event) => navigateDailyDate(event.target.value));
  enableNativeDatePicker($("#dailyDatePicker"));
  $("#currentDay").addEventListener("click", () => navigateDailyDate(todayKey()));
  $("#filterButton").addEventListener("click", () => {
    syncFilterForm();
    openDialog($("#filterSheet"));
    loadFilterOptions();
  });
  $("#applyFilters").addEventListener("click", applyFiltersFromForm);
  $("#filterForm").addEventListener("submit", (event) => { event.preventDefault(); applyFiltersFromForm(); });
  $("#resetFilters").addEventListener("click", () => {
    for (const meta of Object.values(FILTER_FIELDS)) $(meta.selector).value = "";
  });
  $("#clearFilters").addEventListener("click", clearAllFilters);
  $("#sessionSearch").addEventListener("input", () => {
    const value = $("#sessionSearch").value;
    $("#sessionSearchClear").classList.toggle("hidden", !value);
    clearTimeout(sessionSearchTimer);
    sessionSearchTimer = setTimeout(applySessionSearch, 250);
  });
  $("#sessionSearch").addEventListener("keydown", (event) => {
    if (event.key === "Enter") {
      event.preventDefault();
      applySessionSearch();
    }
  });
  $("#sessionSearch").addEventListener("search", applySessionSearch);
  $("#sessionSearchClear").addEventListener("click", () => {
    $("#sessionSearch").value = "";
    applySessionSearch();
    $("#sessionSearch").focus();
  });
  $("#settingsButton").addEventListener("click", () => { syncSettingsForm(); openDialog($("#settingsDialog")); });
  $("#settingsForm").addEventListener("change", (event) => {
    const input = event.target.closest('[data-setting]');
    if (!input?.checked) return;
    if (input.dataset.setting === "locale") {
      i18n.setLocale(input.value);
      return;
    }
    updateDisplayPreference(input.dataset.setting, input.value);
  });
  $("#resetSettings").addEventListener("click", resetDisplayPreferences);
  $("#warningButton").addEventListener("click", () => { openDialog($("#warningsDialog")); loadWarnings(); });
  $("#pricingButton").addEventListener("click", openPricingDialog);
  $("#addOverride").addEventListener("click", () => {
    const input = $("#newOverrideModel");
    const model = input.value.trim().toLowerCase();
    if (!model) { toast(t("pricing.modelRequired"), true); return; }
    appendOverrideCard(model);
    input.value = "";
  });
  $("#newOverrideModel").addEventListener("keydown", (event) => {
    if (event.key === "Enter") { event.preventDefault(); $("#addOverride").click(); }
  });
  $("#savePricing").addEventListener("click", savePricing);
  $("#exportButton").addEventListener("click", () => { setExportLinks(); openDialog($("#exportDialog")); });
  const runScan = async (rebuild = false) => {
    const button = $("#scanButton");
    const rebuildButton = $("#confirmRebuild");
    button.disabled = true;
    rebuildButton.disabled = true;
    $(".scan-icon").classList.add("spin");
    try {
      const refreshFilterOptions = Boolean(state.filterOptions);
      const result = await api("/api/v1/rescan", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ rebuild })
      });
      invalidateDataCache();
      state.filterOptions = null;
      toast(t("scan.complete", { inserted: i18n.formatNumber(result.events_inserted || 0), duplicates: i18n.formatNumber(result.duplicates || 0) }));
      await loadStatus();
      await Promise.all([refreshFilterOptions ? loadFilterOptions({ force: true }) : Promise.resolve(), loadCurrentView()]);
    } catch (error) {
      if (!rebuild && error.payload?.rebuild_required) {
        $("#rebuildDetail").textContent = error.payload.detail || error.message;
        openDialog($("#rebuildDialog"));
      } else {
        toast(error.message, true);
      }
    } finally {
      button.disabled = false;
      rebuildButton.disabled = false;
      $(".scan-icon").classList.remove("spin");
    }
  };
  $("#scanButton").addEventListener("click", () => runScan(false));
  $("#confirmRebuild").addEventListener("click", async () => {
    closeDialog($("#rebuildDialog"));
    await runScan(true);
  });
  $("#themeButton").addEventListener("click", () => {
    const dark = displayPreferences.theme === "system"
      ? window.matchMedia("(prefers-color-scheme: dark)").matches
      : displayPreferences.theme === "dark";
    const next = dark ? "light" : "dark";
    updateDisplayPreference("theme", next);
  });
  $("#localeButton").addEventListener("click", () => {
    i18n.setLocale(i18n.getLocale() === "en" ? "zh-CN" : "en");
  });
  window.addEventListener("codex-usage-locale-change", async () => {
    syncSettingsForm();
    renderFilterOptions();
    renderFilterChips();
    try {
      await Promise.all([loadStatus(), loadCurrentView({ preserve: false })]);
    } catch (error) {
      toast(error.message, true);
    }
  });
}

function scheduleStatusPoll(delay = document.visibilityState === "visible" ? VISIBLE_STATUS_POLL_MS : HIDDEN_STATUS_POLL_MS) {
  clearTimeout(statusPollTimer);
  statusPollTimer = setTimeout(pollStatus, delay);
}

async function pollStatus() {
  if (statusPollRunning) return scheduleStatusPoll();
  statusPollRunning = true;
  try {
    const changed = await loadStatus();
    if (changed && document.visibilityState !== "visible") state.pendingDataRefresh = true;
    if (document.visibilityState === "visible" && (changed || state.pendingDataRefresh)) {
      state.pendingDataRefresh = false;
      await loadCurrentView({ preserve: true });
    }
  } catch {} finally {
    statusPollRunning = false;
    scheduleStatusPoll();
  }
}

function startStatusPolling() {
  document.addEventListener("visibilitychange", () => {
    scheduleStatusPoll(document.visibilityState === "visible" ? 0 : HIDDEN_STATUS_POLL_MS);
  });
  scheduleStatusPoll();
}

async function boot() {
  applyDisplayPreferences();
  i18n.applyStatic();
  if (window.CODEX_USAGE_DEMO || window.CodexUsageDemo) $("#demoBanner").classList.remove("hidden");
  setupEvents();
  renderFilterChips();
  const requestedView = location.hash.slice(1);
  if (["daily", "details"].includes(requestedView)) {
    const initial = state.view;
    state.view = requestedView;
    state.viewVisited[requestedView] = true;
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
    loadCurrentView()
  ]);
  startStatusPolling();
}

boot();
