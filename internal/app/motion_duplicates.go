package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

func motionNameLess(a, b string) bool {
	lengthA, lengthB := utf8.RuneCountInString(a), utf8.RuneCountInString(b)
	if lengthA != lengthB {
		return lengthA < lengthB
	}
	return a < b
}

// sameFileBytes compares complete file contents without loading videos into memory.
func sameFileBytes(first, second string) (bool, error) {
	a, err := os.Open(first)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := os.Open(second)
	if err != nil {
		return false, err
	}
	defer b.Close()
	ai, err := a.Stat()
	if err != nil {
		return false, err
	}
	bi, err := b.Stat()
	if err != nil {
		return false, err
	}
	if !ai.Mode().IsRegular() || !bi.Mode().IsRegular() {
		return false, fmt.Errorf("motion asset is not a regular file")
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	bufA, bufB := make([]byte, 128*1024), make([]byte, 128*1024)
	for {
		na, ea := io.ReadFull(a, bufA)
		nb, eb := io.ReadFull(b, bufB)
		if ea != nil && ea != io.EOF && ea != io.ErrUnexpectedEOF {
			return false, ea
		}
		if eb != nil && eb != io.EOF && eb != io.ErrUnexpectedEOF {
			return false, eb
		}
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		if ea == io.EOF || ea == io.ErrUnexpectedEOF {
			return eb == io.EOF || eb == io.ErrUnexpectedEOF, nil
		}
		if eb != nil {
			return false, nil
		}
	}
}

// duplicateMotionFile only considers regular files with the same generated
// asset suffix, so tracks, cover art, and unrelated files are not removed.
func duplicateMotionFile(path, suffix string) (string, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		candidate := filepath.Join(filepath.Dir(path), entry.Name())
		if candidate == path {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		same, err := sameFileBytes(path, candidate)
		if err != nil {
			return "", err
		}
		if same {
			return candidate, nil
		}
	}
	return "", nil
}

// deduplicateMotionFiles keeps the shortest name, breaking ties alphabetically.
func deduplicateMotionFiles(dir, suffix string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return motionNameLess(entries[i].Name(), entries[j].Name()) })
	var kept []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		duplicate := ""
		for _, existing := range kept {
			same, err := sameFileBytes(path, existing)
			if err != nil {
				return err
			}
			if same {
				duplicate = existing
				break
			}
		}
		if duplicate == "" {
			kept = append(kept, path)
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove duplicate motion asset: %w", err)
		}
		fmt.Println("Removed duplicate motion artwork.")
	}
	return nil
}

// saveUniqueMotionFile keeps the shortest filename even if that copy arrives
// after a longer-named one. Existing files are never overwritten.
func saveUniqueMotionFile(tmp, destination, suffix string) (bool, error) {
	duplicate, err := duplicateMotionFile(tmp, suffix)
	if err != nil {
		return false, err
	}
	if duplicate != "" && !motionNameLess(filepath.Base(destination), filepath.Base(duplicate)) {
		return false, nil
	}
	source, err := os.Open(tmp)
	if err != nil {
		return false, err
	}
	defer source.Close()
	dest, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return false, copyErr
		}
		return false, closeErr
	}
	if duplicate != "" {
		same, err := sameFileBytes(destination, duplicate)
		if err != nil {
			return true, err
		}
		if same {
			if err := os.Remove(duplicate); err != nil {
				return true, fmt.Errorf("remove duplicate motion asset: %w", err)
			}
			fmt.Println("Removed duplicate motion artwork.")
		}
	}
	return true, nil
}
