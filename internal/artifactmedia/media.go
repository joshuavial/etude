package artifactmedia

import (
	"path/filepath"
	"strings"
)

// Infer returns the media type Etude stores for artifact paths when callers did
// not supply an explicit type.
func Infer(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".diff", ".patch":
		return "text/x-diff; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
