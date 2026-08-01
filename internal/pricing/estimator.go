package pricing

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zJay26/codex-usage/internal/model"
)

type UnpricedReason struct {
	Kind   string `json:"kind"`
	Model  string `json:"model,omitempty"`
	Tokens int64  `json:"tokens"`
	Detail string `json:"detail"`
}

type Estimate struct {
	USD                string           `json:"usd"`
	RegularInputUSD    string           `json:"regular_input_usd"`
	CachedInputUSD     string           `json:"cached_input_usd"`
	CacheWriteInputUSD string           `json:"cache_write_input_usd"`
	OutputUSD          string           `json:"output_usd"`
	PricedTokens       int64            `json:"priced_tokens"`
	UnpricedTokens     int64            `json:"unpriced_tokens"`
	CoverageRatio      float64          `json:"coverage_ratio"`
	Reasons            []UnpricedReason `json:"reasons,omitempty"`
}

type ReportPoint struct {
	Date     string           `json:"date"`
	Time     time.Time        `json:"time"`
	Usage    model.TokenUsage `json:"usage"`
	Estimate Estimate         `json:"estimate"`
}

type ModelEstimate struct {
	Key      string           `json:"key"`
	Usage    model.TokenUsage `json:"usage"`
	Estimate Estimate         `json:"estimate"`
}

type Report struct {
	Basis       string          `json:"basis"`
	Currency    string          `json:"currency"`
	CatalogAsOf string          `json:"catalog_as_of"`
	Bucket      string          `json:"bucket"`
	Summary     Estimate        `json:"summary"`
	Points      []ReportPoint   `json:"points"`
	Models      []ModelEstimate `json:"models"`
}

type Builder struct {
	overrides map[string]Override
	summary   aggregate
	points    map[int64]*aggregate
	models    map[string]*aggregate
}

type aggregate struct {
	usage          model.TokenUsage
	regularNano    int64
	cachedNano     int64
	cacheWriteNano int64
	outputNano     int64
	totalNano      int64
	pricedTokens   int64
	unpricedTokens int64
	totalTokens    int64
	reasons        map[string]UnpricedReason
}

type evaluatedEvent struct {
	usage          model.TokenUsage
	regularNano    int64
	cachedNano     int64
	cacheWriteNano int64
	outputNano     int64
	pricedTokens   int64
	unpricedTokens int64
	reasons        []UnpricedReason
}

func NewBuilder(overrides map[string]Override) (*Builder, error) {
	normalized, err := NormalizeOverrides(overrides)
	if err != nil {
		return nil, err
	}
	return &Builder{
		overrides: normalized,
		points:    map[int64]*aggregate{},
		models:    map[string]*aggregate{},
	}, nil
}

func (b *Builder) Add(event model.UsageEvent) error {
	evaluated, err := evaluateEvent(event, b.overrides)
	if err != nil {
		return err
	}
	if err := b.summary.add(evaluated); err != nil {
		return err
	}
	modelKey := strings.TrimSpace(event.Model)
	if modelKey == "" {
		modelKey = "未知模型"
	}
	modelAggregate := b.models[modelKey]
	if modelAggregate == nil {
		modelAggregate = &aggregate{}
		b.models[modelKey] = modelAggregate
	}
	if err := modelAggregate.add(evaluated); err != nil {
		return err
	}
	if !event.Timestamp.IsZero() {
		local := event.Timestamp.In(time.Local)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
		pointAggregate := b.points[start.Unix()]
		if pointAggregate == nil {
			pointAggregate = &aggregate{}
			b.points[start.Unix()] = pointAggregate
		}
		if err := pointAggregate.add(evaluated); err != nil {
			return err
		}
	}
	return nil
}

