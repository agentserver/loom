// snapshot_test.go covers spec docs/specs/wt1-capability-snapshot.spec.md §6.
//
// NOTE: tests in this file that mutate the package global
// capability.DisableUpload MUST NOT call t.Parallel(); they share package
// state. Each such test uses t.Cleanup to restore the prior value.
package capability

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yourorg/multi-agent/internal/ablation"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

// ---------------------------------------------------------------------
// NewCredentialAlias — §7(a) raw-token catalogue + §3.4 alias regex
// ---------------------------------------------------------------------

func TestCredentialAlias_ValidShape(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"abc_def", "s3_prod", "x12", "kafka_admin_role"} {
		got, err := NewCredentialAlias(in)
		if err != nil {
			t.Errorf("NewCredentialAlias(%q) returned error %v; want success", in, err)
			continue
		}
		if string(got) != in {
			t.Errorf("NewCredentialAlias(%q) → %q; want %q", in, string(got), in)
		}
	}
}

func TestCredentialAlias_RejectsRawSk(t *testing.T) {
	t.Parallel()
	_, err := NewCredentialAlias("sk-abc123def4567xyz")
	if !errors.Is(err, ErrLooksLikeRawCredential) {
		t.Fatalf("NewCredentialAlias(sk-…): want ErrLooksLikeRawCredential, got %v", err)
	}
}

func TestCredentialAlias_RejectsRawEyJ(t *testing.T) {
	t.Parallel()
	jwt := "eyJhbGciOiJIUzI1NiJ9.ZXlKMGVYQWlPaUpLVjFRaUxDSmhiR2NpT2lKSVV6STFOaUo5.sig"
	_, err := NewCredentialAlias(jwt)
	if !errors.Is(err, ErrLooksLikeRawCredential) {
		t.Fatalf("NewCredentialAlias(eyJ JWT): want ErrLooksLikeRawCredential, got %v", err)
	}
}

func TestCredentialAlias_RejectsRawAKIA(t *testing.T) {
	t.Parallel()
	_, err := NewCredentialAlias("AKIAABCDEFGHIJKLMNOP")
	if !errors.Is(err, ErrLooksLikeRawCredential) {
		t.Fatalf("NewCredentialAlias(AKIA…): want ErrLooksLikeRawCredential, got %v", err)
	}
}

func TestCredentialAlias_RejectsRawGhp(t *testing.T) {
	t.Parallel()
	_, err := NewCredentialAlias("ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345")
	if !errors.Is(err, ErrLooksLikeRawCredential) {
		t.Fatalf("NewCredentialAlias(ghp_…): want ErrLooksLikeRawCredential, got %v", err)
	}
}

func TestCredentialAlias_RejectsRawXox(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-"} {
		in := prefix + "12345-67890-abcdefABCDEF"
		_, err := NewCredentialAlias(in)
		if !errors.Is(err, ErrLooksLikeRawCredential) {
			t.Errorf("NewCredentialAlias(%q): want ErrLooksLikeRawCredential, got %v", in, err)
		}
	}
}

func TestCredentialAlias_RejectsUppercase(t *testing.T) {
	t.Parallel()
	_, err := NewCredentialAlias("MY_KEY")
	if !errors.Is(err, ErrAliasInvalidShape) {
		t.Fatalf("NewCredentialAlias(MY_KEY): want ErrAliasInvalidShape, got %v", err)
	}
}

// ---------------------------------------------------------------------
// NewSnapshot — invariants per spec §3.1 / §3.2 / §3.3
// ---------------------------------------------------------------------

func validSpec(t *testing.T) Snapshot {
	t.Helper()
	return Snapshot{
		OS:       "linux",
		Arch:     "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
	}
}

func TestNewSnapshot_AcceptsValid(t *testing.T) {
	t.Parallel()
	s, err := NewSnapshot(validSpec(t))
	if err != nil {
		t.Fatalf("NewSnapshot(valid): err = %v", err)
	}
	if s.OS != "linux" || s.Arch != "amd64" || s.Network != NetworkInternet {
		t.Errorf("NewSnapshot(valid) returned %+v", s)
	}
}

