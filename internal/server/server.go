package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/pricing"
	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
	usageweb "github.com/zJay26/codex-usage/internal/web"
)

type Server struct {
	Store                *store.Store
	Scanner              *usage.Scanner
	Homes                func() ([]string, error)
	Address              string
	Port                 int
	Version              string
	LoadPricingOverrides func() (map[string]pricing.Override, error)
	SavePricingOverrides func(map[string]pricing.Override) error

	scanMu            sync.Mutex
	pricingMu         sync.Mutex
	dimensionMu       sync.Mutex
	dimensionRevision uint64
	dimensionCache    store.DimensionValues
	scanning          atomic.Bool
}

func (s *Server) URL() string {
	host := s.Address
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port))
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
	})
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/summary", s.handleSummary)
	mux.HandleFunc("/api/v1/timeseries", s.handleTimeseries)
	mux.HandleFunc("/api/v1/breakdown", s.handleBreakdown)
	mux.HandleFunc("/api/v1/dimensions", s.handleDimensions)
	mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	mux.HandleFunc("/api/v1/warnings", s.handleWarnings)
	mux.HandleFunc("/api/v1/cost-estimate", s.handleCostEstimate)
	mux.HandleFunc("/api/v1/pricing", s.handlePricing)
	mux.HandleFunc("/api/v1/pricing/overrides", s.handlePricingOverrides)
	mux.HandleFunc("/api/v1/rescan", s.handleRescan)
	mux.HandleFunc("/api/v1/export", s.handleExport)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/", usageweb.Handler())

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		isLocalMutation := strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet
		if isLocalMutation && !safeOrigin(r) {
			http.Error(w, "拒绝非本机来源", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) Run(ctx context.Context) error {
	host := s.Address
	if host == "" || host == "localhost" {
		host = "127.0.0.1"
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("拒绝绑定非 loopback 地址 %q", host)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	status, err := s.Store.Status(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	homes, _ := s.Homes()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          s.Version,
		"url":              s.URL(),
		"scanning":         s.scanning.Load() || s.Scanner.Busy(),
		"status":           status,
		"configured_homes": homes,
		"privacy": map[string]any{
			"loopback_only": true,
			"uploads":       false,
			"credentials":   false,
			"content":       false,
		},
	})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := s.Store.Summary(r.Context(), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	if bucket != "day" && bucket != "hour" {
		http.Error(w, "bucket 只能是 day 或 hour", http.StatusBadRequest)
		return
	}
	points, err := s.Store.Timeseries(r.Context(), filter, bucket)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": bucket, "points": points})
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dimension := r.URL.Query().Get("dimension")
	if dimension == "" {
		dimension = "model"
	}
	items, err := s.Store.Breakdown(r.Context(), filter, dimension, parseInt(r.URL.Query().Get("limit"), 25))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dimension": dimension, "items": items})
}

func (s *Server) handleDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	revision := s.Store.Revision()
	s.dimensionMu.Lock()
	if s.dimensionRevision != revision {
		values, err := s.Store.Dimensions(r.Context())
		if err != nil {
			s.dimensionMu.Unlock()
			writeError(w, err)
			return
		}
		s.dimensionCache = values
		s.dimensionRevision = revision
	}
	values := s.dimensionCache
	s.dimensionMu.Unlock()
	writeJSON(w, http.StatusOK, values)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.Store.Sessions(r.Context(), filter,
		parseInt(r.URL.Query().Get("limit"), 100), parseInt(r.URL.Query().Get("offset"), 0))
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("compact") == "1" || strings.EqualFold(r.URL.Query().Get("compact"), "true") {
		for index := range items {
			items[index].Title = compactText(items[index].Title, 240)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleWarnings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	items, err := s.Store.Warnings(r.Context(), parseInt(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCostEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	if bucket != "day" {
		http.Error(w, "bucket 只能是 day", http.StatusBadRequest)
		return
	}
	overrides, err := s.pricingOverrides()
	if err != nil {
		writeError(w, err)
		return
	}
	builder, err := pricing.NewBuilder(overrides)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.forEachPricingEvent(r.Context(), filter, builder.Add); err != nil {
		writeError(w, err)
		return
	}
	report, err := pricing.FillDaily(builder.Report(), filter.Since, filter.Until, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

type pricingCatalogResponse struct {
	Basis          string                      `json:"basis"`
	Currency       string                      `json:"currency"`
	CatalogAsOf    string                      `json:"catalog_as_of"`
	Catalog        []pricing.CatalogEntry      `json:"catalog"`
	Overrides      map[string]pricing.Override `json:"overrides"`
	UnpricedModels []model.BreakdownItem       `json:"unpriced_models"`
}

func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	overrides, err := s.pricingOverrides()
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := s.pricingResponse(r.Context(), overrides)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePricingOverrides(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, http.MethodPut)
		return
	}
	if s.SavePricingOverrides == nil {
		http.Error(w, "当前运行模式不支持保存定价覆写", http.StatusNotImplemented)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Overrides *map[string]pricing.Override `json:"overrides"`
	}
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "无效请求体: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		http.Error(w, "无效请求体: "+err.Error(), http.StatusBadRequest)
		return
	}
	if payload.Overrides == nil {
		http.Error(w, "请求体必须包含 overrides", http.StatusBadRequest)
		return
	}
	normalized, err := pricing.NormalizeOverrides(*payload.Overrides)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.pricingMu.Lock()
	err = s.SavePricingOverrides(normalized)
	s.pricingMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	response, err := s.pricingResponse(r.Context(), normalized)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) pricingOverrides() (map[string]pricing.Override, error) {
	if s.LoadPricingOverrides == nil {
		return nil, nil
	}
	s.pricingMu.Lock()
	defer s.pricingMu.Unlock()
	overrides, err := s.LoadPricingOverrides()
	if err != nil {
		return nil, err
	}
	return pricing.NormalizeOverrides(overrides)
}

func (s *Server) pricingResponse(ctx context.Context, overrides map[string]pricing.Override) (pricingCatalogResponse, error) {
	items, err := s.Store.Breakdown(ctx, model.Filter{}, "model", 500)
	if err != nil {
		return pricingCatalogResponse{}, err
	}
	unpriced := make([]model.BreakdownItem, 0)
	for _, item := range items {
		_, found, resolveErr := pricing.Resolve(item.Key, overrides)
		if resolveErr != nil {
			return pricingCatalogResponse{}, resolveErr
		}
		if !found {
			unpriced = append(unpriced, item)
		}
	}
	if overrides == nil {
		overrides = map[string]pricing.Override{}
	}
	return pricingCatalogResponse{
		Basis: pricing.Basis, Currency: pricing.Currency, CatalogAsOf: pricing.CatalogAsOf,
		Catalog: pricing.Catalog(), Overrides: overrides, UnpricedModels: unpriced,
	}, nil
}

func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !s.scanMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "扫描正在进行"})
		return
	}
	defer s.scanMu.Unlock()
	if s.Scanner.Busy() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "后台扫描正在进行"})
		return
	}
	s.scanning.Store(true)
	defer s.scanning.Store(false)
	homes, err := s.Homes()
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.Scanner.Scan(r.Context(), homes, false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "json"
	}
	stamp := time.Now().Format("20060102-150405")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="codex-usage-`+stamp+`.csv"`)
		w.Write([]byte{0xEF, 0xBB, 0xBF})
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{
			"timestamp", "machine_id", "session_id", "turn_id", "model", "source", "agent_type",
			"project_path", "thread_title", "input", "cached_input", "cache_write_input",
			"output", "reasoning_output", "total", "provenance", "confidence", "codex_home",
		})
		err = s.forEachEvent(r.Context(), filter, func(event model.UsageEvent) error {
			return writer.Write([]string{
				formatTime(event.Timestamp), csvText(event.MachineID), csvText(event.SessionID),
				csvText(event.TurnID), csvText(event.Model),
				csvText(event.Source), csvText(event.AgentType), csvText(event.ProjectPath), csvText(event.ThreadTitle),
				strconv.FormatInt(event.Usage.Input, 10),
				strconv.FormatInt(event.Usage.CachedInput, 10),
				strconv.FormatInt(event.Usage.CacheWriteInput, 10),
				strconv.FormatInt(event.Usage.Output, 10),
				strconv.FormatInt(event.Usage.ReasoningOutput, 10),
				strconv.FormatInt(event.Usage.Total, 10),
				csvText(event.Provenance), csvText(event.Confidence), csvText(event.CodexHome),
			})
		})
		writer.Flush()
		if err == nil {
			err = writer.Error()
		}
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="codex-usage-`+stamp+`.json"`)
		w.Write([]byte("[\n"))
		first := true
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		err = s.forEachEvent(r.Context(), filter, func(event model.UsageEvent) error {
			if !first {
				if _, writeErr := w.Write([]byte(",\n")); writeErr != nil {
					return writeErr
				}
			}
			first = false
			return encoder.Encode(event)
		})
		w.Write([]byte("]\n"))
	default:
		http.Error(w, "format 只能是 json 或 csv", http.StatusBadRequest)
		return
	}
	if err != nil {
		// Headers may already be committed; close the stream by returning.
		return
	}
}

