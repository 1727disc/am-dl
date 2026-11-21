package app

import (
	ampapi "amdl/internal/amp-api"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var artistVideoVariants = []struct{ key, name string }{
	{"motionArtistSquare1x1", "artist_square"},
	{"motionArtistWide16x9", "artist_wide"},
	{"motionArtistFullscreen16x9", "artist_fullscreen"},
	{"motionArtistSingular16x9", "artist_singular"},
}

// writeFileIfAbsent preserves files that may have been edited by the user.
func writeFileIfAbsent(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (r *Runner) saveArtistDetails(artistDir, storefront, artistID, token, mediaUserToken string) error {
	cfg := r.Config.Metadata.Artwork
	dir := filepath.Join(artistDir, "artist-extra")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create artist-extra folder: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	videoKey := artistVideoAttempt{directory: absDir, storefront: storefront, artistID: artistID}
	checkVideos := cfg.SaveArtistAnimated && r.downloadCategory("Motion Artwork") && !r.artistVideoAttempts[videoKey]
	checkInfo := r.Config.Metadata.Extras.SaveArtistExtras && r.downloadCategory("Artist information") && !r.artistInfoAttempts[videoKey]
	if !checkVideos && !checkInfo {
		return nil
	}

	artist, err := ampapi.GetArtistMetadata(storefront, artistID, r.Config.General.Language, token, mediaUserToken)
	if err != nil {
		return err
	}
	var failures []error
	if checkInfo {
		logo := artist.EditorialArtwork.Logo
		if err := r.saveArtistLogo(dir, logo.URL, logo.Width, logo.Height); err != nil {
			failures = append(failures, fmt.Errorf("logo: %w", err))
		}
	}
	if checkVideos {
		if r.artistVideoAttempts == nil {
			r.artistVideoAttempts = make(map[artistVideoAttempt]bool)
		}
		r.artistVideoAttempts[videoKey] = true
		for _, variant := range artistVideoVariants {
			frame := artist.EditorialVideo[variant.key].PreviewFrame
			if r.Config.Metadata.Extras.SaveArtistExtras && frame.URL != "" {
				if err := r.saveMotionPreviewFrame(dir, variant.name, frame.URL, frame.Width, frame.Height, "Artist", false); err != nil {
					failures = append(failures, fmt.Errorf("%s preview frame: %w", variant.name, err))
				}
			}
			if video := artist.EditorialVideo[variant.key].Video; video != "" {
				if err := r.downloadAnimatedArtwork(dir, variant.name, video, false); err != nil {
					failures = append(failures, fmt.Errorf("%s video: %w", variant.name, err))
				}
			}
		}
	}
	if checkInfo {
		if r.artistInfoAttempts == nil {
			r.artistInfoAttempts = make(map[artistVideoAttempt]bool)
		}
		r.artistInfoAttempts[videoKey] = true
		if artist.KeyColor != "" {
			colors, err := json.MarshalIndent(struct {
				KeyColor string `json:"keyColor"`
			}{artist.KeyColor}, "", "  ")
			if err == nil {
				if r.wantsRefresh("Artist information") {
					err = writeChangedAsset(filepath.Join(dir, "colors.json"), append(colors, '\n'))
				} else {
					err = writeFileIfAbsent(filepath.Join(dir, "colors.json"), append(colors, '\n'))
				}
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("colors: %w", err))
			}
		}
		{
			var lines []string
			if artist.Name != "" {
				lines = append(lines, artist.Name)
			}
			if artist.BornOrFormed != "" {
				label := "Born or formed"
				if artist.IsGroup != nil {
					if *artist.IsGroup {
						label = "Formed"
					} else {
						label = "Born"
					}
				}
				lines = append(lines, label+": "+artist.BornOrFormed)
			}
			if artist.Origin != "" {
				lines = append(lines, "Origin: "+artist.Origin)
			}
			if len(artist.GenreNames) > 0 {
				lines = append(lines, "Genres: "+strings.Join(artist.GenreNames, ", "))
			}
			if artist.ArtistBio != "" {
				lines = append(lines, "", artist.ArtistBio)
			}
			if artist.ArtistBio != "" || artist.BornOrFormed != "" || artist.Origin != "" || len(artist.GenreNames) > 0 {
				var err error
				if r.wantsRefresh("Artist information") {
					err = writeChangedAsset(filepath.Join(dir, "biography.txt"), []byte(strings.Join(lines, "\n")+"\n"))
				} else {
					err = writeFileIfAbsent(filepath.Join(dir, "biography.txt"), []byte(strings.Join(lines, "\n")+"\n"))
				}
				if err != nil {
					failures = append(failures, fmt.Errorf("biography: %w", err))
				}
			}
		}
	}
	return errors.Join(failures...)
}
