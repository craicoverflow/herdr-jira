package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// keycache.go answers one question: is this candidate a real Jira key?
// A branch name like "pr-4567-fix" yields the candidate PR-4567; Jira omits
// keys it does not know, so one batched lookup separates real keys from
// noise. Answers are cached per key. The cache holds no summary or status;
// labelState.Cache caches the status of confirmed keys.

const (
	foundKeyTTL   = time.Hour
	missingKeyTTL = 10 * time.Minute
	// keyCacheMaxAge is how long any entry is kept before it is pruned on write.
	keyCacheMaxAge = 24 * time.Hour

	keyFound   = "found"
	keyMissing = "missing"
)

type keyCacheEntry struct {
	Result   string    `json:"result"` // keyFound or keyMissing
	Recorded time.Time `json:"recorded"`
}

type keyCacheFile struct {
	CloudID string                   `json:"cloud_id"`
	Keys    map[string]keyCacheEntry `json:"keys"`
}

func keyCachePath() string { return filepath.Join(stateDir(), "keys.json") }

// cloudIDFn returns the signed-in site's cloud ID without any network call
// ("" if unknown). It is a seam for tests.
var cloudIDFn = func() string {
	creds, err := loadCredentials()
	if err != nil {
		return ""
	}
	return creds.CloudID
}

// readKeyCache returns the cache for cloudID. A missing or corrupt file, or
// one filled for a different site, reads as empty.
func readKeyCache(cloudID string) keyCacheFile {
	empty := keyCacheFile{CloudID: cloudID, Keys: map[string]keyCacheEntry{}}
	data, err := os.ReadFile(keyCachePath())
	if err != nil {
		return empty
	}
	var f keyCacheFile
	if json.Unmarshal(data, &f) != nil || f.CloudID != cloudID || f.Keys == nil {
		return empty
	}
	return f
}

// writeKeyCache writes keys.json atomically (temp file, then rename). Two
// writers (tick and the popup) can race; last writer wins and a lost entry
// costs one extra lookup.
func writeKeyCache(f keyCacheFile, now time.Time) error {
	for k, e := range f.Keys {
		if now.Sub(e.Recorded) > keyCacheMaxAge {
			delete(f.Keys, k)
		}
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keys-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, keyCachePath())
}

func (e keyCacheEntry) fresh(now time.Time) bool {
	ttl := missingKeyTTL
	if e.Result == keyFound {
		ttl = foundKeyTTL
	}
	return now.Sub(e.Recorded) < ttl
}

// lookupKeys returns the candidates Jira knows, in candidate order. It
// sends only unknown or expired candidates to src.search, in one batch, and
// returns the statuses it fetched so the caller can seed its own cache.
// force bypasses keys.json.
//
// If the lookup fails and every unknown candidate has an old (expired)
// answer, the old answers are used and no error is returned, like the label
// cache. A signed-out error leaves the cache untouched.
func lookupKeys(ctx context.Context, src jiraSource, candidates []string, force bool) (confirmed []string, statuses map[string]ticketStatus, err error) {
	var cands []string
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" && !seen[c] {
			seen[c] = true
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return nil, nil, nil
	}

	now := nowFn()
	cache := readKeyCache(cloudIDFn())
	var unknown []string
	for _, c := range cands {
		if e, ok := cache.Keys[c]; force || !ok || !e.fresh(now) {
			unknown = append(unknown, c)
		}
	}

	if len(unknown) > 0 {
		found, serr := src.search(ctx, unknown)
		if serr != nil {
			if errors.Is(serr, errSignedOut) {
				return nil, nil, serr
			}
			for _, c := range unknown {
				if _, ok := cache.Keys[c]; !ok {
					return nil, nil, serr
				}
			}
			// Offline with an old answer for everything: keep it.
		} else {
			statuses = map[string]ticketStatus{}
			// search may have just resolved the cloud ID; fill the cache for it.
			if id := cloudIDFn(); id != cache.CloudID {
				cache = keyCacheFile{CloudID: id, Keys: map[string]keyCacheEntry{}}
			}
			for _, c := range unknown {
				if s, ok := found[c]; ok {
					cache.Keys[c] = keyCacheEntry{Result: keyFound, Recorded: now}
					statuses[c] = s
				} else {
					cache.Keys[c] = keyCacheEntry{Result: keyMissing, Recorded: now}
				}
			}
			_ = writeKeyCache(cache, now) // best effort: a lost write costs one lookup
		}
	}

	for _, c := range cands {
		if cache.Keys[c].Result == keyFound {
			confirmed = append(confirmed, c)
		}
	}
	return confirmed, statuses, nil
}
