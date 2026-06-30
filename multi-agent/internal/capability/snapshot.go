// Package capability snapshot machinery: Snapshot struct (collected
// inspectable facts about the slave host), ComputeHash for deterministic
// identity, NewCredentialAlias / NewSnapshot factories that enforce the
// 13号§3.4 capability-kind invariants, and ablation registration of
// NoCapabilityDiscovery. See docs/specs/wt1-capability-snapshot.spec.md
// for the design and security argument.
package capability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"

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
	regexp.MustCompile(`(?i)sk-[A-Z0-9_-]{10,}`),        // OpenAI / Anthropic-shaped API keys (incl. sk-ant-api03-…)
	regexp.MustCompile(`(?i)eyJ[A-Z0-9_-]{10,}\.`),      // JWT / OIDC tokens — header + '.' delimiter
	regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16,}`),         // AWS access key ID
	regexp.MustCompile(`(?i)ghp_[A-Z0-9]{20,}`),         // GitHub classic personal access token
	regexp.MustCompile(`(?i)github_pat_[A-Z0-9_]{20,}`), // GitHub fine-grained personal access token
	regexp.MustCompile(`(?i)AIza[A-Z0-9_-]{30,}`),       // Google API key
	regexp.MustCompile(`(?i)xox[bapres]-[A-Z0-9-]+`),    // Slack bot/app/user/refresh/eshare/legacy tokens (incl. xoxe-)
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
	// json.Marshal being infallible. Validate it once at construction
	// time so a malformed InputSchema can never produce two distinct
	// snapshots that collide on ComputeHash's error-path constant.
	for i, mt := range spec.MCPTools {
		if len(mt.InputSchema) == 0 {
			continue
		}
		if !json.Valid(mt.InputSchema) {
			return Snapshot{}, fmt.Errorf("%w: MCPTools[%d].InputSchema is not valid JSON", ErrSnapshotInvalid, i)
		}
	}
	return spec, nil
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

// DisableUpload is the ablation toggle for NoCapabilityDiscovery.
//
// Spec §7(d): when true, observerstore.WriteSnapshot must short-circuit
// (skip the DB write) while still allowing local snapshot collection to
// proceed — the slave's self-defence logic ("do I have pytest?") depends
// on local collection regardless of upload state.
//
// Default is false. Mutated only by the Phase 2 CLI binder via
// ablation.Default.SetByName("NoCapabilityDiscovery", true) before run
// start. The ablation registry's pre-run-only mutation pattern means no
// concurrent reader has to take a lock.
var DisableUpload bool

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