func TestNewSnapshot_RejectsInconsistentPlatformAndOS(t *testing.T) {
	t.Parallel()
	s := validSpec(t)
	s.OS = "linux"
	s.Platform.OS = "darwin" // drift between flat OS and Platform.OS
	_, err := NewSnapshot(s)
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(OS drift): want ErrSnapshotInvalid, got %v", err)
	}
}

func TestNewSnapshot_RejectsUnknownNetworkReach(t *testing.T) {
	t.Parallel()
	s := validSpec(t)
	s.Network = NetworkReach("wan")
	_, err := NewSnapshot(s)
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(Network=wan): want ErrSnapshotInvalid, got %v", err)
	}
}

func TestNewSnapshot_RejectsUnknownFileKindDetail(t *testing.T) {
	t.Parallel()
	s := validSpec(t)
	s.Files = []FileResource{{KindDetail: "screenshot", PathPattern: "/tmp/x.png"}}
	_, err := NewSnapshot(s)
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(KindDetail=screenshot): want ErrSnapshotInvalid, got %v", err)
	}
}

func TestNewSnapshot_RejectsEmptyToolName(t *testing.T) {
	t.Parallel()
	s := validSpec(t)
	s.Tools = []ToolVersion{{Name: "", Version: "1.0"}}
	_, err := NewSnapshot(s)
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(empty Tool.Name): want ErrSnapshotInvalid, got %v", err)
	}
}

// ---------------------------------------------------------------------
// ComputeHash — spec §4
// ---------------------------------------------------------------------

func TestComputeHash_Stable(t *testing.T) {
	t.Parallel()
	s, err := NewSnapshot(validSpec(t))
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	if a, b := ComputeHash(s), ComputeHash(s); a != b {
		t.Fatalf("ComputeHash not stable: %s vs %s", a, b)
	}
}

func TestComputeHash_DiffersOnOSDowngrade(t *testing.T) {
	t.Parallel()
	a, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
	})
	if err != nil {
		t.Fatalf("NewSnapshot(linux): %v", err)
	}
	b, err := NewSnapshot(Snapshot{
		OS: "darwin", Arch: "amd64",
		Platform: commandiface.Platform{OS: "darwin", Arch: "amd64"},
		Network:  NetworkInternet,
	})
	if err != nil {
		t.Fatalf("NewSnapshot(darwin): %v", err)
	}
	if ComputeHash(a) == ComputeHash(b) {
		t.Fatalf("expected hash change on OS flip linux→darwin; both = %s", ComputeHash(a))
	}
}

func TestComputeHash_DiffersOnToolVersionDowngrade(t *testing.T) {
	t.Parallel()
	mk := func(ver string) Snapshot {
		s, err := NewSnapshot(Snapshot{
			OS: "linux", Arch: "amd64",
			Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
			Network:  NetworkInternet,
			Tools:    []ToolVersion{{Name: "go", Version: ver}},
		})
		if err != nil {
			t.Fatalf("NewSnapshot(go=%s): %v", ver, err)
		}
		return s
	}
	if ComputeHash(mk("1.22.0")) == ComputeHash(mk("1.18.0")) {
		t.Fatal("expected hash change on go version downgrade 1.22.0 → 1.18.0")
	}
}

func TestComputeHash_DiffersOnMCPDescriptorChange(t *testing.T) {
	t.Parallel()
	base, _ := NewSnapshot(validSpec(t))
	extra := base
	extra.MCPTools = []MCPToolDescriptor{{Server: "srv", Name: "tool"}}
	if ComputeHash(base) == ComputeHash(extra) {
		t.Fatal("expected hash change when adding an MCPTool")
	}
}

