package artifactmedia

import "testing"

func TestInfer(t *testing.T) {
	cases := map[string]string{
		"a.txt":      "text/plain; charset=utf-8",
		"a.md":       "text/markdown; charset=utf-8",
		"a.markdown": "text/markdown; charset=utf-8",
		"a.json":     "application/json",
		"a.yaml":     "application/yaml",
		"a.yml":      "application/yaml",
		"a.diff":     "text/x-diff; charset=utf-8",
		"a.patch":    "text/x-diff; charset=utf-8",
		"a.html":     "text/html; charset=utf-8",
		"a.htm":      "text/html; charset=utf-8",
		"a.png":      "image/png",
		"a.jpg":      "image/jpeg",
		"a.jpeg":     "image/jpeg",
		"a.gif":      "image/gif",
		"a.svg":      "image/svg+xml",
		"a.bin":      "application/octet-stream",
		"a":          "application/octet-stream",
	}
	for path, want := range cases {
		if got := Infer(path); got != want {
			t.Errorf("Infer(%q) = %q, want %q", path, got, want)
		}
	}
}
