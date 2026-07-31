package otel

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/store"
)

const maxOTLPBody = 16 << 20

type Receiver struct {
	Store *store.Store
	RunID string
	Now   func() time.Time
	mu    sync.Mutex
}

type IngestResult struct {
	DataPoints int `json:"data_points"`
	Events     int `json:"events"`
	Duplicates int `json:"duplicates"`
}

type exportRequest struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

type resourceMetrics struct {
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type resource struct {
	Attributes []attribute `json:"attributes"`
}

type scopeMetrics struct {
	Metrics []metric `json:"metrics"`
}

type metric struct {
	Name      string       `json:"name"`
	Histogram *histogram   `json:"histogram,omitempty"`
	Sum       *sumMetric   `json:"sum,omitempty"`
	Gauge     *gaugeMetric `json:"gauge,omitempty"`
}

type histogram struct {
	AggregationTemporality any              `json:"aggregationTemporality"`
	DataPoints             []histogramPoint `json:"dataPoints"`
}

type sumMetric struct {
	AggregationTemporality any           `json:"aggregationTemporality"`
	DataPoints             []numberPoint `json:"dataPoints"`
}

type gaugeMetric struct {
	DataPoints []numberPoint `json:"dataPoints"`
}

type histogramPoint struct {
	Attributes        []attribute `json:"attributes"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	TimeUnixNano      string      `json:"timeUnixNano"`
	Count             flexNumber  `json:"count"`
	Sum               flexNumber  `json:"sum"`
}

type numberPoint struct {
	Attributes        []attribute `json:"attributes"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	TimeUnixNano      string      `json:"timeUnixNano"`
	AsInt             flexNumber  `json:"asInt"`
	AsDouble          flexNumber  `json:"asDouble"`
}

type attribute struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	StringValue string          `json:"stringValue,omitempty"`
	IntValue    flexNumber      `json:"intValue,omitempty"`
	DoubleValue flexNumber      `json:"doubleValue,omitempty"`
	BoolValue   *bool           `json:"boolValue,omitempty"`
	ArrayValue  json.RawMessage `json:"arrayValue,omitempty"`
}

type flexNumber struct {
	raw   string
	value float64
	set   bool
}

func (n *flexNumber) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	raw = strings.Trim(raw, `"`)
	if raw == "" || raw == "null" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return err
	}
	n.raw = raw
	n.value = value
	n.set = true
	return nil
}

func (n flexNumber) Float64() (float64, error) {
	if !n.set {
		return 0, errors.New("number is unset")
	}
	return n.value, nil
}

func (n flexNumber) Uint64() (uint64, error) {
	if !n.set || n.value < 0 || n.value > float64(^uint64(0)) {
		return 0, errors.New("invalid uint64")
	}
	return uint64(n.value), nil
}

func (n flexNumber) String() string { return n.raw }

type rawPoint struct {
	Metric     string
	Attributes map[string]string
	Start      string
	Timestamp  time.Time
	Value      float64
	ValueSet   bool
	Count      uint64
	Cumulative bool
}

type eventGroup struct {
	Timestamp   time.Time
	Attributes  map[string]string
	Usage       model.TokenUsage
	Fingerprint []string
}

func NewReceiver(st *store.Store) *Receiver {
	return &Receiver{Store: st, RunID: randomID(), Now: time.Now}
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	contentType := strings.ToLower(req.Header.Get("Content-Type"))
	if strings.Contains(contentType, "protobuf") || strings.Contains(contentType, "octet-stream") {
		http.Error(w, `codex-usage v1 接收 OTLP/HTTP JSON；请将 protocol 设为 "json"`, http.StatusUnsupportedMediaType)
		return
	}
	body := http.MaxBytesReader(w, req.Body, maxOTLPBody)
	defer body.Close()
	var reader io.Reader = body
	if strings.EqualFold(req.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "无效 gzip", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		reader = gz
	}
	limited := &io.LimitedReader{R: reader, N: maxOTLPBody + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	var payload exportRequest
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "无效 OTLP JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(w, "OTLP JSON 包含额外内容", http.StatusBadRequest)
		return
	}
	if limited.N <= 0 {
		http.Error(w, "OTLP 解压后请求体过大", http.StatusRequestEntityTooLarge)
		return
	}
	result, err := r.Ingest(req.Context(), payload)
	if err != nil {
		http.Error(w, "OTLP 入库失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"partialSuccess": map[string]any{},
		"codexUsage":     result,
	})
}

