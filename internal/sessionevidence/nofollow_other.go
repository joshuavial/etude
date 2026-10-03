//go:build !unix

package sessionevidence

import (
	"os"
)

const nofollowFlag = 0

func readRegularFileUnder(root, path string, limit int64, limited bool) ([]byte, error) {
	checkedPath, err := checkedRegularPath(path, root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(checkedPath)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	f, err := os.OpenFile(checkedPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readOpenedRegularFile(f, limit, limited)
}
