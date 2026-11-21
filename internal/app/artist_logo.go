package app

import (
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var imageExtension = regexp.MustCompile(`(?i)\.(jpg|jpeg|webp|png)$`)

// artistLogoImageURL requests PNG explicitly; JPEG discards logo transparency.
func artistLogoImageURL(template, size string) (string, error) {
	u, err := url.Parse(template)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid artist logo URL")
	}
	if !regexp.MustCompile(`^\d+x\d+$`).MatchString(size) {
		return "", fmt.Errorf("invalid artwork size %q", size)
	}
	path := strings.ReplaceAll(u.Path, "{w}x{h}", size)
	path = strings.ReplaceAll(path, "{w}", strings.Split(size, "x")[0])
	path = strings.ReplaceAll(path, "{h}", strings.Split(size, "x")[1])
	path = strings.ReplaceAll(path, "{c}", "bb")
	path = strings.ReplaceAll(path, "{f}", "png")
	if imageExtension.MatchString(path) {
		path = imageExtension.ReplaceAllString(path, ".png")
	} else {
		return "", fmt.Errorf("unrecognized artist logo URL format")
	}
	u.Path, u.RawPath = path, ""
	return u.String(), nil
}

func (r *Runner) saveArtistLogo(dir, logo string, width, height int) error {
	path := filepath.Join(dir, "logo.png")
	if logo == "" {
		return nil
	}
	if !r.wantsRefresh("Artist information") {
		if exists, err := fileExists(path); err != nil {
			return err
		} else if exists {
			return nil
		}
	}
	size := r.Config.Metadata.Artwork.Size
	if width > 0 && height > 0 {
		size = fmt.Sprintf("%dx%d", width, height)
	}
	imageURL, err := artistLogoImageURL(logo, size)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "logo.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := r.getCover(imageURL, tmp); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	if _, err := png.DecodeConfig(tmp); err != nil {
		return fmt.Errorf("artist logo is not a valid PNG: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	changed, err := updateAssetFromTemp(tmp.Name(), path)
	if err != nil {
		return err
	}
	if changed {
		fmt.Println("Artist logo saved")
	}
	return nil
}

// saveMotionPreviewFrame refreshes a PNG only when its bytes change.
func (r *Runner) saveMotionPreviewFrame(dir, variant, template string, width, height int, label string, deduplicate bool) error {
	if deduplicate {
		if err := deduplicateMotionFiles(dir, "_preview_frame.png"); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, variant+"_preview_frame.png")
	exists, err := fileExists(path)
	if err != nil {
		return err
	}
	if exists && !r.wantsRefresh("Motion Artwork") {
		return nil
	}
	size := r.Config.Metadata.Artwork.Size
	if width > 0 && height > 0 {
		size = fmt.Sprintf("%dx%d", width, height)
	}
	imageURL, err := artistLogoImageURL(template, size)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, variant+"_preview-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := r.getCover(imageURL, tmp); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	if _, err := png.DecodeConfig(tmp); err != nil {
		return fmt.Errorf("artist preview frame is not a valid PNG: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if exists || !deduplicate {
		changed, err := updateAssetFromTemp(tmp.Name(), path)
		if err != nil {
			return err
		}
		if deduplicate {
			if err := deduplicateMotionFiles(dir, "_preview_frame.png"); err != nil {
				return err
			}
		}
		if changed {
			fmt.Println(label + " motion preview frame saved")
		}
		return nil
	}
	saved, err := saveUniqueMotionFile(tmp.Name(), path, "_preview_frame.png")
	if err != nil {
		return err
	}
	if !saved {
		fmt.Println(label + " motion preview frame already saved.")
		return nil
	}
	fmt.Println(label + " motion preview frame saved")
	return nil
}