func TestComputeHash_IndependentOfSliceOrder(t *testing.T) {
	t.Parallel()
	s1, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform:    commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     NetworkInternet,
		Tools:       []ToolVersion{{Name: "go", Version: "1.22"}, {Name: "git", Version: "2.40"}},
		MCPTools:    []MCPToolDescriptor{{Server: "a", Name: "x"}, {Server: "b", Name: "y"}},
		Files:       []FileResource{{KindDetail: "repo", PathPattern: "/a"}, {KindDetail: "config", PathPattern: "/etc/b"}},
		Credentials: []CredentialAlias{"alpha", "beta"},
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot s1: %v", err)
	}
	s2, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform:    commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:     NetworkInternet,
		Tools:       []ToolVersion{{Name: "git", Version: "2.40"}, {Name: "go", Version: "1.22"}}, // reversed
		MCPTools:    []MCPToolDescriptor{{Server: "b", Name: "y"}, {Server: "a", Name: "x"}},      // reversed
		Files:       []FileResource{{KindDetail: "config", PathPattern: "/etc/b"}, {KindDetail: "repo", PathPattern: "/a"}},
		Credentials: []CredentialAlias{"beta", "alpha"},
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot s2: %v", err)
	}
	if ComputeHash(s1) != ComputeHash(s2) {
		t.Fatalf("hash not slice-order-independent:\n  s1=%s\n  s2=%s", ComputeHash(s1), ComputeHash(s2))
	}
}

func TestSnapshot_Hash_EqualsComputeHash(t *testing.T) {
	t.Parallel()
	s, err := NewSnapshot(validSpec(t))
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	if s.Hash() != ComputeHash(s) {
		t.Fatalf("s.Hash() = %s; ComputeHash(s) = %s", s.Hash(), ComputeHash(s))
	}
}

// ---------------------------------------------------------------------
// DisableUpload + ablation registration — §7(d)
// ---------------------------------------------------------------------

// TestNoCapabilityDiscovery_DefaultIsFalse asserts the package-load
// default of DisableUpload. Every test that flips DisableUpload uses
// t.Cleanup to restore it, so this test is safe to run in any position;
// it serves as a regression guard against a future change that flips the
// default at package init (which would silently disable all capability
// uploads).
func TestNoCapabilityDiscovery_DefaultIsFalse(t *testing.T) {
	if DisableUpload {
		t.Fatalf("DisableUpload should be false at the start of this test; got true")
	}
}

func TestRegisteredOnAblationDefault(t *testing.T) {
	t.Parallel()
	registered := ablation.Default.List()
	for _, f := range registered {
		if f == ablation.NoCapabilityDiscovery {
			return
		}
	}
	t.Fatalf("ablation.Default.List() does not contain NoCapabilityDiscovery: %v", registered)
}

// ---------------------------------------------------------------------
// Three-slave acceptance (spec §6.2)
// ---------------------------------------------------------------------

