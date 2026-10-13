package book

import (
	"chaptersmith/internal/meta"
	"fmt"
	"os"
	"path/filepath"
)

// CacheCover copies a selected image into managed storage; an empty path clears it.
func CacheCover(cacheDir, imagePath string) (string, error) {
	if imagePath == "" {
		return "", nil
	}
	return meta.CopyCoverToCache(imagePath, cacheDir)
}

func importCacheDirectory(cacheDir string, inputs ImportInputs) (string, error) {
	if cacheDir == "" {
		return "", fmt.Errorf("CacheDir is required and must be an application-managed directory")
	}
	absolute, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors before creating anything, including symlink aliases.
	canonical, err := resolveCachePath(absolute)
	if err != nil {
		return "", err
	}
	forbidden := map[string]bool{inputs.WorkDir: true}
	for _, path := range inputs.Files {
		forbidden[filepath.Dir(path)] = true
	}
	if forbidden[canonical] {
		return "", fmt.Errorf("CacheDir %s matches a source directory; select a dedicated application-managed directory", cacheDir)
	}
	if st, err := os.Stat(canonical); err == nil && !st.IsDir() {
		return "", fmt.Errorf("CacheDir %s is not a directory", cacheDir)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return canonical, nil
}
func resolveCachePath(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err == nil {
		return p, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err := resolveCachePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
