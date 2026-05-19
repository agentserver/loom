package executor

import (
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/internal/capability"
	"gopkg.in/yaml.v3"
)

// DynamicEntry represents a single MCP server entry in dynamic_mcp.yaml.
type DynamicEntry struct {
	Name      string                         `yaml:"-"`
	Transport string                         `yaml:"transport"`
	Command   string                         `yaml:"command"`
	Args      []string                       `yaml:"args"`
	Version   int                            `yaml:"version"`
	CreatedAt string                         `yaml:"created_at"`
	SpecHash  string                         `yaml:"spec_hash"`
	Tools     []capability.MCPToolDescriptor `yaml:"tools,omitempty"`
}

func (d *DynamicEntry) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Transport string    `yaml:"transport"`
		Command   string    `yaml:"command"`
		Args      []string  `yaml:"args"`
		Version   int       `yaml:"version"`
		CreatedAt string    `yaml:"created_at"`
		SpecHash  string    `yaml:"spec_hash"`
		Tools     yaml.Node `yaml:"tools"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	d.Transport = raw.Transport
	d.Command = raw.Command
	d.Args = raw.Args
	d.Version = raw.Version
	d.CreatedAt = raw.CreatedAt
	d.SpecHash = raw.SpecHash
	if raw.Tools.Kind == 0 {
		return nil
	}
	var descriptors []capability.MCPToolDescriptor
	if err := raw.Tools.Decode(&descriptors); err == nil {
		d.Tools = descriptors
		return nil
	}
	var names []string
	if err := raw.Tools.Decode(&names); err != nil {
		return err
	}
	d.Tools = make([]capability.MCPToolDescriptor, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		d.Tools = append(d.Tools, capability.MCPToolDescriptor{Name: name})
	}
	return nil
}

// DynamicFile is the top-level structure of dynamic_mcp.yaml.
type DynamicFile struct {
	Servers map[string]DynamicEntry `yaml:"servers"`
}

// DynamicYAMLPath returns the path to dynamic_mcp.yaml within workDir.
func DynamicYAMLPath(workDir string) string {
	return filepath.Join(workDir, "dynamic_mcp.yaml")
}

// ReadDynamicYAML reads and parses dynamic_mcp.yaml at path.
// If the file does not exist, an empty DynamicFile is returned.
func ReadDynamicYAML(path string) (DynamicFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DynamicFile{Servers: map[string]DynamicEntry{}}, nil
		}
		return DynamicFile{}, err
	}
	var df DynamicFile
	if err := yaml.Unmarshal(b, &df); err != nil {
		return DynamicFile{}, err
	}
	if df.Servers == nil {
		df.Servers = map[string]DynamicEntry{}
	}
	for name, entry := range df.Servers {
		entry.Name = name
		entry.Tools = capability.WithServer(name, entry.Tools)
		df.Servers[name] = entry
	}
	return df, nil
}

// LookupDynamicEntry looks up a named entry in dynamic_mcp.yaml at path.
func LookupDynamicEntry(path, name string) (DynamicEntry, bool) {
	df, err := ReadDynamicYAML(path)
	if err != nil {
		return DynamicEntry{}, false
	}
	d, ok := df.Servers[name]
	return d, ok
}

// UpsertDynamicYAML writes or updates entry in dynamic_mcp.yaml at path,
// using an atomic rename to avoid partial writes.
func UpsertDynamicYAML(path string, entry DynamicEntry) error {
	df, err := ReadDynamicYAML(path)
	if err != nil {
		return err
	}
	df.Servers[entry.Name] = entry
	out, err := yaml.Marshal(df)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
