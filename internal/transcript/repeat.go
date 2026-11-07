package transcript

import (
	"regexp"
	"strings"
)

var (
	// hex32 matches a 32+ hex-character id anywhere in a command.
	hex32 = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	// hexToken matches a whole token of 8+ hex characters.
	hexToken = regexp.MustCompile(`^[0-9a-fA-F]{8,}$`)
)

// NormalizeCommand normalises a Bash command so repeated commands are
// recognised as the same command:
//   - collapse runs of whitespace to single spaces, trim ends;
//   - replace absolute paths under the session's project root with <root>;
//   - replace long hex ids with <id>;
//   - drop arguments after "--" only if none of them looks like a file
//     path (contains "/" or starts with "." or "~").
func NormalizeCommand(cmd, root string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	// Drop arguments after "--" when they are not file paths.
	for i, f := range fields {
		if f != "--" {
			continue
		}
		rest := fields[i+1:]
		isPath := false
		for _, r := range rest {
			if strings.Contains(r, "/") || strings.HasPrefix(r, ".") || strings.HasPrefix(r, "~") {
				isPath = true
				break
			}
		}
		if !isPath {
			fields = fields[:i]
		}
		break
	}
	out := strings.Join(fields, " ")
	if root != "" {
		out = strings.ReplaceAll(out, root, "<root>")
		out = strings.ReplaceAll(out, strings.TrimSuffix(root, "/")+"/", "<root>/")
	}
	out = hex32.ReplaceAllString(out, "<id>")
	parts := strings.Fields(out)
	for i, p := range parts {
		if hexToken.MatchString(p) && strings.ContainsAny(p, "abcdefABCDEF") && strings.ContainsAny(p, "0123456789") {
			parts[i] = "<id>"
		}
	}
	return strings.Join(parts, " ")
}
