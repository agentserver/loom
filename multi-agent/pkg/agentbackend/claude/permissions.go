package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// Store manages the Claude permissions file (.claude/settings.local.json)
// inside a given workdir.
type Store struct {
	workdir string
}

// State is the native claudeperm state (path + allow/deny lists).
type State struct {
	Path  string   `json:"path"`
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// Patch is the native claudeperm patch shape.
type Patch struct {
	AllowPresets []string `json:"allow_presets"`
	AllowAdd     []string `json:"allow_add"`
	AllowRemove  []string `json:"allow_remove"`
	DenyAdd      []string `json:"deny_add"`
	DenyRemove   []string `json:"deny_remove"`
}

type permissionsSettings struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// NewStore creates a Store rooted at workdir.
func NewStore(workdir string) *Store {
	return &Store{workdir: workdir}
}

// Path returns the absolute path of the permissions file.
func (s *Store) Path() string {
	return filepath.Join(s.workdir, ".claude", "settings.local.json")
}

// Read reads the current permissions state.
func (s *Store) Read() (State, error) {
	doc, err := s.readDoc()
	if err != nil {
		return State{}, err
	}
	perm, err := permissionsFromDoc(doc)
	if err != nil {
		return State{}, err
	}
	return State{Path: s.Path(), Allow: sortedUnique(perm.Allow), Deny: sortedUnique(perm.Deny)}, nil
}

// PatchNative applies a native Patch and returns the updated State.
// (Renamed from the original Patch method to avoid signature clash with the
// agentbackend.PermissionsStore adapter method below.)
func (s *Store) PatchNative(p Patch) (State, error) {
	doc, err := s.readDoc()
	if err != nil {
		return State{}, err
	}
	perm, err := permissionsFromDoc(doc)
	if err != nil {
		return State{}, err
	}
	presetAllow, err := ExpandPresets(p.AllowPresets)
	if err != nil {
		return State{}, err
	}
	allow := stringSet(perm.Allow)
	deny := stringSet(perm.Deny)
	addAll(allow, presetAllow)
	addAll(allow, p.AllowAdd)
	removeAll(allow, p.AllowRemove)
	addAll(deny, p.DenyAdd)
	removeAll(deny, p.DenyRemove)

	perm.Allow = setStrings(allow)
	perm.Deny = setStrings(deny)
	rawPerm, err := json.Marshal(perm)
	if err != nil {
		return State{}, err
	}
	doc["permissions"] = rawPerm
	if err := s.writeDoc(doc); err != nil {
		return State{}, err
	}
	return State{Path: s.Path(), Allow: perm.Allow, Deny: perm.Deny}, nil
}

// --- agentbackend.PermissionsStore adapter ---

// Get implements agentbackend.PermissionsStore.
func (s *Store) Get(_ context.Context) (agentbackend.State, error) {
	st, err := s.Read()
	if err != nil {
		return agentbackend.State{}, err
	}
	return agentbackend.State{
		Backend: agentbackend.KindClaude,
		Path:    st.Path,
		Allow:   st.Allow,
		Deny:    st.Deny,
	}, nil
}

// Patch implements agentbackend.PermissionsStore.
func (s *Store) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	if p.Mode != "" {
		return agentbackend.State{}, fmt.Errorf("claude backend does not accept Patch.Mode (codex-only)")
	}
	native := Patch{
		AllowPresets: p.Presets,
		AllowAdd:     p.AllowAdd,
		AllowRemove:  p.AllowRemove,
		DenyAdd:      p.DenyAdd,
		DenyRemove:   p.DenyRemove,
	}
	st, err := s.PatchNative(native)
	if err != nil {
		return agentbackend.State{}, err
	}
	return agentbackend.State{
		Backend: agentbackend.KindClaude,
		Path:    st.Path,
		Allow:   st.Allow,
		Deny:    st.Deny,
	}, nil
}

// --- internal helpers ---

func (s *Store) readDoc() (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, nil
}

func (s *Store) writeDoc(doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func permissionsFromDoc(doc map[string]json.RawMessage) (permissionsSettings, error) {
	raw := doc["permissions"]
	if len(raw) == 0 {
		return permissionsSettings{}, nil
	}
	var perm permissionsSettings
	if err := json.Unmarshal(raw, &perm); err != nil {
		return permissionsSettings{}, err
	}
	perm.Allow = sortedUnique(perm.Allow)
	perm.Deny = sortedUnique(perm.Deny)
	return perm, nil
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	addAll(out, values)
	return out
}

func addAll(set map[string]struct{}, values []string) {
	for _, value := range values {
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
}

func removeAll(set map[string]struct{}, values []string) {
	for _, value := range values {
		delete(set, value)
	}
}

func setStrings(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedUnique(values []string) []string {
	return setStrings(stringSet(values))
}
