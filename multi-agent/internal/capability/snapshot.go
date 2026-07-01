// Package capability snapshot machinery: Snapshot struct (collected
// inspectable facts about the slave host), ComputeHash for deterministic
// identity, NewCredentialAlias / NewSnapshot factories that enforce the
// 13号§3.4 capability-kind invariants, and ablation registration of
// NoCapabilityDiscovery. See docs/specs/wt1-capability-snapshot.spec.md
// for the design and security argument.
package capability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/yourorg/multi-agent/internal/ablation"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

// ---------------------------------------------------------------------
// Types (spec §3.1)
// ---------------------------------------------------------------------

// ToolVersion is one entry in Snapshot.Tools. Version is free-form
// (semver / commit hash / "unknown") so the snapshot collector records
// whatever the upstream `--version` flag returned without coercion.
type ToolVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CredentialAlias is an opaque placeholder identifying a credential the
// agent can broker. Constructed only via NewCredentialAlias, which
// rejects any value that looks like a raw token. See spec §7(a).
type CredentialAlias string

// NetworkReach is the effective outbound network reach the host has at
// snapshot time. Single-valued: every host has exactly one of these.
type NetworkReach string

const (
	NetworkInternet     NetworkReach = "internet"
	NetworkIntranet     NetworkReach = "intranet"
	NetworkLoopbackOnly NetworkReach = "loopback-only"
	NetworkNone         NetworkReach = "none"
)

// FileResource is one entry in Snapshot.Files. KindDetail must be one of
// the 13号§3.4 enum: repo|dataset|fixture|config (enforced by NewSnapshot).
type FileResource struct {
	KindDetail  string `json:"kind_detail"`
	PathPattern string `json:"path_pattern"`
}

// Snapshot is the collected inspectable capability surface of a slave
// host at one point in time. Field declaration order is part of the hash
// contract (encoding/json marshals struct fields in declaration order);
// reordering fields changes ComputeHash output and is a breaking change.
type Snapshot struct {
	OS                string                          `json:"os"`
	Arch              string                          `json:"arch"`
	Platform          commandiface.Platform           `json:"platform"`
	CommandInterfaces []commandiface.CommandInterface `json:"command_interfaces,omitempty"`
	Tools             []ToolVersion                   `json:"tools,omitempty"`
	MCPTools          []MCPToolDescriptor             `json:"mcp_tools,omitempty"`
	Files             []FileResource                  `json:"files,omitempty"`
	Network           NetworkReach                    `json:"network"`
	Credentials       []CredentialAlias               `json:"credentials,omitempty"`
}

// ---------------------------------------------------------------------
// Sentinel errors (spec §3.1)
// ---------------------------------------------------------------------

var (
	// ErrLooksLikeRawCredential is returned by NewCredentialAlias when
	// the input matches any of the raw-token regexes in §7(a).
	ErrLooksLikeRawCredential = errors.New("capability: value looks like a raw credential token")

	// ErrAliasInvalidShape is returned by NewCredentialAlias when the
	// input does not match the §3.4 alias regex.
	ErrAliasInvalidShape = errors.New("capability: credential alias does not match ^[a-z][a-z0-9_]{2,63}$")

	// ErrSnapshotInvalid is returned by NewSnapshot when a construction
	// invariant fails. Callers test via errors.Is.
	ErrSnapshotInvalid = errors.New("capability: snapshot invalid")
)

// ---------------------------------------------------------------------
// Raw-token catalogue (spec §7(a))
// ---------------------------------------------------------------------