func (s *Server) forEachEvent(ctx context.Context, filter model.Filter, fn func(model.UsageEvent) error) error {
	offset := 0
	for {
		items, err := s.Store.Events(ctx, store.EventQuery{Filter: filter, Limit: 5000, Offset: offset})
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := fn(item); err != nil {
				return err
			}
		}
		if len(items) < 5000 {
			return nil
		}
		offset += len(items)
	}
}

func (s *Server) forEachPricingEvent(ctx context.Context, filter model.Filter, fn func(model.UsageEvent) error) error {
	return s.Store.WalkPricingAggregates(ctx, filter, fn)
}

func compactText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("只能提交一个 JSON 对象")
		}
		return err
	}
	return nil
}

func parseFilter(query url.Values) (model.Filter, error) {
	var filter model.Filter
	var err error
	if value := query.Get("since"); value != "" && value != "all" {
		filter.Since, err = parseSince(value)
		if err != nil {
			return filter, fmt.Errorf("since: %w", err)
		}
		if isDateOnly(value) {
			filter.SinceDate = value
		} else if strings.EqualFold(value, "today") {
			filter.SinceDate = time.Now().In(time.Local).Format("2006-01-02")
		}
	}
	if value := query.Get("until"); value != "" {
		filter.Until, err = parseAbsoluteTime(value)
		if err != nil {
			return filter, fmt.Errorf("until: %w", err)
		}
		if isDateOnly(value) {
			filter.UntilDate = value
		}
	}
	filter.Model = query.Get("model")
	filter.Source = query.Get("source")
	filter.AgentType = query.Get("agent_type")
	filter.Project = query.Get("project")
	filter.SessionID = query.Get("session_id")
	filter.Confidence = query.Get("confidence")
	return filter, nil
}

func isDateOnly(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func ParseSince(value string) (time.Time, error) { return parseSince(value) }

func parseSince(value string) (time.Time, error) {
	now := time.Now()
	switch strings.ToLower(value) {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "all":
		return time.Time{}, nil
	}
	if strings.HasSuffix(value, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("无效天数 %q", value)
		}
		return now.AddDate(0, 0, -n), nil
	}
	if strings.HasSuffix(value, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(value, "w"))
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("无效周数 %q", value)
		}
		return now.AddDate(0, 0, -7*n), nil
	}
	if duration, err := time.ParseDuration(value); err == nil {
		return now.Add(-duration), nil
	}
	return parseAbsoluteTime(value)
}

func parseAbsoluteTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析 %q", value)
}

func safeOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func csvText(value string) string {
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