func TestThreeSlavesProduceDistinctHashes(t *testing.T) {
	t.Parallel()
	mk := func(t *testing.T, name string, b Snapshot) Snapshot {
		t.Helper()
		s, err := NewSnapshot(b)
		if err != nil {
			t.Fatalf("NewSnapshot(%s): %v", name, err)
		}
		return s
	}
	linuxLaptop := mk(t, "linux-laptop", Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		Tools: []ToolVersion{
			{Name: "go", Version: "1.22.0"},
			{Name: "git", Version: "2.40.0"},
			{Name: "node", Version: "20.10.0"},
		},
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
		},
	})
	linuxServer := mk(t, "linux-server", Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkIntranet,
		Tools: []ToolVersion{
			{Name: "go", Version: "1.22.0"},
			{Name: "git", Version: "2.40.0"},
			{Name: "python", Version: "3.12.0"},
			{Name: "docker", Version: "24.0.0"},
			{Name: "kubectl", Version: "1.29.0"},
		},
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
		},
	})
	windowsDesktop := mk(t, "windows-desktop", Snapshot{
		OS: "windows", Arch: "amd64",
		Platform: commandiface.Platform{OS: "windows", Arch: "amd64"},
		Network:  NetworkInternet,
		Tools: []ToolVersion{
			{Name: "go", Version: "1.22.0"},
			{Name: "git", Version: "2.40.0"},
		},
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "powershell", Kind: "powershell", Command: "powershell.exe", Default: true},
			{Skill: "bash", Kind: "bash", Command: "wsl.exe -- bash -lc", Default: false},
		},
	})

	hashes := map[string]string{
		"linux-laptop":    linuxLaptop.Hash(),
		"linux-server":    linuxServer.Hash(),
		"windows-desktop": windowsDesktop.Hash(),
	}
	seen := map[string]string{}
	for name, h := range hashes {
		if prev, ok := seen[h]; ok {
			t.Fatalf("hash collision: %s and %s both have %s", prev, name, h)
		}
		seen[h] = name
	}

	// Stability: each scenario's hash unchanged across a second call.
	for _, snap := range []Snapshot{linuxLaptop, linuxServer, windowsDesktop} {
		if snap.Hash() != snap.Hash() {
			t.Fatal("Hash() not stable on repeat call")
		}
	}
}

// Sanity check that JWT prefix detection actually matches the leading
// "eyJ" + base64-ish payload (regression guard if anyone weakens the
// regex).
func TestRawTokenPattern_EyJRequiresDot(t *testing.T) {
	t.Parallel()
	// "eyJ" by itself (no dot) is NOT a JWT shape — it might be a valid
	// alias if it weren't for the uppercase. To exercise the JWT-shape
	// check distinct from the alias-shape check we keep the test above
	// (RawEyJ) and additionally confirm a bare "eyJabc" with no payload
	// dot is rejected by alias-shape (uppercase) not by raw-token.
	_, err := NewCredentialAlias("eyJabc")
	if !errors.Is(err, ErrAliasInvalidShape) {
		// If a future regex loosens to match prefix-only, this catches it.
		// errors.Is intentionally: ErrLooksLikeRawCredential would be a
		// regression here (false positive on a plausible alias).
		if errors.Is(err, ErrLooksLikeRawCredential) {
			t.Fatalf("NewCredentialAlias(eyJabc): regex too loose, matched as raw token; want ErrAliasInvalidShape")
		}
		t.Fatalf("NewCredentialAlias(eyJabc): want ErrAliasInvalidShape, got %v", err)
	}
}

// §7(a) — raw-token catalogue must be case-insensitive. Stage 3 Codex
// review round 1 caught this: uppercase variants of leaked tokens are
// common in screenshots and log dumps.
func TestCredentialAlias_RejectsRawTokensCaseInsensitively(t *testing.T) {
	t.Parallel()
	cases := []string{
		"SK-ABC123DEF4567XYZ",
		"GHP_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345",
		"XOXB-12345-67890-ABCDEFABCDEF",
		"EyJabcDEFghiJKL.PAYLOAD0123.SIGXYZ7890", // 3-segment uppercase JWT
		"akia0123456789ABCDEF",                   // mixed
	}
	for _, in := range cases {
		_, err := NewCredentialAlias(in)
		if !errors.Is(err, ErrLooksLikeRawCredential) {
			t.Errorf("NewCredentialAlias(%q): want ErrLooksLikeRawCredential, got %v", in, err)
		}
	}
}

// §7(e) — JSONContainsRawToken must catch upper-case raw tokens that the
// canonical JSON could embed (e.g. an MCP description field that pastes
// `SK-...` verbatim).
func TestJSONContainsRawToken_CaseInsensitive(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		[]byte(`{"description":"use SK-ABC123DEF456 to authenticate"}`),
		[]byte(`{"description":"GHP_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"}`),
		[]byte(`{"description":"AWS key AKIA0123456789ABCDEF"}`),
		[]byte(`{"description":"slack XOXB-12345-67890-ABCDEFABCDEF token"}`),
	}
	for _, body := range cases {
		if !JSONContainsRawToken(body) {
			t.Errorf("JSONContainsRawToken(%q) = false; want true (upper-case raw token leaked)", body)
		}
	}
}

