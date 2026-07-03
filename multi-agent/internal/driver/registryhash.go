package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
)

// Package driver's registry-hash publisher — see spec
// docs/specs/wt2-driver-promotion-chain-B6.spec.md §2.5 + §3.3 + §7 (d) +
// §7 (j). Populates the value that eval-runner reads into
// evalrun.Schema.DynamicMCPRegistryHash (D1 field 14).
//
// Design choices:
//   - Hash is content-addressed over (name, descriptor_hash) pairs. Two
//     registrations with byte-identical specs yield the same descHash and
//     thus the same registry hash.
//   - Empty registry hashes to sha256 of the empty byte string. This is
//     a fixed 64-hex constant, NOT the empty string — see (d) rationale
//     in the spec.
//   - The driver keeps an IN-PROCESS per-slave view of what it has
//     caused to be registered / unregistered. It does NOT remotely
//     read a slave's dynamic_mcp.yaml on every hash call. See §7 (j)
//     for the trade-off.

// EmptyBytesSHA256Hex is the pre-computed sha256 of the empty byte
// string, hex-encoded lowercase. This is the CANONICAL definition —
// callers in this repo that need the constant (register_mcp_tool.go,
// unregister_mcp_tool.go, promotion_pipeline_tool.go,
// internal/promotionpipeline) all import it from here rather than
// re-declaring, so content-addressed constants have one source of
// truth. See PR #71 review P1-B6-C.
const EmptyBytesSHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// registryHashOfEmpty is the file-local alias for backwards
// readability of the older docs; equal to EmptyBytesSHA256Hex.
const registryHashOfEmpty = EmptyBytesSHA256Hex

// Field / record separators for the deterministic input encoding. §2.5.
const (
	sepUS = "\x1f" // Unit Separator (US)
	sepRS = "\x1e" // Record Separator (RS)
)

var (
	registryHashMu   sync.RWMutex
	lastRegistryHash string // "" means "never set"; readers translate to registryHashOfEmpty

	// slaveRegistryView is the per-slave in-process view: slave_agent_id →
	// mcp_name → spec_hash. See §3.4 step 2. This is the ONLY source used
	// for LastRegistryHash — no remote reads (§7 (j)).
	slaveRegistryMu   sync.RWMutex
	slaveRegistryView = map[string]map[string]string{}

	// publisherMu serialises the entire noteRegister → snapshotAll →
	// ComputeRegistryHash → SetLastRegistryHash chain. Without it,
	// two concurrent register calls can interleave such that the
	// LATER note completes its SetLast BEFORE the EARLIER note
	// finishes, leaving LastRegistryHash lagging the actual view. The
	// individual read/write locks on slaveRegistryMu and
	// registryHashMu don't compose across the whole publish sequence,
	// which is why we need a coarser publisher-scope lock. See PR #71
	// fresh-review P1-B6-B.
	publisherMu sync.Mutex
)

// LastRegistryHash returns the last-written registry snapshot hash.
// Never returns "" — a fresh process returns the sha256 hex of the
// empty byte string. See §7 (d).
func LastRegistryHash() string {
	registryHashMu.RLock()
	defer registryHashMu.RUnlock()
	if lastRegistryHash == "" {
		return registryHashOfEmpty
	}
	return lastRegistryHash
}

// SetLastRegistryHash publishes h as the current registry-snapshot
// hash. Callers pass the value returned by ComputeRegistryHash. h MUST
// be 64 lowercase hex chars; SetLastRegistryHash does NOT validate
// (the invariant is enforced at the writer boundary and at the DB
// CHECK — validating a third time here would be redundant).
func SetLastRegistryHash(h string) {
	registryHashMu.Lock()
	defer registryHashMu.Unlock()
	lastRegistryHash = h
}