func (b *Builder) Report() Report {
	pointKeys := make([]int64, 0, len(b.points))
	for key := range b.points {
		pointKeys = append(pointKeys, key)
	}
	sort.Slice(pointKeys, func(i, j int) bool { return pointKeys[i] < pointKeys[j] })
	points := make([]ReportPoint, 0, len(pointKeys))
	for _, key := range pointKeys {
		local := time.Unix(key, 0).In(time.Local)
		item := b.points[key]
		points = append(points, ReportPoint{
			Date: local.Format("2006-01-02"), Time: time.Unix(key, 0).UTC(),
			Usage: item.usage, Estimate: item.estimate(),
		})
	}
	models := make([]ModelEstimate, 0, len(b.models))
	for key, item := range b.models {
		models = append(models, ModelEstimate{Key: key, Usage: item.usage, Estimate: item.estimate()})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Usage.Total == models[j].Usage.Total {
			return models[i].Key < models[j].Key
		}
		return models[i].Usage.Total > models[j].Usage.Total
	})
	return Report{
		Basis: Basis, Currency: Currency, CatalogAsOf: CatalogAsOf, Bucket: "day",
		Summary: b.summary.estimate(), Points: points, Models: models,
	}
}

// FillDaily inserts quiet zero-value points for local natural days. The until
// bound remains exclusive when it lands exactly at local midnight.
func FillDaily(report Report, since, until, now time.Time) (Report, error) {
	var start time.Time
	if !since.IsZero() {
		start = localDay(since)
	} else if len(report.Points) > 0 {
		start = localDay(report.Points[0].Time)
	}
	if start.IsZero() {
		return report, nil
	}
	var end time.Time
	if until.IsZero() {
		end = localDay(now).AddDate(0, 0, 1)
	} else {
		end = localDay(until)
		localUntil := until.In(time.Local)
		if !localUntil.Equal(end) {
			end = end.AddDate(0, 0, 1)
		}
	}
	if !end.After(start) {
		report.Points = []ReportPoint{}
		return report, nil
	}
	const maxPoints = 5000
	days := 0
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		days++
		if days > maxPoints {
			return Report{}, fmt.Errorf("每日费用范围超过 %d 天", maxPoints)
		}
	}
	existing := make(map[string]ReportPoint, len(report.Points))
	for _, point := range report.Points {
		existing[point.Date] = point
	}
	points := make([]ReportPoint, 0, days)
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		if point, ok := existing[date]; ok {
			points = append(points, point)
			continue
		}
		points = append(points, ReportPoint{
			Date:     date,
			Time:     day.UTC(),
			Estimate: aggregate{}.estimate(),
		})
	}
	report.Points = points
	return report, nil
}

func localDay(value time.Time) time.Time {
	local := value.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
}