// §4 + §7(b) — MCPTools.InputSchema malformed JSON must be rejected at
// construction, not silently swallowed into ComputeHash's error branch
// (which would let two distinct broken snapshots collide on the same
// constant placeholder hash).
func TestNewSnapshot_RejectsInvalidMCPInputSchema(t *testing.T) {
	t.Parallel()
	s := validSpec(t)
	s.MCPTools = []MCPToolDescriptor{{
		Server:      "srv",
		Name:        "tool",
		InputSchema: []byte("{not json"), // malformed
	}}
	_, err := NewSnapshot(s)
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(invalid InputSchema): want ErrSnapshotInvalid, got %v", err)
	}
}

// §4 — canonical sort must be TOTAL on CommandInterfaces. Two interfaces
// that share Skill+Kind+Command but differ in Default must hash the same
// regardless of original slice order. (Stage 3 Codex review round 1.)
func TestComputeHash_TotalSortOnCommandInterfacesNearDuplicate(t *testing.T) {
	t.Parallel()
	a, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: false},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot a: %v", err)
	}
	b, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		CommandInterfaces: []commandiface.CommandInterface{
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: false},
			{Skill: "bash", Kind: "bash", Command: "/bin/bash", Default: true},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot b: %v", err)
	}
	if ComputeHash(a) != ComputeHash(b) {
		t.Fatalf("hash differs on CommandInterface near-duplicate permutation:\n  a=%s\n  b=%s", ComputeHash(a), ComputeHash(b))
	}
}

// §4 — canonical sort must be TOTAL on MCPTools. Two descriptors sharing
// Server+Name but differing in Description must hash the same regardless
// of original slice order.
func TestComputeHash_TotalSortOnMCPToolsNearDuplicate(t *testing.T) {
	t.Parallel()
	a, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		MCPTools: []MCPToolDescriptor{
			{Server: "srv", Name: "tool", Description: "first variant"},
			{Server: "srv", Name: "tool", Description: "second variant"},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot a: %v", err)
	}
	b, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		MCPTools: []MCPToolDescriptor{
			{Server: "srv", Name: "tool", Description: "second variant"},
			{Server: "srv", Name: "tool", Description: "first variant"},
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot b: %v", err)
	}
	if ComputeHash(a) != ComputeHash(b) {
		t.Fatalf("hash differs on MCPTool near-duplicate permutation:\n  a=%s\n  b=%s", ComputeHash(a), ComputeHash(b))
	}
}

// §7(a) Phase-1 catalogue additions: github_pat_…, AIza…, sk-ant-…,
// xoxe-. (Reviewer round 4 P2 — catalogue gaps.)
func TestCredentialAlias_RejectsExpandedTokenCatalogue(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"sk-ant-api03":        "sk-ant-api03-ABCDEFGHIJKLMNOP1234567890",
		"github fine-grained": "github_pat_11ABCDEFG0123456789012345_abcdefghijklmnopqrstuvwxyz",
		"google api key":      "AIzaSyA-1234567890abcdefghijklmnopqrstuvwx",
		"slack refresh":       "xoxe-12345-67890-abcdefABCDEF",
	}
	for label, in := range cases {
		_, err := NewCredentialAlias(in)
		if !errors.Is(err, ErrLooksLikeRawCredential) {
			t.Errorf("NewCredentialAlias(%s = %q): want ErrLooksLikeRawCredential, got %v", label, in, err)
		}
	}
}

