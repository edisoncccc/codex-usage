package pricing

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	CatalogAsOf = "2026-07-31"
	Currency    = "USD"
	Basis       = "current_standard_api_text_token_prices"
)

type Override struct {
	AliasOf                      string `json:"alias_of,omitempty"`
	InputUSDPerMillion           string `json:"input_usd_per_million,omitempty"`
	CachedInputUSDPerMillion     string `json:"cached_input_usd_per_million,omitempty"`
	CacheWriteInputUSDPerMillion string `json:"cache_write_input_usd_per_million,omitempty"`
	OutputUSDPerMillion          string `json:"output_usd_per_million,omitempty"`
}

type CatalogEntry struct {
	Model                        string   `json:"model"`
	DisplayName                  string   `json:"display_name"`
	Aliases                      []string `json:"aliases,omitempty"`
	SnapshotPatterns             []string `json:"snapshot_patterns,omitempty"`
	InputUSDPerMillion           string   `json:"input_usd_per_million"`
	CachedInputUSDPerMillion     string   `json:"cached_input_usd_per_million"`
	CacheWriteInputUSDPerMillion string   `json:"cache_write_input_usd_per_million,omitempty"`
	OutputUSDPerMillion          string   `json:"output_usd_per_million"`
	LongContextThreshold         int64    `json:"long_context_threshold,omitempty"`
	LongContextInputMultiplier   string   `json:"long_context_input_multiplier,omitempty"`
	LongContextOutputMultiplier  string   `json:"long_context_output_multiplier,omitempty"`
	Source                       string   `json:"source"`
}

type ResolvedRate struct {
	RequestedModel         string
	CanonicalModel         string
	Source                 string
	Custom                 bool
	InputNanoPerToken      int64
	CachedNanoPerToken     int64
	CacheWriteNanoPerToken *int64
	OutputNanoPerToken     int64
	LongContextThreshold   int64
	LongInputNumerator     int64
	LongInputDenominator   int64
	LongOutputNumerator    int64
	LongOutputDenominator  int64
}

var builtInCatalog = []CatalogEntry{
	{
		Model: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", Aliases: []string{"gpt-5.6"},
		SnapshotPatterns:   []string{"gpt-5.6-sol-YYYY-MM-DD", "gpt-5.6-YYYY-MM-DD"},
		InputUSDPerMillion: "5.00", CachedInputUSDPerMillion: "0.50",
		CacheWriteInputUSDPerMillion: "6.25", OutputUSDPerMillion: "30.00",
		LongContextThreshold: 272000, LongContextInputMultiplier: "2", LongContextOutputMultiplier: "1.5",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.6-sol",
	},
	{
		Model: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra",
		SnapshotPatterns:   []string{"gpt-5.6-terra-YYYY-MM-DD"},
		InputUSDPerMillion: "2.00", CachedInputUSDPerMillion: "0.20",
		CacheWriteInputUSDPerMillion: "2.50", OutputUSDPerMillion: "12.00",
		LongContextThreshold: 272000, LongContextInputMultiplier: "2", LongContextOutputMultiplier: "1.5",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.6-terra",
	},
	{
		Model: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna",
		SnapshotPatterns:   []string{"gpt-5.6-luna-YYYY-MM-DD"},
		InputUSDPerMillion: "0.20", CachedInputUSDPerMillion: "0.02",
		CacheWriteInputUSDPerMillion: "0.25", OutputUSDPerMillion: "1.20",
		LongContextThreshold: 272000, LongContextInputMultiplier: "2", LongContextOutputMultiplier: "1.5",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.6-luna",
	},
	{
		Model: "gpt-5.5", DisplayName: "GPT-5.5",
		SnapshotPatterns:   []string{"gpt-5.5-YYYY-MM-DD"},
		InputUSDPerMillion: "5.00", CachedInputUSDPerMillion: "0.50", OutputUSDPerMillion: "30.00",
		LongContextThreshold: 272000, LongContextInputMultiplier: "2", LongContextOutputMultiplier: "1.5",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.5",
	},
	{
		Model: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini",
		SnapshotPatterns:   []string{"gpt-5.4-mini-YYYY-MM-DD"},
		InputUSDPerMillion: "0.75", CachedInputUSDPerMillion: "0.075", OutputUSDPerMillion: "4.50",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.4-mini",
	},
	{
		Model: "gpt-5.4", DisplayName: "GPT-5.4",
		SnapshotPatterns:   []string{"gpt-5.4-YYYY-MM-DD"},
		InputUSDPerMillion: "2.50", CachedInputUSDPerMillion: "0.25", OutputUSDPerMillion: "15.00",
		LongContextThreshold: 272000, LongContextInputMultiplier: "2", LongContextOutputMultiplier: "1.5",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.4",
	},
	{
		Model: "gpt-5.3-codex", DisplayName: "GPT-5.3-Codex",
		SnapshotPatterns:   []string{"gpt-5.3-codex-YYYY-MM-DD"},
		InputUSDPerMillion: "1.75", CachedInputUSDPerMillion: "0.175", OutputUSDPerMillion: "14.00",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.3-codex",
	},
	{
		Model: "gpt-5.2-codex", DisplayName: "GPT-5.2-Codex",
		SnapshotPatterns:   []string{"gpt-5.2-codex-YYYY-MM-DD"},
		InputUSDPerMillion: "1.75", CachedInputUSDPerMillion: "0.175", OutputUSDPerMillion: "14.00",
		Source: "https://developers.openai.com/api/docs/models/gpt-5.2-codex",
	},
}

