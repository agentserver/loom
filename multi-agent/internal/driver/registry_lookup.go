package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// registry_lookup.go implements driver.Lookup — see spec
// docs/specs/wt2-driver-promotion-chain-B4.spec.md.

// Hit is one lookup result.
type Hit struct {
	Source  string  `json:"source"`  // "registry" | "userspace"
	MCPName string  `json:"mcp_name"`
	Score   float64 `json:"score"`
	Detail  string  `json:"detail,omitempty"`
}

// PackageHit is a driver-side view of a userspace search result.
// Score is position-derived by the driver (packageHitsToHits below);
// the userspace API doesn't expose a per-row bm25 today. If a future
// userspace change adds a per-row score, add a `Rank` field HERE and
// wire the adapter in cmd/driver-agent/main.go — the previous
// PackageHit.Rank field was dead (never populated) and was removed
// per PR #71 review P1-B4-1 to prevent a future caller from setting
// it and having it silently ignored.
type PackageHit struct {
	Slug        string
	Description string
}

// UserspaceSearcher is the narrow surface Lookup needs from the
// userspace store. Kept as an interface so tests can inject a mock.
type UserspaceSearcher interface {
	SearchPackagesForIdentity(q, workspaceID, userID, kindFilter string, limit int) ([]PackageHit, error)
}

// RegistryLookupSample is the driver-side sample record threaded into
// the caller-supplied SampleWrite dep. The observerstore side has a
// structurally-identical RegistryLookupSampleRow — the driver-agent
// wiring adapts between them.
type RegistryLookupSample struct {
	Queried         time.Time
	RunID           string
	WorkspaceID     string
	QueryHashPrefix string
	HitCount        int
	RegistryHits    int
	UserspaceHits   int
	TopScore        float64
}

// LookupDeps is the injected dependency bundle. Set once at process
// start via SetLookupDeps.
type LookupDeps struct {
	UserspaceStore UserspaceSearcher
	WorkspaceID    string
	UserID         string
	SampleWrite    func(ctx context.Context, s RegistryLookupSample) error
	CurrentRunID   func() string
}

var (
	lookupDepsMu           sync.RWMutex
	lookupDeps             LookupDeps
	sampleWriteWarnOnce    sync.Once
	userspaceStoreWarnOnce sync.Once
)

// SetLookupDeps publishes the LookupDeps bundle. Nil-valued fields
// are honoured; the pipeline degrades to a WARN log on first Lookup
// call when a downstream dep is missing.
func SetLookupDeps(d LookupDeps) {
	lookupDepsMu.Lock()
	defer lookupDepsMu.Unlock()
	lookupDeps = d
}

func getLookupDeps() LookupDeps {
	lookupDepsMu.RLock()
	defer lookupDepsMu.RUnlock()
	return lookupDeps
}

// resetLookupDepsForTest — test-only.
func resetLookupDepsForTest() {
	lookupDepsMu.Lock()
	defer lookupDepsMu.Unlock()
	lookupDeps = LookupDeps{}
	sampleWriteWarnOnce = sync.Once{}
	userspaceStoreWarnOnce = sync.Once{}
}

// sanitizerRE — spec §3. Strip chars outside `[A-Za-z0-9_ .]`.
// The `-` character is NOT accepted because FTS5 treats it as a NOT
// operator prefix on tokens; a bare `-foo bar` would exclude foo
// from the results. Keeping the class tight closes that surface.
var sanitizerRE = regexp.MustCompile(`[^A-Za-z0-9_ .]+`)
var whitespaceCollapseRE = regexp.MustCompile(`\s+`)

// fts5OperatorWords are the tokens FTS5 treats as boolean operators
// when uppercase. Lowercasing them (as we do here) demotes them to
// plain search terms.
var fts5OperatorWords = map[string]struct{}{
	"AND": {}, "OR": {}, "NOT": {}, "NEAR": {},
}

const lookupQueryMaxLen = 256
const lookupResultCap = 20

func sanitizeLookupQuery(raw string) string {
	return sanitizeLookupQueryWithOptions(raw, true /*logTruncation*/)
}

