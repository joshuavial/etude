package ident

import "testing"

func TestIsValid(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "empty", value: "", want: false},
		{name: "lowercase", value: "abc", want: true},
		{name: "uppercase", value: "ABC", want: true},
		{name: "digits", value: "123", want: true},
		{name: "all punctuation", value: "a_b-c.d", want: true},
		{name: "dot only", value: ".", want: true},
		{name: "unicode", value: "café", want: false},
		{name: "slash", value: "a/b", want: false},
		{name: "space", value: "a b", want: false},
		{name: "other punctuation", value: "a@b", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValid(tc.value); got != tc.want {
				t.Fatalf("IsValid(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