func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(builtInCatalog))
	copy(out, builtInCatalog)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
		out[i].SnapshotPatterns = append([]string(nil), out[i].SnapshotPatterns...)
	}
	return out
}

func NormalizeOverrides(input map[string]Override) (map[string]Override, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make(map[string]Override, len(input))
	for rawModel, raw := range input {
		model := strings.ToLower(strings.TrimSpace(rawModel))
		if model == "" || len(model) > 200 || strings.ContainsAny(model, "\x00\r\n") {
			return nil, fmt.Errorf("无效定价覆写模型 %q", rawModel)
		}
		if _, exists := out[model]; exists {
			return nil, fmt.Errorf("重复定价覆写模型 %q", model)
		}
		raw.AliasOf = strings.ToLower(strings.TrimSpace(raw.AliasOf))
		raw.InputUSDPerMillion = strings.TrimSpace(raw.InputUSDPerMillion)
		raw.CachedInputUSDPerMillion = strings.TrimSpace(raw.CachedInputUSDPerMillion)
		raw.CacheWriteInputUSDPerMillion = strings.TrimSpace(raw.CacheWriteInputUSDPerMillion)
		raw.OutputUSDPerMillion = strings.TrimSpace(raw.OutputUSDPerMillion)
		if _, ok := resolveBuiltIn(model); ok {
			return nil, fmt.Errorf("内置官方模型 %q 不能被本机覆写", model)
		}
		if raw.AliasOf != "" {
			if raw.InputUSDPerMillion != "" || raw.CachedInputUSDPerMillion != "" ||
				raw.CacheWriteInputUSDPerMillion != "" || raw.OutputUSDPerMillion != "" {
				return nil, fmt.Errorf("模型 %q 的 alias_of 与自定义单价不能同时设置", model)
			}
			entry, ok := resolveBuiltIn(raw.AliasOf)
			if !ok {
				return nil, fmt.Errorf("模型 %q 的 alias_of 必须指向内置公开模型", model)
			}
			raw.AliasOf = entry.Model
		} else {
			if raw.InputUSDPerMillion == "" || raw.CachedInputUSDPerMillion == "" ||
				raw.CacheWriteInputUSDPerMillion == "" || raw.OutputUSDPerMillion == "" {
				return nil, fmt.Errorf("模型 %q 必须填写 input、cached input、cache write 和 output 单价", model)
			}
			for label, value := range map[string]string{
				"input": raw.InputUSDPerMillion, "cached input": raw.CachedInputUSDPerMillion,
				"cache write": raw.CacheWriteInputUSDPerMillion, "output": raw.OutputUSDPerMillion,
			} {
				if _, err := parseUSDPerMillion(value); err != nil {
					return nil, fmt.Errorf("模型 %q 的 %s 单价: %w", model, label, err)
				}
			}
		}
		out[model] = raw
	}
	return out, nil
}

func Resolve(model string, overrides map[string]Override) (ResolvedRate, bool, error) {
	requested := strings.ToLower(strings.TrimSpace(model))
	if override, ok := overrides[requested]; ok {
		if override.AliasOf != "" {
			entry, found := resolveBuiltIn(override.AliasOf)
			if !found {
				return ResolvedRate{}, false, fmt.Errorf("alias_of %q 不再存在", override.AliasOf)
			}
			rate, err := resolvedFromEntry(requested, entry)
			rate.Custom = true
			return rate, true, err
		}
		rate, err := resolvedFromOverride(requested, override)
		return rate, true, err
	}
	entry, ok := resolveBuiltIn(requested)
	if !ok {
		return ResolvedRate{}, false, nil
	}
	rate, err := resolvedFromEntry(requested, entry)
	return rate, true, err
}

