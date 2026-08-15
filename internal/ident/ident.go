// Package ident defines the shared Etude identifier character policy.
package ident

// IsValid reports whether value is a non-empty string using only the
// [A-Za-z0-9_.-] character set.
func IsValid(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