// ComputeRegistryHash returns hex(sha256(concat(
//
//	for name in sort(names): name || US || descHash(name) || RS
//
// ))). names may be nil/empty → returns the empty-bytes sha256 constant.
// Sort makes the hash order-independent (§7 (d)). descHash is the
// caller-supplied lookup function: for the driver-tool wiring this
// returns the buildspec sha256 of that MCP.
func ComputeRegistryHash(names []string, descHash func(string) string) string {
	if len(names) == 0 {
		return registryHashOfEmpty
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	h := sha256.New()
	for _, name := range sorted {
		h.Write([]byte(name))
		h.Write([]byte(sepUS))
		h.Write([]byte(descHash(name)))
		h.Write([]byte(sepRS))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// noteRegister records that the driver caused (or observed) slave to
// have mcp `name` with the given specHash. Overwrites any prior entry
// for (slave, name) — a re-register with a new spec is a legal update.
func noteRegister(slave, name, specHash string) {
	slaveRegistryMu.Lock()
	defer slaveRegistryMu.Unlock()
	m, ok := slaveRegistryView[slave]
	if !ok {
		m = map[string]string{}
		slaveRegistryView[slave] = m
	}
	m[name] = specHash
}

// noteUnregister removes (slave, name) from the view. Safe if the
// entry doesn't exist.
func noteUnregister(slave, name string) {
	slaveRegistryMu.Lock()
	defer slaveRegistryMu.Unlock()
	if m, ok := slaveRegistryView[slave]; ok {
		delete(m, name)
	}
}

// snapshotAll returns the (names, descHash) pair suitable for
// ComputeRegistryHash. Names are sorted by (slave_agent_id, mcp_name)
// so the hash is stable across concurrent multi-slave callers. The
// name element carries slave-scope: `<slave_id>:<mcp_name>` so the
// same mcp name registered on two slaves produces distinct entries
// (otherwise the hash would collapse two independent registrations).
func snapshotAll() ([]string, func(string) string) {
	slaveRegistryMu.RLock()
	defer slaveRegistryMu.RUnlock()
	type entry struct {
		key, spec string
	}
	var entries []entry
	for slave, mcps := range slaveRegistryView {
		for name, spec := range mcps {
			entries = append(entries, entry{key: slave + ":" + name, spec: spec})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	names := make([]string, len(entries))
	specByKey := make(map[string]string, len(entries))
	for i, e := range entries {
		names[i] = e.key
		specByKey[e.key] = e.spec
	}
	return names, func(k string) string { return specByKey[k] }
}

// resetRegistryForTest wipes the in-process view. Test-only.
func resetRegistryForTest() {
	registryHashMu.Lock()
	lastRegistryHash = ""
	registryHashMu.Unlock()
	slaveRegistryMu.Lock()
	slaveRegistryView = map[string]map[string]string{}
	slaveRegistryMu.Unlock()
}

// PublishRegisterAndCompute updates the per-slave view for a register
// event (slave, name, specHash) then computes + publishes the new
// LastRegistryHash. Returns the new hash. Called from
// register_slave_mcp after the slave task completes; extracted so B2
// pipeline callers can reuse the same code path.
//
// The publisher-scope publisherMu serialises the full sequence so
// two concurrent register calls cannot interleave and leave
// LastRegistryHash lagging the view — see PR #71 review P1-B6-B.
func PublishRegisterAndCompute(slave, name, specHash string) string {
	publisherMu.Lock()
	defer publisherMu.Unlock()
	noteRegister(slave, name, specHash)
	names, desc := snapshotAll()
	h := ComputeRegistryHash(names, desc)
	SetLastRegistryHash(h)
	return h
}

// PublishUnregisterAndCompute is the symmetric path for
// unregister_slave_mcp. Returns the post-unregister hash. Shares
// publisherMu with PublishRegisterAndCompute — see that function's
// doc comment for rationale.
func PublishUnregisterAndCompute(slave, name string) string {
	publisherMu.Lock()
	defer publisherMu.Unlock()
	noteUnregister(slave, name)
	names, desc := snapshotAll()
	h := ComputeRegistryHash(names, desc)
	SetLastRegistryHash(h)
	return h
}

