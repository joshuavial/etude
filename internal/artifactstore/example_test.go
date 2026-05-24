package artifactstore_test

import (
	"fmt"

	"github.com/joshuavial/etude/internal/artifactstore"
)

// ExampleStore_AddContent shows how to store a content artifact and inspect
// the content-addressed fields returned in the ManifestArtifact.
func ExampleStore_AddContent() {
	s := artifactstore.New()
	a, err := s.AddContent("output", "text/plain; charset=utf-8", []byte("hello\n"))
	if err != nil {
		panic(err)
	}
	fmt.Println(a.SHA256)
	fmt.Println(a.Storage)
	fmt.Println(a.Path)
	fmt.Println(a.Size)
	// Output:
	// 5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03
	// content
	// artifacts/sha256/58/5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03
	// 6
}
