package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/pricing"
	"github.com/zJay26/codex-usage/internal/store"
)

type sessionQueryKey struct {
	Revision                   uint64
	SinceUnix, UntilUnix       int64
	SinceDate, UntilDate       string
	Model, Source, AgentType   string
	Project, SessionID, Search string
	Confidence                 string
	Limit, Offset              int
	Compact                    bool
}

type sessionEstimateCacheKey struct {
	Query           sessionQueryKey
	PricingRevision uint64
}

type sessionResponseItem struct {
	store.SessionRow
	Estimate pricing.Estimate `json:"estimate"`
}

type sessionEstimateResponseItem struct {
	SessionID string           `json:"session_id"`
	Estimate  pricing.Estimate `json:"estimate"`
}

func timeCacheKey(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func makeSessionQueryKey(revision uint64, filter model.Filter, limit, offset int, compact bool) sessionQueryKey {
	return sessionQueryKey{
		Revision: revision, SinceUnix: timeCacheKey(filter.Since), UntilUnix: timeCacheKey(filter.Until),
		SinceDate: filter.SinceDate, UntilDate: filter.UntilDate,
		Model: filter.Model, Source: filter.Source, AgentType: filter.AgentType,
		Project: filter.Project, SessionID: filter.SessionID, Search: filter.Search,
		Confidence: filter.Confidence, Limit: limit, Offset: offset, Compact: compact,
	}
}

func sessionRequestParameters(r *http.Request) (model.Filter, int, int, bool, error) {
	filter, err := parseFilter(r.URL.Query())
	if err != nil {
		return model.Filter{}, 0, 0, false, err
	}
	limit := parseInt(r.URL.Query().Get("limit"), 100)
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	compact := r.URL.Query().Get("compact") == "1" || strings.EqualFold(r.URL.Query().Get("compact"), "true")
	return filter, limit, offset, compact, nil
}

func (s *Server) cachedSessionRows(ctx context.Context, filter model.Filter, limit, offset int, compact bool) (sessionQueryKey, []store.SessionRow, bool, error) {
	key := makeSessionQueryKey(s.Store.Revision(), filter, limit, offset, compact)
	if items, ok := s.sessionRowsCache.get(key); ok {
		return key, items, true, nil
	}
	items, err := s.Store.Sessions(ctx, filter, limit, offset)
	if err != nil {
		return key, nil, false, err
	}
	if compact {
		for index := range items {
			items[index].Title = compactText(items[index].Title, 240)
		}
	}
	s.sessionRowsCache.put(key, items)
	return key, items, false, nil
}

func (s *Server) cachedSessionEstimates(ctx context.Context, queryKey sessionQueryKey, filter model.Filter, items []store.SessionRow) ([]sessionEstimateResponseItem, bool, error) {
	cacheKey := sessionEstimateCacheKey{Query: queryKey, PricingRevision: s.pricingRevision.Load()}
	if estimates, ok := s.sessionEstimateCache.get(cacheKey); ok {
		return estimates, true, nil
	}
	overrides, err := s.pricingOverrides()
	if err != nil {
		return nil, false, err
	}
	builders := make(map[string]*pricing.Builder, len(items))
	sessionIDs := make([]string, 0, len(items))
	for _, item := range items {
		builder, buildErr := pricing.NewBuilder(overrides)
		if buildErr != nil {
			return nil, false, buildErr
		}
		builders[item.SessionID] = builder
		sessionIDs = append(sessionIDs, item.SessionID)
	}
	if err := s.Store.WalkSessionPricingAggregates(ctx, filter, sessionIDs, func(event model.UsageEvent) error {
		builder := builders[event.SessionID]
		if builder == nil {
			return nil
		}
		return builder.Add(event)
	}); err != nil {
		return nil, false, err
	}
	estimates := make([]sessionEstimateResponseItem, 0, len(items))
	for _, item := range items {
		estimates = append(estimates, sessionEstimateResponseItem{
			SessionID: item.SessionID,
			Estimate:  builders[item.SessionID].Report().Summary,
		})
	}
	s.sessionEstimateCache.put(cacheKey, estimates)
	return estimates, false, nil
}

func includeSessionEstimates(r *http.Request) bool {
	value := strings.TrimSpace(r.URL.Query().Get("include_estimate"))
	return value != "0" && !strings.EqualFold(value, "false")
}

func timingMetric(name string, elapsed time.Duration, hit bool) string {
	state := "miss"
	if hit {
		state = "hit"
	}
	return fmt.Sprintf(`%s;dur=%.2f;desc="%s"`, name, float64(elapsed.Microseconds())/1000, state)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, limit, offset, compact, err := sessionRequestParameters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rowsStarted := time.Now()
	queryKey, items, rowsHit, err := s.cachedSessionRows(r.Context(), filter, limit, offset, compact)
	rowsElapsed := time.Since(rowsStarted)
	if err != nil {
		writeError(w, err)
		return
	}
	if !includeSessionEstimates(r) {
		w.Header().Set("Server-Timing", timingMetric("sessions", rowsElapsed, rowsHit))
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	estimateStarted := time.Now()
	estimates, estimateHit, err := s.cachedSessionEstimates(r.Context(), queryKey, filter, items)
	estimateElapsed := time.Since(estimateStarted)
	if err != nil {
		writeError(w, err)
		return
	}
	bySession := make(map[string]pricing.Estimate, len(estimates))
	for _, item := range estimates {
		bySession[item.SessionID] = item.Estimate
	}
	responseItems := make([]sessionResponseItem, 0, len(items))
	for _, item := range items {
		responseItems = append(responseItems, sessionResponseItem{SessionRow: item, Estimate: bySession[item.SessionID]})
	}
	w.Header().Set("Server-Timing", strings.Join([]string{
		timingMetric("sessions", rowsElapsed, rowsHit),
		timingMetric("pricing", estimateElapsed, estimateHit),
	}, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"items": responseItems})
}

func (s *Server) handleSessionEstimates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	filter, limit, offset, compact, err := sessionRequestParameters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rowsStarted := time.Now()
	queryKey, items, rowsHit, err := s.cachedSessionRows(r.Context(), filter, limit, offset, compact)
	rowsElapsed := time.Since(rowsStarted)
	if err != nil {
		writeError(w, err)
		return
	}
	estimateStarted := time.Now()
	estimates, estimateHit, err := s.cachedSessionEstimates(r.Context(), queryKey, filter, items)
	estimateElapsed := time.Since(estimateStarted)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Server-Timing", strings.Join([]string{
		timingMetric("sessions", rowsElapsed, rowsHit),
		timingMetric("pricing", estimateElapsed, estimateHit),
	}, ", "))
	writeJSON(w, http.StatusOK, map[string]any{"items": estimates})
}
