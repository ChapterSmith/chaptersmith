package meta

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// CacheCoverImage discovers sidecar or attached artwork and publishes it in managed storage.
func CacheCoverImage(workDir, firstAudioFile, cacheDir string, logger *log.Logger) (string, error) {
	if logger == nil {
		logger = log.New(os.Stderr, "", 0)
	}
	cache, err := coverCacheDirectory(cacheDir)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{workDir, filepath.Dir(firstAudioFile)} {
		if dir == "" || (firstAudioFile == "" && dir == ".") {
			continue
		}
		p, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		p, err = filepath.EvalSymlinks(p)
		if err != nil {
			return "", err
		}
		if cache == p {
			return "", fmt.Errorf("cache directory %s matches source directory %s; use a dedicated managed directory", cacheDir, dir)
		}
	}
	if imagePath, ok := FindCoverImage(workDir, firstAudioFile, logger); ok {
		return CopyCoverToCache(imagePath, cache)
	}
	if firstAudioFile == "" {
		return "", nil
	}
	data, extension, err := readAttachedCover(firstAudioFile)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	return publishCover(cache, data, extension)
}

// CopyCoverToCache validates an image and publishes a content-addressed managed copy.
// JPEG and PNG bytes are preserved; GIF is converted to PNG for renderer compatibility.
func CopyCoverToCache(imagePath, cacheDir string) (string, error) {
	if imagePath == "" {
		return "", nil
	}
	extension := strings.ToLower(filepath.Ext(imagePath))
	switch extension {
	case ".jpg", ".jpeg", ".png", ".gif":
	default:
		return "", fmt.Errorf("unsupported cover format %s in %s; provide a JPEG or PNG cover", extension, imagePath)
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("reading cover %s: %w", imagePath, err)
	}
	validated, extension, err := validateCoverBytes(data, true)
	if err != nil {
		return "", fmt.Errorf("cover %s: %w", imagePath, err)
	}
	cache, err := coverCacheDirectory(cacheDir)
	if err != nil {
		return "", err
	}
	source, err := filepath.Abs(imagePath)
	if err != nil {
		return "", err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(validated)
	destination := filepath.Join(cache, fmt.Sprintf("%x%s", sum, extension))
	if filepath.Dir(source) == cache && source != destination {
		return "", fmt.Errorf("cache directory %s matches cover source directory; use a dedicated managed directory", cacheDir)
	}
	return publishCover(cache, validated, extension)
}

func validateCoverBytes(data []byte, allowGIF bool) ([]byte, string, error) {
	im, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("invalid or unsupported image; provide a JPEG or PNG cover: %w", err)
	}
	switch format {
	case "jpeg":
		return data, ".jpg", nil
	case "png":
		return data, ".png", nil
	case "gif":
		if allowGIF {
			var b bytes.Buffer
			if err := png.Encode(&b, im); err != nil {
				return nil, "", fmt.Errorf("converting GIF cover: %w", err)
			}
			return b.Bytes(), ".png", nil
		}
	}
	return nil, "", fmt.Errorf("unsupported %s image; provide a JPEG or PNG cover", format)
}
func coverCacheDirectory(cacheDir string) (string, error) {
	if cacheDir == "" {
		return "", fmt.Errorf("cover cache directory is required")
	}
	p, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	// Resolve ancestors without writing so ownership checks precede publication.
	canonical, err := resolveCoverCachePath(p)
	if err != nil {
		return "", fmt.Errorf("cover cache %s: %w", cacheDir, err)
	}
	if st, err := os.Stat(canonical); err == nil && !st.IsDir() {
		return "", fmt.Errorf("cover cache %s is not a directory", cacheDir)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return canonical, nil
}
func resolveCoverCachePath(path string) (string, error) {
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
	p, err = resolveCoverCachePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, filepath.Base(path)), nil
}
func publishCover(cache string, data []byte, extension string) (string, error) {
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", fmt.Errorf("creating cover cache %s: %w", cache, err)
	}
	sum := sha256.Sum256(data)
	destination := filepath.Join(cache, fmt.Sprintf("%x%s", sum, extension))
	checkExisting := func() (bool, error) {
		st, err := os.Lstat(destination)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !st.Mode().IsRegular() {
			return false, fmt.Errorf("existing cached cover %s is not a regular file", destination)
		}
		cached, err := os.ReadFile(destination)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(cached, data) {
			return false, fmt.Errorf("existing cached cover %s has invalid content", destination)
		}
		return true, nil
	}
	if exists, err := checkExisting(); exists || err != nil {
		if err != nil {
			return "", err
		}
		return destination, nil
	}
	temp, err := os.CreateTemp(cache, ".cover-*")
	if err != nil {
		return "", fmt.Errorf("creating cover cache temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("writing cover cache: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("closing cover cache: %w", err)
	}
	// Linking publishes atomically without overwriting a concurrent import's entry.
	if err := os.Link(tempPath, destination); err != nil {
		if exists, checkErr := checkExisting(); checkErr != nil {
			return "", checkErr
		} else if exists {
			return destination, nil
		}
		return "", fmt.Errorf("publishing cached cover: %w", err)
	}
	return destination, nil
}
