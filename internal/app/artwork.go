package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ffmpegURLPattern = regexp.MustCompile(`https?://[^\s'"<>]+`)

func artworkFFmpegError(output []byte) string {
	text := ffmpegURLPattern.ReplaceAllString(string(output), "[URL]")
	text = strings.TrimSpace(text)
	const maxErrorBytes = 2000
	if len(text) > maxErrorBytes {
		text = text[len(text)-maxErrorBytes:]
	}
	return text
}

func (r *Runner) saveAnimatedArtwork(dir string, squareVideoURL string, tallVideoURL string) {
	if squareVideoURL != "" {
		if err := r.downloadAnimatedArtwork(dir, "square", squareVideoURL, true); err != nil {
			fmt.Printf("no motion video square: %v\n", err)
		}
		squarePath := filepath.Join(dir, "square_animated_artwork.mp4")
		squareExists, squareErr := fileExists(squarePath)
		if squareErr != nil {
			fmt.Printf("check animated artwork: %v\n", squareErr)
		}
		if r.Config.Metadata.Artwork.EmbyAnimated && squareExists {
			gifExists, gifErr := fileExists(filepath.Join(dir, "folder.jpg"))
			if gifErr != nil {
				fmt.Printf("check animated artwork gif: %v\n", gifErr)
			} else if !gifExists {
				cmd := exec.Command("ffmpeg", "-i", squarePath, "-vf", "scale=440:-1", "-r", "24", "-f", "gif", filepath.Join(dir, "folder.jpg"))
				if err := cmd.Run(); err != nil {
					fmt.Printf("animated artwork square to gif err: %v\n", err)
				}
			}
		}
	}
	if tallVideoURL != "" {
		if err := r.downloadAnimatedArtwork(dir, "tall", tallVideoURL, true); err != nil {
			fmt.Printf("no motion video tall: %v\n", err)
		}
	}
}

func (r *Runner) downloadAnimatedArtwork(dir string, orientation string, videoURL string, deduplicate bool) error {
	if deduplicate {
		if err := deduplicateMotionFiles(dir, "_animated_artwork.mp4"); err != nil {
			return err
		}
	}
	output := filepath.Join(dir, orientation+"_animated_artwork.mp4")
	exists, err := fileExists(output)
	if err != nil {
		return fmt.Errorf("check existing artwork: %w", err)
	}
	if exists && !r.wantsRefresh("Motion Artwork") {
		return nil
	}
	videoURL, err = r.extractVideo(videoURL)
	if err != nil {
		return err
	}
	fmt.Printf("Animation Artwork %s Downloading...\n", orientation)
	tmp, err := os.CreateTemp(dir, orientation+"_animated_artwork-*.mp4")
	if err != nil {
		return fmt.Errorf("create temporary artwork: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return err
	}
	for attempt := 1; attempt <= 2; attempt++ {
		cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-i", videoURL, "-c", "copy", tmpPath)
		output, runErr := cmd.CombinedOutput()
		if runErr == nil {
			break
		}
		if attempt == 1 {
			fmt.Printf("Animation artwork %s failed; retrying...\n", orientation)
			time.Sleep(time.Second)
			continue
		}
		if detail := artworkFFmpegError(output); detail != "" {
			return fmt.Errorf("download animated artwork: %w: %s", runErr, detail)
		}
		return fmt.Errorf("download animated artwork: %w", runErr)
	}
	if exists || !deduplicate {
		changed, err := updateAssetFromTemp(tmpPath, output)
		if err != nil {
			return fmt.Errorf("save animated artwork: %w", err)
		}
		if deduplicate {
			if err := deduplicateMotionFiles(dir, "_animated_artwork.mp4"); err != nil {
				return err
			}
		}
		if changed {
			fmt.Printf("Animation Artwork %s Downloaded\n", orientation)
		} else {
			fmt.Printf("Animation artwork %s is up to date.\n", orientation)
		}
		return nil
	}
	saved, err := saveUniqueMotionFile(tmpPath, output, "_animated_artwork.mp4")
	if err != nil {
		return err
	}
	if !saved {
		fmt.Printf("Animation artwork %s already saved.\n", orientation)
		return nil
	}
	fmt.Printf("Animation Artwork %s Downloaded\n", orientation)
	return nil
}

func sanitizeFolderName(name string) string {
	if name == "" {
		return ""
	}
	if name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}
	return forbiddenNames.ReplaceAllString(name, "_")
}

func (r *Runner) prepareCollectionFolder(base string, displayName string) (string, error) {
	if strings.HasSuffix(displayName, ".") {
		displayName = displayName[:len(displayName)-1]
	}
	displayName = strings.TrimSpace(displayName)
	folderPath := filepath.Join(base, sanitizeFolderName(displayName))
	if err := createDirectory(folderPath); err != nil {
		return "", err
	}
	return folderPath, nil
}

// prepareAlbumFolder creates a separate folder for a duplicate album when enabled.
// A runner reuses its chosen folder on retries of the same album.
func (r *Runner) prepareAlbumFolder(base, name string, version bool) (string, error) {
	if !version {
		return r.prepareCollectionFolder(base, name)
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, "."))
	basePath := filepath.Join(base, sanitizeFolderName(name))
	key, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}
	if existing, ok := r.albumVersionDirs[key]; ok {
		return existing, nil
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return "", err
	}
	path := basePath
	if err := os.Mkdir(path, 0755); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
		stamp := time.Now().Format("2006-01-02 15-04-05")
		for n := 1; ; n++ {
			suffix := " [" + stamp + "]"
			if n > 1 {
				suffix = fmt.Sprintf(" [%s %d]", stamp, n)
			}
			path = basePath + suffix
			if err := os.Mkdir(path, 0755); err != nil {
				if os.IsExist(err) {
					continue
				}
				return "", err
			}
			break
		}
	}
	if r.albumVersionDirs == nil {
		r.albumVersionDirs = make(map[string]string)
	}
	r.albumVersionDirs[key] = path
	return path, nil
}

func (r *Runner) saveM3UPlaylist(dir string, name string, startIdx int) {
	if !r.Flags.SaveM3U8 || r.Flags.LyricOnly || (r.Flags.Refresh && !r.wantsRefresh("Audio")) {
		return
	}
	if len(r.State.AddedTracks) > startIdx {
		if err := r.writeM3UPlaylist(dir, name, r.State.AddedTracks[startIdx:]); err != nil {
			fmt.Printf("Failed to write M3U8 playlist: %v\n", err)
		} else {
			fmt.Println("M3U8 playlist saved.")
		}
	}
}

func createDirectory(path string) error {
	if err := os.MkdirAll(path, os.ModePerm); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

func isOrientationMotionPreview(name string) bool {
	return name == "square" || name == "tall"
}
