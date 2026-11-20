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
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if logo == "" {
		return nil
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	fmt.Println("Artist logo saved")
	return nil
}

// saveArtistPreviewFrame downloads a PNG without replacing an existing frame.
func (r *Runner) saveMotionPreviewFrame(dir, variant, template string, width, height int, label string) error {
	path := filepath.Join(dir, variant+"_preview_frame.png")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
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
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	if err := writeFileIfAbsent(path, data); err != nil {
		return err
	}
	fmt.Println(label + " motion preview frame saved")
	return nil
}
