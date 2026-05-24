package claude

import "fmt"

// ExpandPresets converts symbolic preset names into concrete permission strings.
func ExpandPresets(presets []string) ([]string, error) {
	var out []string
	for _, preset := range presets {
		switch preset {
		case "python":
			out = append(out, "Bash(python *)", "Bash(python3 *)")
		case "pip":
			out = append(out, "Bash(pip *)", "Bash(pip3 *)", "Bash(python -m pip *)", "Bash(python3 -m pip *)")
		case "curl":
			out = append(out, "Bash(curl *)")
		case "file_read":
			out = append(out, "Read")
		case "file_write":
			out = append(out, "Write", "Edit", "Read")
		default:
			return nil, fmt.Errorf("unknown permission preset %q", preset)
		}
	}
	return sortedUnique(out), nil
}
