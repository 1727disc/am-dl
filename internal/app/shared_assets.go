package app

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"amdl/internal/model"
)

// updateAssetFromTemp preserves existing symbolic links and replaces a file
// only after a complete download has been compared and copied successfully.
func updateAssetFromTemp(source, destination string) (bool, error) {
	actual := destination
	info, err := os.Lstat(destination)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		actual, err = filepath.EvalSymlinks(destination)
		if err != nil {
			return false, fmt.Errorf("resolve existing asset link: %w", err)
		}
	}
	info, err = os.Stat(actual)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	mode := os.FileMode(0644)
	if err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("existing asset is not a regular file")
		}
		mode = info.Mode().Perm()
		same, err := sameFileBytes(source, actual)
		if err != nil {
			return false, err
		}
		if same {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(actual), 0755); err != nil {
		return false, err
	}
	in, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(actual), ".asset-update-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), actual); err != nil {
		return false, err
	}
	return true, nil
}

func writeChangedAsset(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".asset-content-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_, err = updateAssetFromTemp(tmp.Name(), path)
	return err
}

func assetFolderName(name, id string) string {
	name = strings.TrimSpace(forbiddenNames.ReplaceAllString(strings.ReplaceAll(name, "\x00", "_"), "_"))
	name = strings.TrimRight(name, ". ")
	if name == "" || name == "." || name == ".." {
		name = "Unknown"
	}
	id = strings.TrimSpace(forbiddenNames.ReplaceAllString(strings.ReplaceAll(id, "\x00", "_"), "_"))
	if len(id) > 64 {
		digest := sha256.Sum256([]byte(id))
		id = fmt.Sprintf("%x", digest[:12])
	}
	// Leave room for filename suffixes, including the TTML variant identifier.
	maxNameBytes := 210
	if id != "" && id != "." && id != ".." {
		maxNameBytes -= len(id) + 3
	}
	for len(name) > maxNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "Unknown"
	}
	if id != "" && id != "." && id != ".." {
		name += " [" + id + "]"
	}
	return name
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func (r *Runner) registerSharedAssetDir(local, master string) error {
	if !r.Config.Paths.Symlinking {
		return nil
	}
	if strings.TrimSpace(r.Config.Paths.Master) == "" {
		return fmt.Errorf("paths.master must be set when symlinking is enabled")
	}
	local, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	master, err = filepath.Abs(master)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(r.Config.Paths.Master)
	if err != nil {
		return err
	}
	if pathWithin(root, local) || pathWithin(local, master) {
		return fmt.Errorf("the Master folder must be separate from the output folder")
	}
	if r.sharedAssetDirs == nil {
		r.sharedAssetDirs = make(map[string]string)
	}
	r.sharedAssetDirs[local] = master
	return nil
}

func (r *Runner) sharedAssetDir(local string) (string, bool, error) {
	if !r.Config.Paths.Symlinking {
		return "", false, nil
	}
	local, err := filepath.Abs(local)
	if err != nil {
		return "", false, err
	}
	var best, target string
	for root, master := range r.sharedAssetDirs {
		if pathWithin(root, local) && len(root) > len(best) {
			best, target = root, master
		}
	}
	if best == "" {
		return "", false, nil
	}
	rel, err := filepath.Rel(best, local)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(target, rel), true, nil
}

// linkSharedFile leaves existing files and links alone, including dangling links.
func linkSharedFile(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	parent, err := filepath.Abs(filepath.Dir(destination))
	if err != nil {
		return err
	}
	link, err := filepath.Rel(parent, source)
	if err != nil {
		// Windows cannot form relative paths across drives.
		link = source
	}
	if err := os.Symlink(link, destination); os.IsExist(err) {
		return nil
	} else {
		return err
	}
}

func sharedAssetName(name string) bool {
	return name == "release-info.txt" || name == "biography.txt" || name == "colors.json" || name == "logo.png" || name == "folder.jpg" || strings.HasSuffix(name, "_preview_frame.png") || strings.HasSuffix(name, "_animated_artwork.mp4")
}

func (r *Runner) syncSharedAssets(source, destination string, recursive bool) error {
	entries, err := os.ReadDir(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// A deduplicated Master file can leave one of our older motion links
	// dangling. Remove only broken motion links pointing into this source folder.
	localEntries, readErr := os.ReadDir(destination)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	for _, entry := range localEntries {
		if entry.Type()&os.ModeSymlink == 0 || !(strings.HasSuffix(entry.Name(), "_preview_frame.png") || strings.HasSuffix(entry.Name(), "_animated_artwork.mp4")) {
			continue
		}
		path := filepath.Join(destination, entry.Name())
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(destination, target)
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return err
		}
		if pathWithin(sourceAbs, target) {
			if _, err := os.Stat(target); os.IsNotExist(err) {
				if err := os.Remove(path); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		src, dst := filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if recursive {
				if err := r.syncSharedAssets(src, dst, true); err != nil {
					return err
				}
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && sharedAssetName(entry.Name()) {
			localInfo, err := os.Lstat(dst)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			category := "Artist information"
			if strings.HasSuffix(entry.Name(), "_preview_frame.png") || strings.HasSuffix(entry.Name(), "_animated_artwork.mp4") || entry.Name() == "folder.jpg" {
				category = "Motion Artwork"
			}
			if entry.Name() == "release-info.txt" {
				category = "Album information"
			}
			if err == nil && localInfo.Mode().IsRegular() && r.wantsRefresh(category) {
				if _, err := updateAssetFromTemp(src, dst); err != nil {
					return fmt.Errorf("update existing asset: %w", err)
				}
			} else if err := linkSharedFile(src, dst); err != nil {
				return fmt.Errorf("link shared asset: %w", err)
			}
		}
	}
	return nil
}

func (r *Runner) sharedLyricsPath(local string, track *model.Track) (string, bool, error) {
	master, shared, err := r.sharedAssetDir(local)
	if err != nil || !shared {
		return "", shared, err
	}
	key := sha256.Sum256([]byte(track.ID + "\x00" + track.Storefront + "\x00" + r.Config.General.Language + "\x00" + r.Config.Metadata.Lyrics.Type + "\x00" + r.Config.Metadata.Lyrics.Extra))
	name := fmt.Sprintf("%s [%x].ttml", assetFolderName(track.Resp.Attributes.Name, track.ID), key[:6])
	return filepath.Join(master, "Lyrics", name), true, nil
}

func (r *Runner) cachedSharedLyrics(local string, track *model.Track) (string, bool, error) {
	if !strings.EqualFold(r.Config.Metadata.Lyrics.Format, "ttml") {
		return "", false, nil
	}
	path, shared, err := r.sharedLyricsPath(local, track)
	if err != nil || !shared {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// withSharedAssets runs a writer in Master and links its generated files back.
// subtree selects a child folder created by the writer; an empty subtree links
// only files directly in the destination, not its subfolders.
func (r *Runner) withSharedAssets(local, subtree string, fn func(string) error) error {
	master, shared, err := r.sharedAssetDir(local)
	if err != nil {
		return err
	}
	if !shared {
		return fn(local)
	}
	if err := os.MkdirAll(master, 0755); err != nil {
		return err
	}
	writeErr := fn(master)
	linkErr := r.syncSharedAssets(filepath.Join(master, subtree), filepath.Join(local, subtree), subtree != "")
	return errors.Join(writeErr, linkErr)
}