func (r *Receiver) Ingest(ctx context.Context, payload exportRequest) (IngestResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Store == nil {
		return IngestResult{}, errors.New("receiver store is nil")
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	now := r.Now().UTC()
	points := flatten(payload)
	result := IngestResult{DataPoints: len(points)}
	if len(points) == 0 {
		return result, nil
	}
	hasParentUsage := false
	for _, point := range points {
		tokenType := pointTokenType(point)
		if tokenType == "" || !point.ValueSet {
			continue
		}
		if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) ||
			point.Value < 0 || point.Value > float64(1<<53-1) ||
			math.Abs(point.Value-math.Round(point.Value)) > 1e-6 {
			return result, fmt.Errorf("无效 Token 累计值 %.4f", point.Value)
		}
		if tokenType == "input" || tokenType == "output" || tokenType == "total" {
			hasParentUsage = true
		}
	}
	if !hasParentUsage {
		_ = r.Store.AddWarning(ctx, "otel_unusable_batch", "",
			"收到 turn.token_usage，但批次不含 input/output/total；未建立覆盖窗口，避免遮蔽 session 历史")
		return result, nil
	}
	groups := map[string]*eventGroup{}
	coverageStart := now
	for _, point := range points {
		tokenType := pointTokenType(point)
		if tokenType == "" || !point.ValueSet {
			continue
		}
		seriesKey := stableSeriesKey(point)
		delta, changed, coveredSince, newSegment, err := r.Store.OTelDelta(ctx, store.OTelSeries{
			Key:       seriesKey,
			StartTime: point.Start,
			Value:     point.Value,
			Count:     point.Count,
			LastSeen:  now,
		}, point.Cumulative)
		if err != nil {
			return result, err
		}
		if parsedStart := nanoTime(point.Start); newSegment && !parsedStart.IsZero() {
			// For a new series or producer start-time change, the current
			// cumulative value covers the producer's start interval.
			coveredSince = parsedStart
		}
		if !coveredSince.IsZero() && coveredSince.Before(coverageStart) {
			coverageStart = coveredSince
		}
		if !changed || delta == 0 {
			result.Duplicates++
			continue
		}
		if delta < 0 || delta > float64(^uint64(0)>>1) {
			return result, fmt.Errorf("无效 Token delta %.4f", delta)
		}
		value := int64(math.Round(delta))
		timeBucket := point.Timestamp.Truncate(time.Second)
		groupKey := strconv.FormatInt(timeBucket.Unix(), 10) + "\x00" + commonAttributeKey(point.Attributes)
		group := groups[groupKey]
		if group == nil {
			group = &eventGroup{
				Timestamp:  point.Timestamp,
				Attributes: point.Attributes,
			}
			groups[groupKey] = group
		} else if point.Timestamp.After(group.Timestamp) {
			group.Timestamp = point.Timestamp
		}
		switch tokenType {
		case "input":
			group.Usage.Input += value
		case "cached_input":
			group.Usage.CachedInput += value
		case "cache_write_input":
			group.Usage.CacheWriteInput += value
		case "output":
			group.Usage.Output += value
		case "reasoning_output":
			group.Usage.ReasoningOutput += value
		case "total":
			group.Usage.Total += value
		}
		group.Fingerprint = append(group.Fingerprint,
			fmt.Sprintf("%s=%g@%s", seriesKey, point.Value, point.Start))
	}
	if err := r.Store.TouchCoverageInterval(ctx, r.RunID, coverageStart, now); err != nil {
		return result, err
	}
	if len(groups) == 0 {
		return result, nil
	}
	for _, group := range groups {
		sort.Strings(group.Fingerprint)
		attrs := group.Attributes
		source := firstAttr(attrs, "originator", "session_source", "source", "service.name")
		sessionID := firstAttr(attrs, "session_id", "thread_id", "conversation.id", "gen_ai.conversation.id")
		turnID := firstAttr(attrs, "turn_id", "gen_ai.operation.id")
		modelName := firstAttr(attrs, "model", "gen_ai.request.model", "gen_ai.response.model")
		project := firstAttr(attrs, "cwd", "project_path")
		title := firstAttr(attrs, "thread_title")
		agent := model.ClassifyAgent(source, firstAttr(attrs, "agent_type"))
		if explicit := firstAttr(attrs, "agent_type"); explicit != "" {
			agent = explicit
		}
		usage := group.Usage.Compatible()
		if usage.IsZero() {
			continue
		}
		timestamp := group.Timestamp
		confidence := model.ConfidenceExact
		if timestamp.IsZero() {
			timestamp = now
			confidence = model.ConfidenceGapFallback
		}
		idMaterial := strings.Join(group.Fingerprint, "\n") + "\x00" + timestamp.Format(time.RFC3339Nano)
		event := model.UsageEvent{
			ID:          "otel:" + hash(idMaterial),
			Timestamp:   timestamp,
			ObservedAt:  now,
			MachineID:   r.Store.Machine().ID,
			SessionID:   sessionID,
			TurnID:      turnID,
			Model:       modelName,
			Source:      source,
			AgentType:   agent,
			ProjectPath: project,
			ThreadTitle: title,
			Usage:       usage,
			Provenance:  model.ProvenanceOTel,
			Confidence:  confidence,
		}
		inserted, err := r.Store.InsertEvent(ctx, event, "")
		if err != nil {
			return result, err
		}
		if inserted {
			result.Events++
		} else {
			result.Duplicates++
		}
	}
	return result, nil
}

