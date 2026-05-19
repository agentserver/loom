package claudeperm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadMissingReturnsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Allow) != 0 || len(got.Deny) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestStorePatchExpandsPresetsSortsAndIsIdempotent(t *testing.T) {
	store := NewStore(t.TempDir())
	patch := Patch{
		AllowPresets: []string{"file_write", "curl", "python", "pip"},
		AllowAdd:     []string{"Bash(python3 *)"},
		DenyAdd:      []string{"Bash(rm *)"},
	}
	first, err := store.Patch(patch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Patch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(first.Allow, second.Allow) || !equal(first.Deny, second.Deny) {
		t.Fatalf("not idempotent: first=%+v second=%+v", first, second)
	}
	wantAllow := []string{
		"Bash(curl *)",
		"Bash(pip *)",
		"Bash(pip3 *)",
		"Bash(python *)",
		"Bash(python -m pip *)",
		"Bash(python3 *)",
		"Bash(python3 -m pip *)",
		"Edit",
		"Read",
		"Write",
	}
	if !equal(first.Allow, wantAllow) {
		t.Fatalf("allow=%q want=%q", first.Allow, wantAllow)
	}
	if !equal(first.Deny, []string{"Bash(rm *)"}) {
		t.Fatalf("deny=%q", first.Deny)
	}
}

func TestStorePreservesUnknownSettingsFields(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","permissions":{"allow":["Read"],"deny":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(workdir)
	if _, err := store.Patch(Patch{AllowAdd: []string{"Write"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["theme"]) != `"dark"` {
		t.Fatalf("theme not preserved: %s", data)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