// sanitizeLookupQueryWithOptions is the shared implementation. When
// logTruncation is false the caller wants a silent sanitisation
// (used by the ablation-log path so an ablated call still produces
// exactly ONE log line — see PR #71 review P1-B4-2).
func sanitizeLookupQueryWithOptions(raw string, logTruncation bool) string {
	q := raw
	if len(q) > lookupQueryMaxLen {
		if logTruncation {
			log.Printf("driver.Lookup: query truncated from %d to %d chars", len(q), lookupQueryMaxLen)
		}
		q = q[:lookupQueryMaxLen]
	}
	q = sanitizerRE.ReplaceAllString(q, "")
	q = whitespaceCollapseRE.ReplaceAllString(q, " ")
	q = strings.TrimSpace(q)
	// Lowercase any FTS5 operator words so they don't parse as
	// operators. Split on whitespace, filter, rejoin.
	if q == "" {
		return q
	}
	tokens := strings.Fields(q)
	for i, tok := range tokens {
		if _, isOp := fts5OperatorWords[strings.ToUpper(tok)]; isOp {
			tokens[i] = strings.ToLower(tok)
		}
	}
	return strings.Join(tokens, " ")
}

// hashPrefix returns the first 8 hex chars of sha256(raw). Empty raw
// yields "" so downstream log lines can omit the field.
func hashPrefix(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:8]
}

// Lookup — see spec §2. Never returns an error; degrades on each
// source's failure with a log line.
func Lookup(ctx context.Context, query string) []Hit {
	// Ablation short-circuit BEFORE any other side effect (log,
	// sanitizer, sample write, counter bump). Doing this first
	// upholds §7 (c) "silent skip would break the paper's ablation
	// audit story" — the only observable side-effect is the single
	// [ablation] log line. Even the truncation log is deferred so
	// an ablated call is one log line and nothing else.
	if IsNoRegistryLookup() {
		hp := hashPrefix(query)
		// Use the SHARED sanitizer (silent-truncation mode) so the
		// ablation log line renders exactly what a non-ablated call
		// would send to FTS5 — including the AND/OR/NOT/NEAR
		// lowercasing. Previously the ablation branch had its own
		// inline sanitiser that skipped the operator-lowercase step,
		// producing inconsistent log renderings between the two
		// paths. Fixed per PR #71 review P1-B4-2.
		q := sanitizeLookupQueryWithOptions(query, false /*logTruncation*/)
		// §7 (h): cap the log-side render at 64 chars so an
		// alphanumeric secret-shaped input (which the sanitizer
		// cannot strip because letters/digits are legitimate query
		// tokens) is bounded in the log. The 8-hex query_hash gives
		// operators a way to correlate across log entries.
		if len(q) > 64 {
			q = q[:64]
		}
		log.Printf("[ablation] NoRegistryLookup: skipped query_sanitized=%q query_hash=%s", q, hp)
		return nil
	}

	// Non-ablated path: surface init errors, sanitize with logging,
	// bump counters, run searches.
	surfaceInitErrorOnce()
	sanitized := sanitizeLookupQuery(query)
	hp := hashPrefix(query)
	bumpLookupQuery()

	// Empty-sanitised-query guard. When the caller sends
	// all-punctuation / emoji / meta chars, sanitized == "" and
	// SearchPackagesForIdentity(q="") would fall into its
	// "list-all-packages" branch (up to lookupResultCap unrelated
	// rows), which the metric writer would count as HITS — a caller
	// sending `""""""""` could artificially lift
	// RegistryLookupHitRate. Short-circuit here and still write a
	// 0-hit sample so per-run denominator stays correct.
	var (
		registryHits  []Hit
		userspaceHits []Hit
		merged        []Hit
	)
	if sanitized == "" {
		log.Printf("driver.Lookup: sanitised query empty (raw len=%d); returning 0 hits", len(query))
	} else {
		// Registry side — read the per-slave in-process view.
		names, descHashFn := snapshotAll()
		_ = descHashFn // reserved; not used for lookup matching yet
		registryHits = searchRegistryView(sanitized, names)

		// Userspace side — nil-safe.
		deps := getLookupDeps()
		if deps.UserspaceStore == nil {
			userspaceStoreWarnOnce.Do(func() {
				log.Printf("[warn] LookupDeps.UserspaceStore unwired — userspace search disabled for this process")
			})
		} else {
			rows, err := deps.UserspaceStore.SearchPackagesForIdentity(sanitized, deps.WorkspaceID, deps.UserID, "", lookupResultCap)
			if err != nil {
				log.Printf("driver.Lookup: userspace search error (registry-only degrade): %v", err)
			} else {
				userspaceHits = packageHitsToHits(rows)
			}
		}

		merged = mergeAndCap(registryHits, userspaceHits, lookupResultCap)
		if len(merged) > 0 {
			bumpLookupHit()
		}
	}
	deps := getLookupDeps()

	// Sample-writer — nil-safe.
	sample := RegistryLookupSample{
		Queried:         time.Now().UTC(),
		WorkspaceID:     deps.WorkspaceID,
		QueryHashPrefix: hp,
		HitCount:        len(merged),
		RegistryHits:    len(registryHits),
		UserspaceHits:   len(userspaceHits),
		TopScore:        topScore(merged),
	}
	if deps.CurrentRunID != nil {
		sample.RunID = deps.CurrentRunID()
	}
	if deps.SampleWrite == nil {
		sampleWriteWarnOnce.Do(func() {
			log.Printf("[warn] SampleWrite unwired — registry_lookup_samples rows not persisted for this process")
		})
	} else {
		if err := deps.SampleWrite(ctx, sample); err != nil {
			log.Printf("driver.Lookup: SampleWrite failed: %v", err)
		}
	}

	return merged
}