func resolveBuiltIn(model string) (CatalogEntry, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, entry := range builtInCatalog {
		if modelMatches(model, entry.Model) {
			return entry, true
		}
		for _, alias := range entry.Aliases {
			if modelMatches(model, alias) {
				return entry, true
			}
		}
	}
	return CatalogEntry{}, false
}

func modelMatches(model, base string) bool {
	if model == base {
		return true
	}
	if !strings.HasPrefix(model, base+"-") {
		return false
	}
	suffix := strings.TrimPrefix(model, base+"-")
	if len(suffix) != len("2006-01-02") || suffix[4] != '-' || suffix[7] != '-' {
		return false
	}
	_, err := time.Parse("2006-01-02", suffix)
	return err == nil
}

func resolvedFromEntry(requested string, entry CatalogEntry) (ResolvedRate, error) {
	input, err := parseUSDPerMillion(entry.InputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	cached, err := parseUSDPerMillion(entry.CachedInputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	output, err := parseUSDPerMillion(entry.OutputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	var cacheWrite *int64
	if entry.CacheWriteInputUSDPerMillion != "" {
		value, parseErr := parseUSDPerMillion(entry.CacheWriteInputUSDPerMillion)
		if parseErr != nil {
			return ResolvedRate{}, parseErr
		}
		cacheWrite = &value
	}
	rate := ResolvedRate{
		RequestedModel: requested, CanonicalModel: entry.Model, Source: entry.Source,
		InputNanoPerToken: input, CachedNanoPerToken: cached,
		CacheWriteNanoPerToken: cacheWrite, OutputNanoPerToken: output,
		LongContextThreshold: entry.LongContextThreshold,
		LongInputNumerator:   1, LongInputDenominator: 1,
		LongOutputNumerator: 1, LongOutputDenominator: 1,
	}
	if entry.LongContextInputMultiplier == "2" {
		rate.LongInputNumerator = 2
	}
	if entry.LongContextOutputMultiplier == "1.5" {
		rate.LongOutputNumerator = 3
		rate.LongOutputDenominator = 2
	}
	return rate, nil
}

func resolvedFromOverride(model string, override Override) (ResolvedRate, error) {
	input, err := parseUSDPerMillion(override.InputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	cached, err := parseUSDPerMillion(override.CachedInputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	output, err := parseUSDPerMillion(override.OutputUSDPerMillion)
	if err != nil {
		return ResolvedRate{}, err
	}
	var cacheWrite *int64
	if override.CacheWriteInputUSDPerMillion != "" {
		value, parseErr := parseUSDPerMillion(override.CacheWriteInputUSDPerMillion)
		if parseErr != nil {
			return ResolvedRate{}, parseErr
		}
		cacheWrite = &value
	}
	return ResolvedRate{
		RequestedModel: model, CanonicalModel: model, Source: "local_override", Custom: true,
		InputNanoPerToken: input, CachedNanoPerToken: cached,
		CacheWriteNanoPerToken: cacheWrite, OutputNanoPerToken: output,
		LongInputNumerator: 1, LongInputDenominator: 1,
		LongOutputNumerator: 1, LongOutputDenominator: 1,
	}, nil
}

// parseUSDPerMillion converts a USD/1M-token decimal with at most three
// fractional digits to nano-USD/token. For example, 0.075 becomes 75.
func parseUSDPerMillion(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "eE") {
		return 0, fmt.Errorf("必须是非负十进制字符串")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("必须是非负十进制字符串")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > 1_000_000 {
		return 0, fmt.Errorf("数值超出范围")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > 3 {
			return 0, fmt.Errorf("最多保留三位小数")
		}
		for _, char := range fraction {
			if char < '0' || char > '9' {
				return 0, fmt.Errorf("必须是非负十进制字符串")
			}
		}
	}
	fraction += strings.Repeat("0", 3-len(fraction))
	frac := int64(0)
	if fraction != "" {
		frac, _ = strconv.ParseInt(fraction, 10, 64)
	}
	return whole*1000 + frac, nil
}
