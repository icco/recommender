package sanitize

import "strings"

// ForLog strips control characters that could fake extra log fields or lines.
func ForLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\t' {
			return ' '
		}
		return r
	}, s)
}