// rawTokenPatterns is the catalogue of regular expressions matching
// commonly-leaked raw credential token shapes. Used by both
// NewCredentialAlias (construction-time defence) and JSONContainsRawToken
// (pre-write defence in observerstore). Adding a new shape hardens both
// surfaces at once. See spec §7(a) for the Phase-1 catalogue; §7.1 lists
// known gaps deferred to future worktrees (AWS secret access keys,
// Stripe sk_live_, Twilio AC… etc. — all too ambiguous to regex without
// an entropy check that this Phase-1 deliverable does not implement).
//
// All patterns are CASE-INSENSITIVE (`(?i)`) — leaked tokens often
// arrive uppercased in screenshots/log dumps, and the pre-write guard
// must reject `SK-...` / `GHP_...` / `XOXB-...` just as firmly as their
// canonical lowercase forms.
var rawTokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)sk-[A-Z0-9_-]{10,}`),                                      // OpenAI / Anthropic-shaped API keys (incl. sk-ant-api03-…)
	regexp.MustCompile(`(?i)eyJ[A-Z0-9_-]{10,}\.[A-Z0-9_-]{10,}(\.[A-Z0-9_-]{10,})?`), // JWT / OIDC tokens: 2+ base64 segments (round-5 audit: single-segment false-positives on identifier.name phrasings)
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16,}`),                                       // AWS access key ID
	regexp.MustCompile(`(?i)ghp_[A-Z0-9]{20,}`),                                       // GitHub classic personal access token
	regexp.MustCompile(`(?i)github_pat_[A-Z0-9_]{20,}`),                               // GitHub fine-grained personal access token
	regexp.MustCompile(`(?i)AIza[A-Z0-9_-]{30,}`),                                     // Google API key
	regexp.MustCompile(`(?i)xox[bapres]-[A-Z0-9-]+`),                                  // Slack bot/app/user/refresh/eshare/legacy tokens (incl. xoxe-)
}

// aliasShapeRe enforces the §3.4 alias regex on the CredentialAlias type.
var aliasShapeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

