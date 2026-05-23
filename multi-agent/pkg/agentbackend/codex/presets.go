package codex

// presetToMode picks the strongest mode any of the named presets implies.
// "ask" < "workspace-write" < "full-access".
func presetToMode(presets []string, current string) (mode string, ok bool) {
	rank := map[string]int{"ask": 0, "workspace-write": 1, "full-access": 2}
	cur := rank[current]
	if current == "" {
		cur = 0
	}
	best := cur
	for _, p := range presets {
		switch p {
		case "file_write", "curl", "python":
			if rank["workspace-write"] > best {
				best = rank["workspace-write"]
			}
		case "full_access":
			if rank["full-access"] > best {
				best = rank["full-access"]
			}
		default:
			// unknown preset — caller will warn
		}
	}
	if best == cur {
		if current == "" {
			return "ask", true
		}
		return current, true
	}
	for name, r := range rank {
		if r == best {
			return name, true
		}
	}
	return "ask", false
}
