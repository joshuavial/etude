package runmanifest

import "testing"

func TestIsValidIdentifier(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"a", true},
		{"abc", true},
		{"ABC", true},
		{"abc123", true},
		{"abc-def", true},
		{"abc_def", true},
		{"abc.def", true},
		{"run.1", true},
		{"20260522-run.1", true},
		{"bad/value", false},
		{"bad value", false},
		{"bad@value", false},
		{"bad!value", false},
		// charset allows dots in any position — extra rules are IsValidRunID's job
		{".", true},
		{"..", true},
		{".hidden", true},
		{"run.lock", true},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := IsValidIdentifier(tc.value); got != tc.want {
				t.Errorf("IsValidIdentifier(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestIsValidRunID(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		// empty — fails IsValidIdentifier
		{"", false},
		// valid run ids
		{"run-1", true},
		{"myrun", true},
		{"20260522-run.1", true},
		{"abc_def", true},
		{"a.b", true},
		{"run.1", true},
		// bad charset — fails IsValidIdentifier
		{"bad/id", false},
		{"bad id", false},
		// leading dot
		{".hidden", false},
		// trailing dot
		{"myrun.", false},
		// double dot
		{"..", false},
		{"run..1", false},
		// all dots
		{".", false},
		{"...", false},
		// .lock suffix
		{"x.lock", false},
		{"run.lock", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			if got := IsValidRunID(tc.value); got != tc.want {
				t.Errorf("IsValidRunID(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