func flatten(payload exportRequest) []rawPoint {
	var out []rawPoint
	for _, rm := range payload.ResourceMetrics {
		resourceAttrs := attrsMap(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, metricValue := range sm.Metrics {
				if !isTokenUsageMetric(metricValue.Name) {
					continue
				}
				if metricValue.Histogram != nil {
					cumulative := isCumulative(metricValue.Histogram.AggregationTemporality)
					for _, point := range metricValue.Histogram.DataPoints {
						attrs := mergeAttrs(resourceAttrs, attrsMap(point.Attributes))
						value, _ := point.Sum.Float64()
						count, _ := point.Count.Uint64()
						out = append(out, rawPoint{
							Metric: metricValue.Name, Attributes: attrs, Start: point.StartTimeUnixNano,
							Timestamp: nanoTime(point.TimeUnixNano), Value: value, Count: count,
							ValueSet: point.Sum.set, Cumulative: cumulative,
						})
					}
				}
				if metricValue.Sum != nil {
					cumulative := isCumulative(metricValue.Sum.AggregationTemporality)
					for _, point := range metricValue.Sum.DataPoints {
						out = append(out, rawPoint{
							Metric:     metricValue.Name,
							Attributes: mergeAttrs(resourceAttrs, attrsMap(point.Attributes)),
							Start:      point.StartTimeUnixNano, Timestamp: nanoTime(point.TimeUnixNano),
							Value: point.number(), ValueSet: point.hasNumber(), Cumulative: cumulative,
						})
					}
				}
				if metricValue.Gauge != nil {
					for _, point := range metricValue.Gauge.DataPoints {
						out = append(out, rawPoint{
							Metric:     metricValue.Name,
							Attributes: mergeAttrs(resourceAttrs, attrsMap(point.Attributes)),
							Start:      point.StartTimeUnixNano, Timestamp: nanoTime(point.TimeUnixNano),
							Value: point.number(), ValueSet: point.hasNumber(), Cumulative: false,
						})
					}
				}
			}
		}
	}
	return out
}

func (p numberPoint) number() float64 {
	if p.AsInt.set {
		value, _ := p.AsInt.Float64()
		return value
	}
	value, _ := p.AsDouble.Float64()
	return value
}

func (p numberPoint) hasNumber() bool {
	return p.AsInt.set || p.AsDouble.set
}

func attrsMap(values []attribute) map[string]string {
	out := make(map[string]string, len(values))
	for _, item := range values {
		out[item.Key] = item.Value.text()
	}
	return out
}

func (v anyValue) text() string {
	if v.StringValue != "" {
		return v.StringValue
	}
	if v.IntValue.set {
		return v.IntValue.String()
	}
	if v.DoubleValue.set {
		return v.DoubleValue.String()
	}
	if v.BoolValue != nil {
		return strconv.FormatBool(*v.BoolValue)
	}
	return ""
}

func mergeAttrs(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func firstAttr(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func normalizeTokenType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, ".", "_")
	switch value {
	case "input", "input_tokens", "prompt", "prompt_tokens":
		return "input"
	case "cached_input", "cached_input_tokens", "cache_read_input", "cache_read":
		return "cached_input"
	case "cache_write_input", "cache_write_input_tokens", "cache_write":
		return "cache_write_input"
	case "output", "output_tokens", "completion", "completion_tokens":
		return "output"
	case "reasoning_output", "reasoning_output_tokens", "reasoning":
		return "reasoning_output"
	case "total", "total_tokens":
		return "total"
	default:
		return ""
	}
}

func pointTokenType(point rawPoint) string {
	value := normalizeTokenType(point.Attributes["token_type"])
	if value == "" {
		value = normalizeTokenType(point.Attributes["type"])
	}
	return value
}

func isTokenUsageMetric(name string) bool {
	name = strings.ToLower(name)
	return name == "turn.token_usage" || strings.HasSuffix(name, ".turn.token_usage")
}

func isCumulative(value any) bool {
	switch typed := value.(type) {
	case json.Number:
		n, _ := typed.Int64()
		return n == 2
	case float64:
		return int64(typed) == 2
	case string:
		upper := strings.ToUpper(typed)
		return typed == "2" || strings.Contains(upper, "CUMULATIVE")
	default:
		return strings.Contains(strings.ToUpper(fmt.Sprint(value)), "CUMULATIVE")
	}
}

func nanoTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	nanos, err := strconv.ParseInt(value, 10, 64)
	if err != nil || nanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

func stableSeriesKey(point rawPoint) string {
	keys := make([]string, 0, len(point.Attributes))
	for key := range point.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(point.Metric)
	for _, key := range keys {
		b.WriteByte('\x00')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(point.Attributes[key])
	}
	return hash(b.String())
}

func commonAttributeKey(attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		if key == "token_type" || key == "type" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(attrs[key])
		b.WriteByte('\x00')
	}
	return b.String()
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}
