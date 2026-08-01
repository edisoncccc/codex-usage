package pricing

import "testing"

func TestResolveBuiltInAndVersionedSnapshot(t *testing.T) {
	rate, ok, err := Resolve("gpt-5.6-sol-2026-07-15", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a versioned built-in model to resolve")
	}
	if rate.CanonicalModel != "gpt-5.6-sol" || rate.InputNanoPerToken != 5000 || rate.CachedNanoPerToken != 500 || rate.OutputNanoPerToken != 30000 {
		t.Fatalf("unexpected rate: %#v", rate)
	}
	if rate.CacheWriteNanoPerToken == nil || *rate.CacheWriteNanoPerToken != 6250 {
		t.Fatalf("unexpected cache-write rate: %#v", rate.CacheWriteNanoPerToken)
	}
}

func TestEveryCatalogModelAndSnapshotPatternResolvesExactly(t *testing.T) {
	for _, entry := range Catalog() {
		for _, name := range []string{entry.Model, entry.Model + "-2026-07-31"} {
			rate, ok, err := Resolve(name, nil)
			if err != nil || !ok || rate.CanonicalModel != entry.Model {
				t.Fatalf("%q did not resolve to %q: rate=%#v ok=%v err=%v", name, entry.Model, rate, ok, err)
			}
		}
		if len(entry.SnapshotPatterns) == 0 {
			t.Fatalf("catalog entry %q does not publish its snapshot pattern", entry.Model)
		}
	}
	for _, name := range []string{"gpt-5.6", "gpt-5.6-2026-07-31"} {
		rate, ok, err := Resolve(name, nil)
		if err != nil || !ok || rate.CanonicalModel != "gpt-5.6-sol" {
			t.Fatalf("public alias %q did not resolve: rate=%#v ok=%v err=%v", name, rate, ok, err)
		}
	}
}

func TestGPT56CacheWriteIsOnePointTwoFiveTimesInput(t *testing.T) {
	for _, modelName := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		rate, ok, err := Resolve(modelName, nil)
		if err != nil || !ok || rate.CacheWriteNanoPerToken == nil {
			t.Fatalf("%s cache-write rate missing: rate=%#v ok=%v err=%v", modelName, rate, ok, err)
		}
		if *rate.CacheWriteNanoPerToken*4 != rate.InputNanoPerToken*5 {
			t.Fatalf("%s cache-write rate is not 1.25x input", modelName)
		}
	}
}

func TestNormalizeOverrides(t *testing.T) {
	overrides, err := NormalizeOverrides(map[string]Override{
		" CODEX-AUTO-REVIEW ": {AliasOf: " GPT-5.6-LUNA "},
		"internal-model": {
			InputUSDPerMillion:           "1.00",
			CachedInputUSDPerMillion:     "0.10",
			CacheWriteInputUSDPerMillion: "1.25",
			OutputUSDPerMillion:          "6.00",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if overrides["codex-auto-review"].AliasOf != "gpt-5.6-luna" {
		t.Fatalf("alias was not normalized: %#v", overrides)
	}
	rate, ok, err := Resolve("codex-auto-review", overrides)
	if err != nil || !ok || rate.CanonicalModel != "gpt-5.6-luna" || !rate.Custom {
		t.Fatalf("unexpected alias resolution: rate=%#v ok=%v err=%v", rate, ok, err)
	}
	rate, ok, err = Resolve("internal-model", overrides)
	if err != nil || !ok || rate.InputNanoPerToken != 1000 || rate.CacheWriteNanoPerToken == nil || *rate.CacheWriteNanoPerToken != 1250 {
		t.Fatalf("unexpected custom rate: rate=%#v ok=%v err=%v", rate, ok, err)
	}
}

func TestNormalizeOverridesRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]Override
	}{
		{"negative", map[string]Override{"x": {InputUSDPerMillion: "-1", CachedInputUSDPerMillion: "0.1", CacheWriteInputUSDPerMillion: "1", OutputUSDPerMillion: "1"}}},
		{"too many decimals", map[string]Override{"x": {InputUSDPerMillion: "0.0001", CachedInputUSDPerMillion: "0.1", CacheWriteInputUSDPerMillion: "1", OutputUSDPerMillion: "1"}}},
		{"missing rate", map[string]Override{"x": {InputUSDPerMillion: "1", OutputUSDPerMillion: "1"}}},
		{"missing cache write", map[string]Override{"x": {InputUSDPerMillion: "1", CachedInputUSDPerMillion: "0.1", OutputUSDPerMillion: "1"}}},
		{"alias to custom", map[string]Override{"x": {AliasOf: "y"}, "y": {InputUSDPerMillion: "1", CachedInputUSDPerMillion: "0.1", CacheWriteInputUSDPerMillion: "1", OutputUSDPerMillion: "1"}}},
		{"alias cycle", map[string]Override{"x": {AliasOf: "y"}, "y": {AliasOf: "x"}}},
		{"alias and rate", map[string]Override{"x": {AliasOf: "gpt-5.6-luna", InputUSDPerMillion: "1"}}},
		{"built-in override", map[string]Override{"gpt-5.4": {InputUSDPerMillion: "1", CachedInputUSDPerMillion: "0.1", OutputUSDPerMillion: "1"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeOverrides(test.overrides); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestModelMatchingDoesNotGuessFamilies(t *testing.T) {
	for _, name := range []string{"gpt-5.6-turbo", "gpt-5.4-pro", "codex-auto-review", ""} {
		if _, ok, err := Resolve(name, nil); err != nil || ok {
			t.Fatalf("model %q should remain unpriced, ok=%v err=%v", name, ok, err)
		}
	}
}