// JSONContainsRawToken returns true if b contains a substring matching
// any of the raw-token regexes in rawTokenPatterns. Used by
// observerstore.WriteSnapshot before insert.
func JSONContainsRawToken(b []byte) bool {
	for _, re := range rawTokenPatterns {
		if re.Match(b) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// NewCredentialAlias (spec §3.1, §7(a))
// ---------------------------------------------------------------------

// NewCredentialAlias is the only constructor for CredentialAlias.
// Raw-token check runs FIRST so a developer pasting a real key gets the
// informative ErrLooksLikeRawCredential rather than the less-helpful
// ErrAliasInvalidShape.
func NewCredentialAlias(s string) (CredentialAlias, error) {
	for _, re := range rawTokenPatterns {
		if re.MatchString(s) {
			return "", ErrLooksLikeRawCredential
		}
	}
	if !aliasShapeRe.MatchString(s) {
		return "", ErrAliasInvalidShape
	}
	return CredentialAlias(s), nil
}

// ---------------------------------------------------------------------
// NewSnapshot factory (spec §3.1, §3.2, §3.3)
// ---------------------------------------------------------------------

// NewSnapshot validates a Snapshot's construction invariants and
// returns the validated value (or ErrSnapshotInvalid wrapped with the
// specific field that failed). All §3.4 capability kinds must be
// satisfiable from the returned value without further inference.
func NewSnapshot(spec Snapshot) (Snapshot, error) {
	if spec.OS == "" {
		return Snapshot{}, fmt.Errorf("%w: OS empty", ErrSnapshotInvalid)
	}
	if spec.Arch == "" {
		return Snapshot{}, fmt.Errorf("%w: Arch empty", ErrSnapshotInvalid)
	}
	if spec.OS != spec.Platform.OS {
		return Snapshot{}, fmt.Errorf("%w: OS=%q drifts from Platform.OS=%q", ErrSnapshotInvalid, spec.OS, spec.Platform.OS)
	}
	if spec.Arch != spec.Platform.Arch {
		return Snapshot{}, fmt.Errorf("%w: Arch=%q drifts from Platform.Arch=%q", ErrSnapshotInvalid, spec.Arch, spec.Platform.Arch)
	}
	switch spec.Network {
	case NetworkInternet, NetworkIntranet, NetworkLoopbackOnly, NetworkNone:
		// ok
	default:
		return Snapshot{}, fmt.Errorf("%w: unknown NetworkReach %q", ErrSnapshotInvalid, spec.Network)
	}
	for i, tv := range spec.Tools {
		if tv.Name == "" {
			return Snapshot{}, fmt.Errorf("%w: Tools[%d].Name empty", ErrSnapshotInvalid, i)
		}
	}
	for i, f := range spec.Files {
		switch f.KindDetail {
		case "repo", "dataset", "fixture", "config":
			// ok
		default:
			return Snapshot{}, fmt.Errorf("%w: Files[%d].KindDetail %q (want repo|dataset|fixture|config)", ErrSnapshotInvalid, i, f.KindDetail)
		}
	}
	// MCPTools.InputSchema is json.RawMessage; ComputeHash relies on
	// json.Marshal being infallible AND on byte-identical inputs
	// producing identical hashes. Validate + CANONICALISE at
	// construction time so that (a) a malformed InputSchema can never
	// produce two distinct snapshots that collide on ComputeHash's
	// error-path constant, and (b) two semantically-identical schemas
	// with different byte layouts (`{"a":1,"b":2}` vs `{"b":2,"a":1}`
	// or embedded whitespace) hash to the same value — otherwise dedup
	// at the observer breaks silently (round-5 audit finding).
	//
	// canonicalJSONBytes unmarshals into interface{}, then re-marshals
	// with sorted object keys via json.Marshal (which sorts map[string]…
	// keys deterministically per Go 1.12+ spec).
	if len(spec.MCPTools) > 0 {
		normed := make([]MCPToolDescriptor, len(spec.MCPTools))
		for i, mt := range spec.MCPTools {
			normed[i] = mt
			if len(mt.InputSchema) == 0 {
				continue
			}
			if !json.Valid(mt.InputSchema) {
				return Snapshot{}, fmt.Errorf("%w: MCPTools[%d].InputSchema is not valid JSON", ErrSnapshotInvalid, i)
			}
			canon, err := canonicalJSONBytes(mt.InputSchema)
			if err != nil {
				return Snapshot{}, fmt.Errorf("%w: MCPTools[%d].InputSchema: %v", ErrSnapshotInvalid, i, err)
			}
			normed[i].InputSchema = canon
		}
		spec.MCPTools = normed
	}
	return spec, nil
}

// canonicalJSONBytes returns a deterministic byte encoding of the JSON
// value in b: whitespace-free, with object keys sorted at every nesting
// level, AND with every JSON number normalised to a single canonical
// literal form. Used to normalise MCPTools.InputSchema so semantically-
// identical schemas hash identically.
//
// The map-key sort comes for free from encoding/json (it marshals
// map[string]… keys lexicographically). The number normalisation is
// hand-rolled because Go's json.Number preserves the input literal
// verbatim — so `1e10`, `1E10`, `1.0`, `1e+10`, `0e0` and `0` all
// round-trip to distinct byte sequences, which would silently defeat
// hash dedup (round-6 audit).
//
// Algorithm:
//  1. Decode into interface{} with UseNumber to keep precision.
//  2. Walk the tree; for every json.Number, replace with a
//     canonicalNumber wrapper that emits a canonical form via
//     big.Rat. Integer values remain integers (e.g. `1.0` → `1`);
//     non-integer rationals emit the shortest decimal that
//     round-trips through big.Rat.
//  3. Re-marshal; encoding/json sorts map keys and calls
//     canonicalNumber.MarshalJSON for numeric literals.
//
// big.Rat handles ANY JSON-representable number without float64 loss
// because JSON numbers are always rational (the grammar has no
// irrationals).
func canonicalJSONBytes(b json.RawMessage) (json.RawMessage, error) {
	var v interface{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Reject trailing tokens ({"a":1}{"b":2} shape — json.Valid also
	// rejects this, so callers are already protected, but belt-and-braces).
	if dec.More() {
		return nil, fmt.Errorf("canonical json: trailing tokens after value")
	}
	v, err := canonicaliseNumbers(v)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

// maxNumberLiteralLen bounds the byte length of a json.Number literal
// the canonicaliser will process. Motivation (round-7 audit P2): an
// adversarial MCP tool descriptor with `{"n":1e1000000}` forces the
// canonicaliser to allocate ~1MB per snapshot inside big.Int, DoSing
// every capability collector on the network. Legitimate JSON-schema
// numeric constraints comfortably fit in 64 bytes.
const maxNumberLiteralLen = 64

// maxNumberBitLen bounds the BitLen of a parsed rational's numerator
// AND denominator (each). Motivation (round-8 audit P1): the literal-
// length cap is necessary but NOT sufficient — a short literal like
// `1e-100000` (9 bytes) parses to 1/10^100000 whose denominator has
// ~332,000 bits, sending exactDecimalPrec into a ~12-second loop and
// FloatString into a ~100KB allocation. Bounding BitLen bounds both
// the trial-division loop count (which is at most log2(denom)) AND
// the output string size (~BitLen / 3.3 decimal chars). 4096 covers
// integer/decimal magnitudes up to ~1e1233 — well beyond any
// legitimate JSON schema value — while capping the worst-case output
// at ~1.2KB and worst-case compute at sub-millisecond.
const maxNumberBitLen = 4096

// maxNumberAbsExponent bounds the absolute value of the exponent in a
// json.Number literal we will hand to big.Rat.SetString. Motivation
// (round-9 audit P2): the BitLen cap runs AFTER SetString, so a short
// literal like `1e-999999` still burns ~28 ms of wall time inside the
// parser BEFORE the cap rejects it (SetString allocates a ~3M-bit
// bignum). A cheap pre-parse guard on the exponent digits — bounded to
// ~62 characters by maxNumberLiteralLen — closes this residual DoS at
// the parser boundary.
//
// 1250 comfortably exceeds the decimal magnitude the BitLen cap admits
// (log10(2^4096) ≈ 1233); anything past this is guaranteed to be
// rejected by the BitLen cap anyway, so we short-circuit before the
// expensive parse.
const maxNumberAbsExponent = 1250

// exponentAbsExceedsCap returns true if the JSON number literal in s
// carries an exponent whose absolute value exceeds maxNumberAbsExponent.
// Non-scientific literals (no `e`/`E`) return false.
//
// The scan is intentionally tolerant of unusual-but-JSON-legal shapes
// like `1E+42` / `1e-42` / `1.5e10`; it locates the exponent marker,
// skips an optional sign, and parses the remaining digits. If the
// digits overflow int (which requires >~19 characters, well past our
// 64-byte input cap × common sense), it treats the literal as over
// the cap.
func exponentAbsExceedsCap(s string) bool {
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return false
	}
	exp := s[i+1:]
	if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
		exp = exp[1:]
	}
	if exp == "" {
		return false // malformed — leave the real reject to SetString
	}
	// Count leading zeros; if the remaining digit run is longer than
	// what could possibly encode a value ≤ maxNumberAbsExponent (4
	// digits for values up to 9999), we know it exceeds the cap
	// without needing to parse. This avoids int-overflow concerns for
	// arbitrarily long exponent digit strings.
	trimmed := strings.TrimLeft(exp, "0")
	if len(trimmed) > 4 {
		return true
	}
	// trimmed fits in at most 4 digits → parse safely.
	n := 0
	for _, c := range trimmed {
		if c < '0' || c > '9' {
			return false // non-digit → leave rejection to SetString
		}
		n = n*10 + int(c-'0')
	}
	return n > maxNumberAbsExponent
}

// canonicalNumber is a wrapper around big.Rat that marshals to a single
// canonical decimal literal per rational value. Integer values emit as
// `N`; non-integers emit the EXACT shortest decimal (all trailing zeros
// trimmed). No leading `+`, no uppercase `E`, no exponent notation.
//
// The exact-decimal property holds for every rational whose denominator
// (in lowest terms) is 2^a * 5^b — which is EVERY rational parseable
// from a JSON literal, since JSON numbers are finite decimals of the
// form m * 10^e (denominator = 10^(-e) = 2^(-e) * 5^(-e)). This is
// exploited by exactDecimalPrec: no round-trip probe loop, no
// truncation, no small-fraction collisions (round-7 audit P1).
type canonicalNumber struct{ r *big.Rat }

// MarshalJSON emits the canonical form. Errors only if the value is
// (impossibly, for JSON-derived inputs) a repeating decimal like 1/3.
func (c canonicalNumber) MarshalJSON() ([]byte, error) {
	if c.r.IsInt() {
		return []byte(c.r.Num().String()), nil
	}
	prec, ok := exactDecimalPrec(c.r)
	if !ok {
		// Repeating decimal — unreachable from any JSON literal input,
		// but a hand-built big.Rat like 1/3 would land here. Refuse to
		// emit a truncated form rather than silently collide two
		// different rationals to the same string. The caller sees
		// this as a canonicalisation error and either fixes the source
		// or accepts that this exotic path was never on the input
		// contract.
		return nil, fmt.Errorf("canonical json: number %s has a repeating decimal expansion", c.r.String())
	}
	s := c.r.FloatString(prec)
	return []byte(trimTrailingZeros(s)), nil
}

// exactDecimalPrec returns the minimum k such that r.FloatString(k) is
// an EXACT decimal representation of r. For a rational whose lowest-
// terms denominator factors as 2^a * 5^b, that k = max(a, b). Returns
// (0, false) if the denominator has any other prime factor (i.e. the
// rational has a repeating decimal). Every JSON-literal-derived rational
// satisfies the 2-and-5-only condition.
func exactDecimalPrec(r *big.Rat) (int, bool) {
	d := new(big.Int).Set(r.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	tmp := new(big.Int)
	a := 0
	for {
		tmp.Mod(d, two)
		if tmp.Sign() != 0 {
			break
		}
		d.Quo(d, two)
		a++
	}
	b := 0
	for {
		tmp.Mod(d, five)
		if tmp.Sign() != 0 {
			break
		}
		d.Quo(d, five)
		b++
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return 0, false
	}
	if a > b {
		return a, true
	}
	return b, true
}

// trimTrailingZeros strips redundant fractional trailing zeros from a
// decimal produced by big.Rat.FloatString. "1.500" → "1.5"; "2.000"
// → "2"; "0.0" → "0"; "-0.000" → "0" (big.Rat normalises signed zero,
// but the guard covers the edge case).
func trimTrailingZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" || s == "-0" {
		return "0"
	}
	return s
}

// canonicaliseNumbers walks a decoded JSON value tree, replacing every
// json.Number with a canonicalNumber wrapper. Non-numeric nodes pass
// through unchanged. Recurses into maps and slices.
//
// Numeric literals longer than maxNumberLiteralLen bytes are rejected
// via ErrSnapshotInvalid (surfaced by NewSnapshot's caller) — see the
// DoS-defence rationale on maxNumberLiteralLen.
func canonicaliseNumbers(v interface{}) (interface{}, error) {
	switch t := v.(type) {
	case json.Number:
		if len(string(t)) > maxNumberLiteralLen {
			return nil, fmt.Errorf("canonical json: number literal %q exceeds %d-byte cap (adversarial-DoS defence)", string(t), maxNumberLiteralLen)
		}
		// Pre-parse exponent guard (round-9 audit P2): big.Rat.SetString
		// on a short-literal huge-exponent input (`1e-999999`) still
		// allocates a ~3M-bit bignum during parsing, ~28 ms wall time
		// per literal. Reject overtly-oversized exponents at the
		// parser boundary before SetString runs.
		if exponentAbsExceedsCap(string(t)) {
			return nil, fmt.Errorf("canonical json: number literal %q has an exponent whose absolute value exceeds %d (adversarial-DoS defence)", string(t), maxNumberAbsExponent)
		}
		// big.Rat.SetString accepts JSON number grammar (decimal +
		// exponent). If it can't parse, that means json.Number handed
		// us bytes json.Decode itself would reject on re-parse — belt-
		// and-braces error.
		r, ok := new(big.Rat).SetString(string(t))
		if !ok {
			return nil, fmt.Errorf("canonical json: json.Number %q not a valid rational", string(t))
		}
		// Second DoS cap (round-8 audit P1): a short literal like
		// "1e-100000" (9 bytes) parses to a rational whose DENOMINATOR
		// has ~332,000 bits, sending exactDecimalPrec into a multi-
		// second loop and FloatString into a large allocation. Bounding
		// numerator+denominator BitLen bounds both the trial-division
		// loop and the output string size.
		if bl := r.Num().BitLen(); bl > maxNumberBitLen {
			return nil, fmt.Errorf("canonical json: number literal %q parses to a rational whose numerator has %d bits (cap %d, adversarial-DoS defence)", string(t), bl, maxNumberBitLen)
		}
		if bl := r.Denom().BitLen(); bl > maxNumberBitLen {
			return nil, fmt.Errorf("canonical json: number literal %q parses to a rational whose denominator has %d bits (cap %d, adversarial-DoS defence)", string(t), bl, maxNumberBitLen)
		}
		return canonicalNumber{r: r}, nil
	case map[string]interface{}:
		for k, child := range t {
			nc, err := canonicaliseNumbers(child)
			if err != nil {
				return nil, err
			}
			t[k] = nc
		}
		return t, nil
	case []interface{}:
		for i, child := range t {
			nc, err := canonicaliseNumbers(child)
			if err != nil {
				return nil, err
			}
			t[i] = nc
		}
		return t, nil
	default:
		return v, nil
	}
}

// ---------------------------------------------------------------------
// Canonical encoding + ComputeHash (spec §4)
// ---------------------------------------------------------------------

// canonical returns a copy of s with every relevant slice sorted in a
// stable, TOTAL key order. The sort is mandatory: without it, the hash
// would depend on the snapshot collector's slice append order, which is
// not part of the capability fact set we want to identify.
//
// For slices whose elements have a natural composite key
// (CommandInterface = Skill+Kind+Command; MCPTool = Server+Name) it is
// possible for two elements to share the same composite key while
// differing in another field (CommandInterface.Default; MCPTool's
// Description/InputSchema/ResultDescription). A non-total key would let
// the sort reorder them and produce different JSON bytes — meaning the
// hash would no longer be slice-order-independent for "near-duplicate"
// entries. To guarantee totality, we fall back to comparing the
// element's full canonical JSON encoding when the composite key
// matches; that breaks the tie deterministically against the actual
// bytes ComputeHash hashes.
func canonical(s Snapshot) Snapshot {
	if s.Tools != nil {
		cp := append([]ToolVersion(nil), s.Tools...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].Name != cp[j].Name {
				return cp[i].Name < cp[j].Name
			}
			return cp[i].Version < cp[j].Version
		})
		s.Tools = cp
	}
	if s.CommandInterfaces != nil {
		cp := append([]commandiface.CommandInterface(nil), s.CommandInterfaces...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].Skill != cp[j].Skill {
				return cp[i].Skill < cp[j].Skill
			}
			if cp[i].Kind != cp[j].Kind {
				return cp[i].Kind < cp[j].Kind
			}
			if cp[i].Command != cp[j].Command {
				return cp[i].Command < cp[j].Command
			}
			// Total tiebreak on the remaining field (Default bool).
			if cp[i].Default != cp[j].Default {
				return !cp[i].Default && cp[j].Default
			}
			return false
		})
		s.CommandInterfaces = cp
	}
	if s.MCPTools != nil {
		cp := append([]MCPToolDescriptor(nil), s.MCPTools...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].Server != cp[j].Server {
				return cp[i].Server < cp[j].Server
			}
			if cp[i].Name != cp[j].Name {
				return cp[i].Name < cp[j].Name
			}
			// (Server, Name) is the natural identity, but MCPToolDescriptor
			// carries Description/InputSchema/ResultDescription too. Tie-
			// break on the full marshalled bytes to guarantee a total order;
			// json.Marshal of MCPToolDescriptor is infallible after the
			// NewSnapshot InputSchema validation, so we use MarshalIndent
			// not at all — plain Marshal is canonical enough for the
			// tiebreak (we are not hashing this — only comparing).
			bi, _ := json.Marshal(cp[i])
			bj, _ := json.Marshal(cp[j])
			return string(bi) < string(bj)
		})
		s.MCPTools = cp
	}
	if s.Files != nil {
		cp := append([]FileResource(nil), s.Files...)
		sort.Slice(cp, func(i, j int) bool {
			if cp[i].KindDetail != cp[j].KindDetail {
				return cp[i].KindDetail < cp[j].KindDetail
			}
			return cp[i].PathPattern < cp[j].PathPattern
		})
		s.Files = cp
	}
	if s.Credentials != nil {
		cp := append([]CredentialAlias(nil), s.Credentials...)
		sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
		s.Credentials = cp
	}
	return s
}