func evaluateEvent(event model.UsageEvent, overrides map[string]Override) (evaluatedEvent, error) {
	usage := event.Usage
	out := evaluatedEvent{usage: usage}
	modelName := strings.TrimSpace(event.Model)
	if !usage.NonNegative() {
		out.unpricedTokens = maxInt64(usage.Total, 0)
		out.reasons = append(out.reasons, UnpricedReason{
			Kind: "invalid_token_categories", Model: modelName, Tokens: out.unpricedTokens,
			Detail: "Token 分类关系不一致，未参与费用估算",
		})
		return out, nil
	}
	categoryTotal, addErr := checkedAdd(usage.Input, usage.Output)
	if addErr != nil {
		return out, fmt.Errorf("Input 与 Output Token 合计溢出: %w", addErr)
	}
	if usage.Total == 0 && categoryTotal > 0 {
		usage.Total = categoryTotal
		out.usage.Total = categoryTotal
	}
	total := usage.Total
	if usage.CachedInput > usage.Input || usage.CacheWriteInput > usage.Input-usage.CachedInput ||
		usage.ReasoningOutput > usage.Output {
		out.unpricedTokens = maxInt64(total, 0)
		out.reasons = append(out.reasons, UnpricedReason{
			Kind: "invalid_token_categories", Model: modelName, Tokens: out.unpricedTokens,
			Detail: "Token 分类关系不一致，未参与费用估算",
		})
		return out, nil
	}
	if usage.Input == 0 && usage.Output == 0 && total > 0 {
		out.unpricedTokens = total
		out.reasons = append(out.reasons, UnpricedReason{
			Kind: "missing_token_categories", Model: modelName, Tokens: total,
			Detail: "只有累计总量，缺少 Input 与 Output 拆分",
		})
		return out, nil
	}
	if usage.Total > 0 && usage.Total != categoryTotal {
		out.unpricedTokens = total
		out.reasons = append(out.reasons, UnpricedReason{
			Kind: "invalid_token_categories", Model: modelName, Tokens: total,
			Detail: "Token 分类关系不一致，未参与费用估算",
		})
		return out, nil
	}
	rate, found, err := Resolve(modelName, overrides)
	if err != nil {
		return out, err
	}
	if !found {
		out.unpricedTokens = total
		out.reasons = append(out.reasons, UnpricedReason{
			Kind: "unknown_model", Model: modelName, Tokens: total,
			Detail: "没有公开 API 单价或本机定价覆写",
		})
		return out, nil
	}
	longInputNumerator, longInputDenominator := int64(1), int64(1)
	longOutputNumerator, longOutputDenominator := int64(1), int64(1)
	if rate.LongContextThreshold > 0 && usage.Input > rate.LongContextThreshold {
		if event.Confidence != model.ConfidenceExact {
			out.unpricedTokens = total
			out.reasons = append(out.reasons, UnpricedReason{
				Kind: "long_context_uncertain", Model: modelName, Tokens: total,
				Detail: "超过长上下文阈值，但该事件不是精确请求增量",
			})
			return out, nil
		}
		longInputNumerator, longInputDenominator = rate.LongInputNumerator, rate.LongInputDenominator
		longOutputNumerator, longOutputDenominator = rate.LongOutputNumerator, rate.LongOutputDenominator
	}
	regularTokens := usage.Input - usage.CachedInput - usage.CacheWriteInput
	regularCost, err := tokenCost(regularTokens, rate.InputNanoPerToken, longInputNumerator, longInputDenominator)
	if err != nil {
		return out, err
	}
	cachedCost, err := tokenCost(usage.CachedInput, rate.CachedNanoPerToken, longInputNumerator, longInputDenominator)
	if err != nil {
		return out, err
	}
	outputCost, err := tokenCost(usage.Output, rate.OutputNanoPerToken, longOutputNumerator, longOutputDenominator)
	if err != nil {
		return out, err
	}
	out.regularNano = regularCost
	out.cachedNano = cachedCost
	out.outputNano = outputCost
	out.pricedTokens = regularTokens + usage.CachedInput + usage.Output
	if usage.CacheWriteInput > 0 {
		if rate.CacheWriteNanoPerToken == nil {
			out.unpricedTokens += usage.CacheWriteInput
			out.reasons = append(out.reasons, UnpricedReason{
				Kind: "cache_write_rate_missing", Model: modelName, Tokens: usage.CacheWriteInput,
				Detail: "官方模型页未公开 Cache Write 单价",
			})
		} else {
			writeCost, writeErr := tokenCost(usage.CacheWriteInput, *rate.CacheWriteNanoPerToken, longInputNumerator, longInputDenominator)
			if writeErr != nil {
				return out, writeErr
			}
			out.cacheWriteNano = writeCost
			out.pricedTokens += usage.CacheWriteInput
		}
	}
	return out, nil
}