// Same expanded catalogue is enforced at pre-write scan time.
func TestJSONContainsRawToken_ExpandedCatalogue(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		[]byte(`{"description":"use github_pat_11ABCDEFG0123456789012345_abcdefghij to push"}`),
		[]byte(`{"description":"google key AIzaSyA-1234567890abcdefghijklmnopqrstuvwx"}`),
		[]byte(`{"description":"sk-ant-api03-ABCDEFGHIJKLMNOP1234567890 for claude"}`),
		[]byte(`{"description":"slack xoxe-12345-67890-abcdef refresh"}`),
	}
	for _, body := range cases {
		if !JSONContainsRawToken(body) {
			t.Errorf("JSONContainsRawToken(%q) = false; want true (expanded catalogue gap)", body)
		}
	}
}

// §7(a) spec wording for Slack tokens is `xox[baprs]-[A-Za-z0-9-]+`
// (1+ chars after the prefix). A regression that tightened that to 10+
// would silently let short Slack-shaped values through; this test
// regression-guards the spec-faithful regex.
func TestCredentialAlias_RejectsShortXoxSlackToken(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"xoxb-a", "xoxp-12345"} {
		_, err := NewCredentialAlias(in)
		if !errors.Is(err, ErrLooksLikeRawCredential) {
			t.Errorf("NewCredentialAlias(%q): want ErrLooksLikeRawCredential, got %v", in, err)
		}
	}
}

// Belt-and-braces: confirm Snapshot zero value is not silently accepted.
func TestNewSnapshot_RejectsZeroValue(t *testing.T) {
	t.Parallel()
	_, err := NewSnapshot(Snapshot{})
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("NewSnapshot(Snapshot{}): want ErrSnapshotInvalid, got %v", err)
	}
}

// Strings helper to make negative-test messages readable.
var _ = strings.HasPrefix

// ---------------------------------------------------------------------
// Round-5 fresh-reviewer audit fixes
// ---------------------------------------------------------------------

// §4 — MCPTools.InputSchema key-order permutations must hash IDENTICALLY,
// otherwise dedup at the observer breaks silently: two semantically-
// identical schemas would land as two distinct rows. NewSnapshot must
// canonicalise the InputSchema at construction time.
func TestNewSnapshot_CanonicalisesInputSchemaKeyOrder(t *testing.T) {
	t.Parallel()
	mk := func(schema string) Snapshot {
		s, err := NewSnapshot(Snapshot{
			OS: "linux", Arch: "amd64",
			Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
			Network:  NetworkInternet,
			MCPTools: []MCPToolDescriptor{{
				Server: "srv", Name: "tool",
				InputSchema: json.RawMessage(schema),
			}},
		})
		if err != nil {
			t.Fatalf("NewSnapshot(%s): %v", schema, err)
		}
		return s
	}
	a := ComputeHash(mk(`{"a":1,"b":2}`))
	b := ComputeHash(mk(`{"b":2,"a":1}`))
	if a != b {
		t.Fatalf("InputSchema key-order defeats hash dedup:\n  a=%s\n  b=%s", a, b)
	}
}

// §4 — whitespace inside InputSchema must not change the hash either
// (the canonicaliser strips it via unmarshal → remarshal round-trip).
func TestNewSnapshot_CanonicalisesInputSchemaWhitespace(t *testing.T) {
	t.Parallel()
	mk := func(schema string) Snapshot {
		s, err := NewSnapshot(Snapshot{
			OS: "linux", Arch: "amd64",
			Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
			Network:  NetworkInternet,
			MCPTools: []MCPToolDescriptor{{
				Server: "srv", Name: "tool",
				InputSchema: json.RawMessage(schema),
			}},
		})
		if err != nil {
			t.Fatalf("NewSnapshot: %v", err)
		}
		return s
	}
	tight := ComputeHash(mk(`{"a":1,"b":[2,3]}`))
	loose := ComputeHash(mk("{\n  \"a\" : 1 ,\n  \"b\" : [ 2 , 3 ]\n}"))
	if tight != loose {
		t.Fatalf("InputSchema whitespace changes hash:\n  tight=%s\n  loose=%s", tight, loose)
	}
}

