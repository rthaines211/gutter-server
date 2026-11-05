package creators

import "strings"

// SplitRoles splits a ComicVine person_credits role string ("penciler,
// inker") into its individual, trimmed roles.
func SplitRoles(role string) []string {
	if role == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(role, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// RoleGroup buckets a single (already-split) role into one of the three
// chip groups the creator page binds against, or "" when it doesn't fit any
// of them (e.g. "letterer", "editor", "plotter", "cover artist"). These sets
// must stay exactly in sync with the UI's chip-group bindings:
// Writer={writer}; Art={penciler, penciller, inker, colorist, artist};
// Covers={cover}.
func RoleGroup(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "writer":
		return "writer"
	case "penciler", "penciller", "inker", "colorist", "artist":
		return "art"
	case "cover":
		return "covers"
	default:
		return ""
	}
}