func (a *aggregate) add(event evaluatedEvent) error {
	var err error
	if a.usage, err = addUsage(a.usage, event.usage); err != nil {
		return err
	}
	if a.regularNano, err = checkedAdd(a.regularNano, event.regularNano); err != nil {
		return err
	}
	if a.cachedNano, err = checkedAdd(a.cachedNano, event.cachedNano); err != nil {
		return err
	}
	if a.cacheWriteNano, err = checkedAdd(a.cacheWriteNano, event.cacheWriteNano); err != nil {
		return err
	}
	if a.outputNano, err = checkedAdd(a.outputNano, event.outputNano); err != nil {
		return err
	}
	eventNano, err := checkedAdd(event.regularNano, event.cachedNano)
	if err != nil {
		return err
	}
	eventNano, err = checkedAdd(eventNano, event.cacheWriteNano)
	if err != nil {
		return err
	}
	eventNano, err = checkedAdd(eventNano, event.outputNano)
	if err != nil {
		return err
	}
	if a.totalNano, err = checkedAdd(a.totalNano, eventNano); err != nil {
		return err
	}
	if a.pricedTokens, err = checkedAdd(a.pricedTokens, event.pricedTokens); err != nil {
		return err
	}
	if a.unpricedTokens, err = checkedAdd(a.unpricedTokens, event.unpricedTokens); err != nil {
		return err
	}
	eventTokens, err := checkedAdd(event.pricedTokens, event.unpricedTokens)
	if err != nil {
		return err
	}
	if a.totalTokens, err = checkedAdd(a.totalTokens, eventTokens); err != nil {
		return err
	}
	if len(event.reasons) > 0 && a.reasons == nil {
		a.reasons = map[string]UnpricedReason{}
	}
	for _, reason := range event.reasons {
		key := reason.Kind + "\x00" + reason.Model + "\x00" + reason.Detail
		current := a.reasons[key]
		current.Kind, current.Model, current.Detail = reason.Kind, reason.Model, reason.Detail
		current.Tokens, err = checkedAdd(current.Tokens, reason.Tokens)
		if err != nil {
			return err
		}
		a.reasons[key] = current
	}
	return nil
}

func (a aggregate) estimate() Estimate {
	coverage := 0.0
	if a.totalTokens > 0 {
		coverage = float64(a.pricedTokens) / float64(a.totalTokens)
	}
	reasons := make([]UnpricedReason, 0, len(a.reasons))
	for _, reason := range a.reasons {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].Tokens == reasons[j].Tokens {
			if reasons[i].Kind == reasons[j].Kind {
				return reasons[i].Model < reasons[j].Model
			}
			return reasons[i].Kind < reasons[j].Kind
		}
		return reasons[i].Tokens > reasons[j].Tokens
	})
	return Estimate{
		USD: formatNanoUSD(a.totalNano), RegularInputUSD: formatNanoUSD(a.regularNano),
		CachedInputUSD: formatNanoUSD(a.cachedNano), CacheWriteInputUSD: formatNanoUSD(a.cacheWriteNano),
		OutputUSD: formatNanoUSD(a.outputNano), PricedTokens: a.pricedTokens,
		UnpricedTokens: a.unpricedTokens, CoverageRatio: coverage, Reasons: reasons,
	}
}

func tokenCost(tokens, nanoPerToken, numerator, denominator int64) (int64, error) {
	if tokens < 0 || nanoPerToken < 0 || numerator <= 0 || denominator <= 0 {
		return 0, fmt.Errorf("无效费用计算参数")
	}
	if tokens != 0 && nanoPerToken > math.MaxInt64/tokens {
		return 0, fmt.Errorf("费用计算溢出")
	}
	base := tokens * nanoPerToken
	if numerator != 1 {
		if base > math.MaxInt64/numerator {
			return 0, fmt.Errorf("费用倍率计算溢出")
		}
		base *= numerator
	}
	if denominator > 1 {
		quotient, remainder := base/denominator, base%denominator
		if remainder >= denominator/2+denominator%2 {
			quotient++
		}
		base = quotient
	}
	return base, nil
}

func addUsage(left, right model.TokenUsage) (model.TokenUsage, error) {
	values := []*int64{&left.Input, &left.CachedInput, &left.CacheWriteInput, &left.Output, &left.ReasoningOutput, &left.Total}
	additions := []int64{right.Input, right.CachedInput, right.CacheWriteInput, right.Output, right.ReasoningOutput, right.Total}
	for index := range values {
		value, err := checkedAdd(*values[index], additions[index])
		if err != nil {
			return model.TokenUsage{}, err
		}
		*values[index] = value
	}
	return left, nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("费用或 Token 累计溢出")
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, fmt.Errorf("费用或 Token 累计溢出")
	}
	return left + right, nil
}

func formatNanoUSD(value int64) string {
	if value < 0 {
		return "-" + formatNanoUSD(-value)
	}
	return fmt.Sprintf("%d.%09d", value/1_000_000_000, value%1_000_000_000)
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
