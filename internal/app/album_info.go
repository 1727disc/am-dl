package app

import (
	ampapi "amdl/internal/amp-api"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func albumDuration(ms int64) string {
	h := ms / 3600000
	m := (ms / 60000) % 60
	s := (ms / 1000) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms%1000)
	}
	return fmt.Sprintf("%d:%02d.%03d", m, s, ms%1000)
}

func renderAlbumInfo(album *ampapi.AlbumRespData) (string, error) {
	if album == nil {
		return "", fmt.Errorf("missing album metadata")
	}
	a := album.Attributes
	var lines []string
	if a.Name != "" {
		lines = append(lines, "Album: "+a.Name)
	}
	if a.ArtistName != "" {
		lines = append(lines, "Artist: "+a.ArtistName)
	}
	if len(a.GenreNames) > 0 {
		lines = append(lines, "Genres: "+strings.Join(a.GenreNames, ", "))
	}
	if a.ReleaseDate != "" {
		lines = append(lines, "Release date: "+a.ReleaseDate)
	}
	if a.Copyright != "" {
		lines = append(lines, "Copyright: "+a.Copyright)
	}
	tracks := album.Relationships.Tracks.Data
	var total int64
	complete := len(tracks) > 0 && album.Relationships.Tracks.Next == "" && (a.TrackCount == 0 || len(tracks) == a.TrackCount)
	for _, track := range tracks {
		if track.Attributes.DurationInMillis <= 0 {
			complete = false
		} else {
			total += int64(track.Attributes.DurationInMillis)
		}
	}
	if complete {
		lines = append(lines, "Album length: "+albumDuration(total))
	}
	short := strings.TrimSpace(a.EditorialNotes.Short)
	if short == "" {
		short = strings.TrimSpace(a.PlainEditorialNotes.Short)
	}
	standard := strings.TrimSpace(a.EditorialNotes.Standard)
	if standard == "" {
		standard = strings.TrimSpace(a.PlainEditorialNotes.Standard)
	}
	if short != "" {
		lines = append(lines, "", "Short description:", short)
	}
	if standard != "" {
		lines = append(lines, "", "Standard description:", standard)
	}
	if len(tracks) > 0 {
		lines = append(lines, "", "Tracks:")
		previousDisc := 0
		discPositions := make(map[int]int)
		for _, track := range tracks {
			disc := track.Attributes.DiscNumber
			if disc <= 0 {
				disc = 1
			}
			discPositions[disc]++
			if disc != previousDisc {
				lines = append(lines, "", fmt.Sprintf("Disc %d", disc))
				previousDisc = disc
			}
			number := track.Attributes.TrackNumber
			if number <= 0 {
				number = discPositions[disc]
			}
			marker := " "
			if track.Meta.Popularity != nil && *track.Meta.Popularity > 0.7 {
				marker = "•"
			}
			label := fmt.Sprintf("%s %d %s", marker, number, track.Attributes.Name)
			var details []string
			if track.Attributes.DurationInMillis > 0 {
				details = append(details, albumDuration(int64(track.Attributes.DurationInMillis)))
			}
			if track.Attributes.ReleaseDate != "" {
				details = append(details, "released "+track.Attributes.ReleaseDate)
			}
			if len(details) > 0 {
				label += " (" + strings.Join(details, "; ") + ")"
			}
			lines = append(lines, label)
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// writeAlbumInfoAtomic avoids leaving a partial file if writing fails.
func writeAlbumInfoAtomic(path string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".album-info-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

var albumInfoTrackLine = regexp.MustCompile(`^(?:•| ) [0-9]+ |^[0-9]+\. |^Disc [0-9]+$`)

// mergeAlbumInfo updates a recognized generated track section and preserves the header.
func mergeAlbumInfo(outputPath, rendered string) (string, error) {
	previous, err := os.ReadFile(outputPath)
	if os.IsNotExist(err) {
		return rendered, nil
	}
	if err != nil {
		return "", err
	}
	old := string(previous)
	const delimiter = "\nTracks:\n"
	pos, newPos := strings.Index(old, delimiter), strings.Index(rendered, delimiter)
	if pos < 0 || newPos < 0 {
		return "", fmt.Errorf("existing album-info.txt has no recognizable track section; preserve it and rename it before updating")
	}
	for _, line := range strings.Split(old[pos+len(delimiter):], "\n") {
		if strings.TrimSpace(line) != "" && !albumInfoTrackLine.MatchString(line) {
			return "", fmt.Errorf("existing album-info.txt contains unrecognized text in its track section; preserve it and rename it before updating")
		}
	}
	return old[:pos] + rendered[newPos:], nil
}

func saveFullAlbumInfo(dir string, album *ampapi.AlbumRespData) error {
	if album == nil {
		return fmt.Errorf("missing album metadata")
	}
	rendered, err := renderAlbumInfo(album)
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "album-extra")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, "album-info.txt")
	if len(album.Relationships.Tracks.Data) > 0 {
		rendered, err = mergeAlbumInfo(path, rendered)
	} else if _, statErr := os.Stat(path); statErr == nil {
		return nil
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err != nil {
		return err
	}
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == rendered {
		return nil
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	return writeAlbumInfoAtomic(path, []byte(rendered))
}