// §4 — canonicaliser must preserve number precision (json.Number),
// not silently float64-round large integers into imprecision.
func TestNewSnapshot_CanonicalisesInputSchemaPreservesNumbers(t *testing.T) {
	t.Parallel()
	// 2^53+1 is the smallest integer that float64 cannot represent
	// exactly. If the canonicaliser round-trips through float64 it will
	// silently rewrite 9007199254740993 → 9007199254740992.
	big := `{"n":9007199254740993}`
	s, err := NewSnapshot(Snapshot{
		OS: "linux", Arch: "amd64",
		Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
		Network:  NetworkInternet,
		MCPTools: []MCPToolDescriptor{{
			Server: "srv", Name: "tool",
			InputSchema: json.RawMessage(big),
		}},
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	if !strings.Contains(string(s.MCPTools[0].InputSchema), "9007199254740993") {
		t.Fatalf("canonicalised InputSchema lost integer precision: %s", s.MCPTools[0].InputSchema)
	}
}

// §7(d) — IsUploadDisabled must return the atomic value, not the raw
// bool, so concurrent readers race-free. Direct test: two goroutines
// hammer read + SetDisableUpload write; -race must not fire.
func TestIsUploadDisabled_RaceFree(t *testing.T) {
	// This test flips DisableUpload; serial to avoid stepping on other
	// tests. Restore.
	t.Cleanup(func() { SetDisableUpload(false) })

	var reads atomic.Int64
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			_ = IsUploadDisabled()
			reads.Add(1)
		}
		close(done)
	}()
	for i := 0; i < 10000; i++ {
		SetDisableUpload(i%2 == 0)
	}
	<-done
	if reads.Load() == 0 {
		t.Fatal("no reads happened; test setup is broken")
	}
	// If -race fires, the test binary exits non-zero and this line
	// never returns "pass". Explicit success sentinel:
	t.Logf("completed %d atomic-read/write pairs with no race", reads.Load())
}

// §7(d) — SyncDisableUpload mirrors the raw bool (as the ablation
// registry writes it) into the atomic. Simulates the Phase-2 CLI
// binder flow.
func TestSyncDisableUpload_MirrorsRawBool(t *testing.T) {
	t.Cleanup(func() { SetDisableUpload(false) })

	// Simulate ablation.Default.SetByName writing directly to
	// &DisableUpload (bypassing SetDisableUpload).
	DisableUpload = true
	SyncDisableUpload()
	if !IsUploadDisabled() {
		t.Fatal("SyncDisableUpload did not mirror raw true into atomic")
	}

	DisableUpload = false
	SyncDisableUpload()
	if IsUploadDisabled() {
		t.Fatal("SyncDisableUpload did not mirror raw false into atomic")
	}
}

// §4 — MCPTools.InputSchema numeric-literal variants must hash
// IDENTICALLY when semantically equal, otherwise the same
// dedup-defeat failure round-5 tried to fix reappears via number-shape
// divergence (round-6 audit).
func TestNewSnapshot_CanonicalisesInputSchemaNumberLiteralShapes(t *testing.T) {
	t.Parallel()
	mk := func(schema string) Snapshot {
		s, err := NewSnapshot(Snapshot{
			OS: "linux", Arch: "amd64",
			Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
			Network:  NetworkInternet,
			MCPTools: []MCPToolDescriptor{{
				Server: "srv", Name: "tool",
				InputSchema: json.RawMessage(schema),
			}},
		})
		if err != nil {
			t.Fatalf("NewSnapshot(%s): %v", schema, err)
		}
		return s
	}
	pairs := [][2]string{
		{`{"n":1e10}`, `{"n":1E10}`},          // exponent case
		{`{"n":1.0}`, `{"n":1}`},              // trailing zero after point
		{`{"n":1e+10}`, `{"n":1e10}`},         // explicit + in exponent
		{`{"n":0e0}`, `{"n":0}`},              // zero-exponent zero
		{`{"n":1.500}`, `{"n":1.5}`},          // fractional trailing zeros
		{`{"n":100}`, `{"n":1e2}`},            // integer written as scientific
		{`{"n":-0}`, `{"n":0}`},               // signed zero
		{`{"a":[1.0,2.0]}`, `{"a":[1,2]}`},    // nested array numbers
		{`{"x":{"y":1.00}}`, `{"x":{"y":1}}`}, // nested object numbers
	}
	for _, p := range pairs {
		a, b := ComputeHash(mk(p[0])), ComputeHash(mk(p[1]))
		if a != b {
			t.Errorf("hash diverges for semantically-equal numbers %q vs %q:\n  a=%s\n  b=%s", p[0], p[1], a, b)
		}
	}
}