// CanonicalJSON returns the canonical JSON encoding of s. The byte slice
// is what ComputeHash hashes and what observerstore stores in
// snapshot_json, so a post-hoc auditor can recompute the hash from the DB
// row. Exported so observerstore can re-use the canonicalisation logic
// without duplicating it.
//
// Today Marshal of a Snapshot value is infallible; the error return is
// kept for forward compatibility (a future field type might be
// non-Marshallable).
func CanonicalJSON(s Snapshot) ([]byte, error) {
	return json.Marshal(canonical(s))
}

// ComputeHash returns the SHA-256 hex digest of the canonical JSON of s.
// See spec §4 + §7(b) for the rollback-attack rationale: OS and every
// tool version are inputs, so a 1.22 → 1.18 downgrade changes the hash.
//
// ComputeHash panics if CanonicalJSON returns an error. NewSnapshot's
// MCPTools.InputSchema validation makes that path unreachable for any
// Snapshot built through the factory; the panic exists to catch the case
// where a caller hand-constructs a Snapshot with a malformed RawMessage
// and would otherwise get a constant placeholder hash that silently
// collides with every other malformed snapshot — a much more dangerous
// outcome than a panic at the call site.
func ComputeHash(s Snapshot) string {
	body, err := CanonicalJSON(s)
	if err != nil {
		panic(fmt.Sprintf("capability.ComputeHash: canonical json failed (snapshot built outside NewSnapshot?): %v", err))
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Hash is the method form. Equivalent to ComputeHash(s). Provided so D1
// run-schema writer can call s.Hash() without importing the function
// name.
func (s Snapshot) Hash() string { return ComputeHash(s) }

// ---------------------------------------------------------------------
// Ablation flag + init() (spec §7(d))
// ---------------------------------------------------------------------

// DisableUpload is the ablation toggle target seen by the ablation
// registry. The registry contract requires a *bool, so this variable
// stays. Readers MUST use IsUploadDisabled() instead — that accessor
// loads from a mirroring atomic.Bool and is race-free.
//
// Spec §7(d): when true, observerstore.WriteSnapshot must short-circuit
// (skip the DB write) while still allowing local snapshot collection to
// proceed — the slave's self-defence logic ("do I have pytest?") depends
// on local collection regardless of upload state.
var DisableUpload bool

// disableUploadAtomic mirrors DisableUpload for race-free reads. See
// SetDisableUpload / SyncDisableUpload / IsUploadDisabled.
var disableUploadAtomic atomic.Bool

// IsUploadDisabled returns the ablation flag's value via an atomic load.
// This is the ONLY correct way to read the flag from a concurrent
// context. Reading the raw DisableUpload variable races with any
// writer.
func IsUploadDisabled() bool { return disableUploadAtomic.Load() }

// SetDisableUpload sets both the atomic mirror and the raw bool.
// Direct consumers (tests, production code that needs to flip the flag
// without going through the ablation registry) MUST use this.
func SetDisableUpload(v bool) {
	disableUploadAtomic.Store(v)
	DisableUpload = v
}

// SyncDisableUpload copies the current raw DisableUpload value into the
// atomic mirror. The Phase-2 CLI binder MUST call this after any
// ablation.Default.SetByName("NoCapabilityDiscovery", ...) batch and
// BEFORE spawning any goroutine that calls WriteSnapshot — the ablation
// registry writes only through the *bool, and readers see the atomic.
//
// Safe to call from a single goroutine at process start. NOT safe to
// call concurrently with WriteSnapshot without external synchronisation
// — but the ablation package's own "pre-run-only mutation" contract
// covers that.
func SyncDisableUpload() {
	disableUploadAtomic.Store(DisableUpload)
}

func init() {
	if err := ablation.Default.Register(ablation.NoCapabilityDiscovery, &DisableUpload); err != nil {
		// init-time panic would DoS the whole process before main runs;
		// the ablation contract says Register never panics. The only
		// failure modes here ("unknown flag", "duplicate registration",
		// "nil target", "target already registered under another flag")
		// all indicate a build-time bug. Log loudly so dev / test see
		// it; do not panic.
		log.Printf("capability: ablation registration failed: %v", err)
	}
}
