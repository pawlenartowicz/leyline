package watch

import (
	"io/fs"
	"os"
	"path/filepath"
)

func osStat(p string) (fs.FileInfo, error) { return os.Stat(p) }

func walkDirs(root string, fn func(string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return fn(path)
		}
		return nil
	})
}

// walkContentDirs walks vault content directories — every directory under
// root EXCEPT the .leyline/ and .git/ subtrees, which have their own
// dedicated watchers. Skipping these here keeps editor scratch files,
// daemon runtime state (.leyline/backend/), and git's object writes from
// triggering structural deps rebuilds; the dedicated handlers pick up the
// events that actually matter.
func walkContentDirs(root string, fn func(string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root {
			if d.Name() == ".leyline" || d.Name() == ".git" {
				return filepath.SkipDir
			}
		}
		return fn(path)
	})
}