// §4 — the canonicaliser must also preserve DISTINCT numeric values
// (a normalisation that collapses everything to the same string would
// pass the equality test but destroy the hash's rollback-detection
// guarantee). Test the negative side.
func TestNewSnapshot_InputSchemaDistinctNumbersHashDistinctly(t *testing.T) {
	t.Parallel()
	mk := func(schema string) Snapshot {
		s, err := NewSnapshot(Snapshot{
			OS: "linux", Arch: "amd64",
			Platform: commandiface.Platform{OS: "linux", Arch: "amd64"},
			Network:  NetworkInternet,
			MCPTools: []MCPToolDescriptor{{
				Server: "srv", Name: "tool",
				InputSchema: json.RawMessage(schema),
			}},
		})
		if err != nil {
			t.Fatalf("NewSnapshot: %v", err)
		}
		return s
	}
	if ComputeHash(mk(`{"n":1}`)) == ComputeHash(mk(`{"n":2}`)) {
		t.Fatal("1 and 2 hashed identically — canonicaliser collapsed distinct values")
	}
	if ComputeHash(mk(`{"n":1.5}`)) == ComputeHash(mk(`{"n":1.6}`)) {
		t.Fatal("1.5 and 1.6 hashed identically")
	}
	// Big integer past float64 precision still distinguished.
	if ComputeHash(mk(`{"n":9007199254740993}`)) == ComputeHash(mk(`{"n":9007199254740992}`)) {
		t.Fatal("2^53 and 2^53+1 hashed identically — big.Rat lost precision")
	}
}

// §7(a) — eyJ regex must require a second base64 segment, otherwise it
// false-positives on innocuous "identifier.name" phrasings in tool
// descriptions.
func TestJSONContainsRawToken_EyJRequiresPayloadSegment(t *testing.T) {
	t.Parallel()
	// A 3-segment JWT (real): must match.
	realJWT := []byte(`{"description":"jwt eyJhbGciOiJIUzI1NiJ9.ZXlKMGVYQWlPaUpLVjFRaUxDSmhiR2NpT2lKSVV6STFOaUo5.sig"}`)
	if !JSONContainsRawToken(realJWT) {
		t.Errorf("real 3-segment JWT not caught: %s", realJWT)
	}
	// A 2-segment JWT stub (real, truncated): must match.
	twoSeg := []byte(`{"description":"eyJhbGciOiJIUzI1NiJ9.ZXlKMGVYQWlPaUpLVjFRaUxDSmhiR2NpT2lKSVV6STFOaUo5"}`)
	if !JSONContainsRawToken(twoSeg) {
		t.Errorf("2-segment JWT not caught: %s", twoSeg)
	}
	// An innocuous identifier.name (single segment): must NOT match.
	innocent := []byte(`{"description":"See eyJconfigDefaults. for defaults"}`)
	if JSONContainsRawToken(innocent) {
		t.Errorf("false positive on innocuous identifier.name: %s", innocent)
	}
	// A shorter phrase still starting with eyJ: must NOT match.
	shorter := []byte(`{"description":"eyJabc"}`)
	if JSONContainsRawToken(shorter) {
		t.Errorf("false positive on short eyJ identifier: %s", shorter)
	}
}
