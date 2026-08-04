package usage

import (
	"context"
	"os"
	"sync"
)

type fileStamp struct {
	size          int64
	modifiedNanos int64
}

// ActivityProbe performs a metadata-only pass over rollout files. It avoids
// opening JSONL, reading the Codex state database, hashing prefixes, or writing
// SQLite. A full scanner pass is only needed when this snapshot changes.
type ActivityProbe struct {
	mu          sync.Mutex
	initialized bool
	files       map[string]fileStamp
}

func (p *ActivityProbe) Changed(ctx context.Context, homes []string) (bool, error) {
	current := map[string]fileStamp{}
	for _, home := range homes {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		for _, path := range walkRollouts(home) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			current[pathKey(path)] = fileStamp{size: info.Size(), modifiedNanos: info.ModTime().UnixNano()}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized {
		p.initialized = true
		p.files = current
		return false, nil
	}
	changed := len(current) != len(p.files)
	if !changed {
		for path, stamp := range current {
			if previous, ok := p.files[path]; !ok || previous != stamp {
				changed = true
				break
			}
		}
	}
	p.files = current
	return changed, nil
}