// searchRegistryView returns registry-side Hits for the given query
// against the driver's in-process per-slave view.
func searchRegistryView(query string, names []string) []Hit {
	if query == "" {
		return nil
	}
	lower := strings.ToLower(query)
	var hits []Hit
	for _, fq := range names {
		// fq is "<slave_id>:<mcp_name>"
		parts := strings.SplitN(fq, ":", 2)
		mcpName := fq
		slaveID := ""
		if len(parts) == 2 {
			slaveID = parts[0]
			mcpName = parts[1]
		}
		lowerName := strings.ToLower(mcpName)
		var score float64
		switch {
		case lowerName == lower:
			score = 1.0
		case strings.Contains(lowerName, lower):
			score = 0.7
		}
		if score > 0 {
			detail := ""
			if slaveID != "" {
				detail = "registered on " + slaveID
			}
			hits = append(hits, Hit{
				Source:  "registry",
				MCPName: fq,
				Score:   score,
				Detail:  detail,
			})
		}
	}
	return hits
}

// packageHitsToHits converts userspace results to Hits with
// position-derived scores. First result → 1.0, twentieth → 0.5.
func packageHitsToHits(rows []PackageHit) []Hit {
	if len(rows) == 0 {
		return nil
	}
	out := make([]Hit, 0, len(rows))
	for i, r := range rows {
		score := 1.0
		if len(rows) > 1 {
			// Linearly interpolate from 1.0 (i=0) down to 0.5 at i=lookupResultCap-1.
			denom := float64(lookupResultCap - 1)
			t := float64(i) / denom
			if t > 1 {
				t = 1
			}
			score = 1.0 - 0.5*t
		}
		desc := r.Description
		if len(desc) > 200 {
			desc = desc[:200]
		}
		out = append(out, Hit{
			Source:  "userspace",
			MCPName: r.Slug,
			Score:   score,
			Detail:  desc,
		})
	}
	return out
}

// mergeAndCap merges the two sources, dedups by MCPName (registry
// wins on collision because it's exact-match state), sorts by score
// descending, and caps at limit.
func mergeAndCap(reg, us []Hit, limit int) []Hit {
	seen := map[string]bool{}
	merged := make([]Hit, 0, len(reg)+len(us))
	for _, h := range reg {
		if !seen[h.MCPName] {
			seen[h.MCPName] = true
			merged = append(merged, h)
		}
	}
	for _, h := range us {
		if !seen[h.MCPName] {
			seen[h.MCPName] = true
			merged = append(merged, h)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func topScore(hits []Hit) float64 {
	if len(hits) == 0 {
		return 0
	}
	return hits[0].Score
}
