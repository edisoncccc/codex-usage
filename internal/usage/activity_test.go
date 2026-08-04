package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestActivityProbeOnlySignalsRolloutMetadataChanges(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	dir := filepath.Join(home, "sessions", "2026", "08", "03")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "first.jsonl")
	if err := os.WriteFile(first, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := &ActivityProbe{}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || changed {
		t.Fatalf("initial probe changed=%v err=%v", changed, err)
	}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || changed {
		t.Fatalf("stable probe changed=%v err=%v", changed, err)
	}
	file, err := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || !changed {
		t.Fatalf("append probe changed=%v err=%v", changed, err)
	}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || changed {
		t.Fatalf("post-append stable probe changed=%v err=%v", changed, err)
	}
	second := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(second, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || !changed {
		t.Fatalf("create probe changed=%v err=%v", changed, err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if changed, err := probe.Changed(context.Background(), []string{home}); err != nil || !changed {
		t.Fatalf("remove probe changed=%v err=%v", changed, err)
	}
}

func BenchmarkActivityProbe(b *testing.B) {
	home := os.Getenv("CODEX_USAGE_BENCH_HOME")
	if home == "" {
		b.Skip("set CODEX_USAGE_BENCH_HOME to a read-only Codex home")
	}
	probe := &ActivityProbe{}
	if _, err := probe.Changed(context.Background(), []string{home}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := probe.Changed(context.Background(), []string{home}); err != nil {
			b.Fatal(err)
		}
	}
}
