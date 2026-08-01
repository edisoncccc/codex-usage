package cliui

import (
	"reflect"
	"sort"
	"testing"
)

func TestCatalogsHaveIdenticalKeys(t *testing.T) {
	keys := func(locale Locale) []string {
		result := make([]string, 0, len(messages[locale]))
		for key := range messages[locale] {
			result = append(result, key)
		}
		sort.Strings(result)
		return result
	}
	if !reflect.DeepEqual(keys(Chinese), keys(English)) {
		t.Fatalf("Chinese and English CLI catalogs must have identical keys")
	}
}

func TestDetectLocalePrecedence(t *testing.T) {
	tests := []struct {
		name, explicit, environment, system string
		want                                Locale
	}{
		{"flag", "zh-CN", "en", "en-US", Chinese},
		{"environment", "", "en", "zh-CN", English},
		{"system", "", "", "en_US.UTF-8", English},
		{"fallback", "", "", "fr-FR", Chinese},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Detect(test.explicit, test.environment, test.system)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q want %q", got, test.want)
			}
		})
	}
}

func TestInvalidExplicitLocale(t *testing.T) {
	if _, err := Detect("fr", "en", "en-US"); err == nil {
		t.Fatal("expected invalid explicit locale to fail")
	}
}
