package app

import (
	ampapi "amdl/internal/amp-api"
	"amdl/internal/download"
	"amdl/internal/fsutil"
	"amdl/internal/model"
	"amdl/internal/wrapper"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/grafov/m3u8"
	"github.com/itouakirai/go-mp4tag"
	"github.com/olekukonko/tablewriter"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func (r *Runner) writeCover(sanAlbumFolder, name string, url string) (string, error) {
	originalUrl := url
	var ext string
	var covPath string
	if r.Config.Metadata.Artwork.Format == "original" {
		ext = strings.Split(url, "/")[len(strings.Split(url, "/"))-2]
		ext = ext[strings.LastIndex(ext, ".")+1:]
		covPath = filepath.Join(sanAlbumFolder, name+"."+ext)
	} else {
		covPath = filepath.Join(sanAlbumFolder, name+"."+r.Config.Metadata.Artwork.Format)
	}
	if !r.wantsRefresh("Covers") {
		if exists, err := fileExists(covPath); err != nil {
			return "", err
		} else if exists {
			return covPath, nil
		}
	}
	absPath, err := filepath.Abs(covPath)
	if err != nil {
		return "", err
	}
	refreshKey := absPath + "\x00" + originalUrl
	if r.coverRefreshAttempts[refreshKey] {
		if exists, err := fileExists(covPath); err != nil {
			return "", err
		} else if exists {
			return covPath, nil
		}
	}
	if master, shared, err := r.sharedAssetDir(sanAlbumFolder); err != nil {
		return "", err
	} else if shared {
		if err := os.MkdirAll(master, 0755); err != nil {
			return "", err
		}
		// The Master directory is not registered as a local output directory.
		sharedPath, err := r.writeCover(master, name, originalUrl)
		if err != nil {
			return "", err
		}
		localInfo, statErr := os.Lstat(covPath)
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
		if statErr == nil && localInfo.Mode().IsRegular() && r.wantsRefresh("Covers") {
			if _, err := updateAssetFromTemp(sharedPath, covPath); err != nil {
				return "", err
			}
		} else if err := linkSharedFile(sharedPath, covPath); err != nil {
			return "", err
		}
		if _, err := os.Stat(covPath); err != nil {
			return "", fmt.Errorf("existing cover link is unavailable: %w", err)
		}
		if r.coverRefreshAttempts == nil {
			r.coverRefreshAttempts = make(map[string]bool)
		}
		r.coverRefreshAttempts[refreshKey] = true
		return covPath, nil
	}
	if r.Config.Metadata.Artwork.Format == "png" {
		re := regexp.MustCompile(`\{w\}x\{h\}`)
		parts := re.Split(url, 2)
		url = parts[0] + "{w}x{h}" + strings.Replace(parts[1], ".jpg", ".png", 1)
	}
	url = strings.Replace(url, "{w}x{h}", r.Config.Metadata.Artwork.Size, 1)
	if r.Config.Metadata.Artwork.Format == "original" {
		url = strings.Replace(url, "is1-ssl.mzstatic.com/image/thumb", "a5.mzstatic.com/us/r1000/0", 1)
		url = url[:strings.LastIndex(url, "/")]
	}

	tmpFile, err := fsutil.CreateOutputTemp(sanAlbumFolder, name+".tmp-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			tmpFile.Close()
			os.Remove(tmpPath)
		}
	}()

	if err := r.getCover(url, tmpFile); err != nil {
		if r.Config.Metadata.Artwork.Format != "original" {
			return "", err
		}
		splitByDot := strings.Split(originalUrl, ".")
		last := splitByDot[len(splitByDot)-1]
		fallback := originalUrl[:len(originalUrl)-len(last)] + ext
		fallback = strings.Replace(fallback, "{w}x{h}", r.Config.Metadata.Artwork.Size, 1)
		if err := r.getCover(fallback, tmpFile); err != nil {
			return "", err
		}
	}

	if err := tmpFile.Close(); err != nil {
		return "", err
	}
	if _, err := updateAssetFromTemp(tmpPath, covPath); err != nil {
		return "", err
	}
	if r.coverRefreshAttempts == nil {
		r.coverRefreshAttempts = make(map[string]bool)
	}
	r.coverRefreshAttempts[refreshKey] = true
	return covPath, nil
}

func (r *Runner) getCover(url string, out *os.File) error {
	if err := out.Truncate(0); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	resp, err := download.GetWithHeaders(url, map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

func (r *Runner) writeLyrics(sanAlbumFolder, filename string, lrc string, track *model.Track) error {
	lyricspath := filepath.Join(sanAlbumFolder, filename)
	if strings.EqualFold(filepath.Ext(filename), ".ttml") {
		masterPath, shared, err := r.sharedLyricsPath(sanAlbumFolder, track)
		if err != nil {
			return err
		}
		if shared {
			if _, err := os.Lstat(lyricspath); err == nil && !r.wantsRefresh("Lyrics") {
				if _, err := os.Stat(lyricspath); err != nil {
					return fmt.Errorf("existing lyrics link is unavailable: %w", err)
				}
				return nil
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
			dir := filepath.Dir(masterPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			var writeErr error
			if r.wantsRefresh("Lyrics") {
				writeErr = writeChangedAsset(masterPath, []byte(lrc))
			} else {
				writeErr = writeFileIfAbsent(masterPath, []byte(lrc))
			}
			if err := writeErr; err != nil {
				return err
			}
			if r.wantsRefresh("Lyrics") {
				if info, err := os.Lstat(lyricspath); err == nil && info.Mode().IsRegular() {
					_, err = updateAssetFromTemp(masterPath, lyricspath)
					return err
				} else if err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			return linkSharedFile(masterPath, lyricspath)
		}
	}
	if r.wantsRefresh("Lyrics") {
		return writeChangedAsset(lyricspath, []byte(lrc))
	}
	f, err := os.Create(lyricspath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(lrc)
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) writeM3UPlaylist(folderPath string, name string, tracks []AddedTrack) error {
	if !r.Flags.SaveM3U8 {
		return nil
	}
	m3uPath := filepath.Join(folderPath, sanitizeFolderName(name)+".m3u8")
	master, shared, err := r.sharedAssetDir(folderPath)
	if err != nil {
		return err
	}
	var content bytes.Buffer
	fmt.Fprintln(&content, "#EXTM3U")
	for _, track := range tracks {
		fmt.Fprintf(&content, "#EXTINF:-1,%s - %s\n", track.Artist, track.Song)
		rel, err := filepath.Rel(folderPath, track.Path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			fmt.Fprintln(&content, filepath.ToSlash(rel))
		} else {
			fmt.Fprintln(&content, filepath.Base(track.Path))
		}
	}
	if !shared {
		return writeChangedAsset(m3uPath, content.Bytes())
	}
	outputDir, err := filepath.Abs(folderPath)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(outputDir))
	playlistDir := filepath.Join(master, "Playlists", fmt.Sprintf("%x", digest[:12]))
	if err := os.MkdirAll(playlistDir, 0755); err != nil {
		return err
	}
	masterPath := filepath.Join(playlistDir, filepath.Base(m3uPath))
	info, err := os.Lstat(m3uPath)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		linked, resolveErr := filepath.EvalSymlinks(m3uPath)
		if resolveErr != nil {
			return resolveErr
		}
		masterRoot, resolveErr := filepath.EvalSymlinks(filepath.Join(master, "Playlists"))
		if resolveErr != nil {
			return resolveErr
		}
		linked, resolveErr = filepath.Abs(linked)
		if resolveErr != nil {
			return resolveErr
		}
		masterRoot, resolveErr = filepath.Abs(masterRoot)
		if resolveErr != nil {
			return resolveErr
		}
		if !pathWithin(masterRoot, linked) {
			return fmt.Errorf("existing playlist link does not point into Master: %s", m3uPath)
		}
		return writeChangedAsset(linked, content.Bytes())
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && info.Mode().IsRegular() {
		return writeChangedAsset(m3uPath, content.Bytes())
	}
	if err == nil {
		return fmt.Errorf("existing playlist is not a regular file: %s", m3uPath)
	}
	if err := writeChangedAsset(masterPath, content.Bytes()); err != nil {
		return err
	}
	return linkSharedFile(masterPath, m3uPath)
}

func firstGenre(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}

func (r *Runner) writeMP4Tags(track *model.Track, lrc string, credits ampapi.SongCredits) error {
	t := &mp4tag.MP4Tags{
		Title:  track.Resp.Attributes.Name,
		Artist: track.Resp.Attributes.ArtistName,
		Custom: map[string]string{
			"PERFORMER":   track.Resp.Attributes.ArtistName,
			"RELEASETIME": track.Resp.Attributes.ReleaseDate,
			"ISRC":        track.Resp.Attributes.Isrc,
			"LABEL":       "",
			"UPC":         "",
		},
		Composer:    track.Resp.Attributes.ComposerName,
		CustomGenre: firstGenre(track.Resp.Attributes.GenreNames),
		Lyrics:      lrc,
		TrackNumber: int16(track.Resp.Attributes.TrackNumber),
		DiscNumber:  int16(track.Resp.Attributes.DiscNumber),
		Album:       track.Resp.Attributes.AlbumName,
	}

	for role, names := range credits {
		if len(names) == 0 {
			continue
		}
		key := role
		for existing := range t.Custom {
			if strings.EqualFold(existing, key) {
				// Keep existing custom fields intact, including when only case differs.
				key = "Credit:" + role
				break
			}
		}
		if len(names) == 1 {
			t.Custom[key] = names[0]
		} else {
			t.Custom[key] = strings.Join(names[:len(names)-1], ", ") + " & " + names[len(names)-1]
		}
	}

	if r.Config.Metadata.Artwork.Embed && track.CoverPath != "" {
		cover, err := os.ReadFile(track.CoverPath)
		if err != nil {
			return fmt.Errorf("read cover: %w", err)
		}
		t.Pictures = []*mp4tag.MP4Picture{{
			Format: mp4tag.ImageTypeAuto,
			Data:   cover,
		}}
	}

	if r.Config.Metadata.Tags.SortOrder {
		t.TitleSort = track.Resp.Attributes.Name
		t.ArtistSort = track.Resp.Attributes.ArtistName
		t.ComposerSort = track.Resp.Attributes.ComposerName
		t.AlbumSort = track.Resp.Attributes.AlbumName
	}

	if r.Config.Metadata.Tags.ItunesID {
		if track.PreType == "albums" {
			albumID, err := strconv.ParseUint(track.PreID, 10, 64)
			if err != nil {
				return err
			}
			t.ItunesAlbumID = int32(albumID)
		}

		if len(track.Resp.Relationships.Artists.Data) > 0 {
			artistID, err := strconv.ParseUint(track.Resp.Relationships.Artists.Data[0].ID, 10, 64)
			if err != nil {
				return err
			}
			t.ItunesArtistID = int32(artistID)
		}
	}

	if (track.PreType == "playlists" || track.PreType == "stations") && !r.Config.Metadata.Tags.UseSongInfoForPlaylist {
		t.DiscNumber = 1
		t.DiscTotal = 1
		t.TrackNumber = int16(track.TaskNum)
		t.TrackTotal = int16(track.TaskTotal)
		t.Album = track.PlaylistData.Attributes.Name
		t.AlbumArtist = track.PlaylistData.Attributes.ArtistName
		if r.Config.Metadata.Tags.SortOrder {
			t.AlbumSort = track.PlaylistData.Attributes.Name
			t.AlbumArtistSort = track.PlaylistData.Attributes.ArtistName
		}
	} else if (track.PreType == "playlists" || track.PreType == "stations") && r.Config.Metadata.Tags.UseSongInfoForPlaylist {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.Attributes.TrackCount)
		t.AlbumArtist = track.AlbumData.Attributes.ArtistName
		t.Custom["UPC"] = track.AlbumData.Attributes.Upc
		t.Custom["LABEL"] = track.AlbumData.Attributes.RecordLabel
		t.Date = track.AlbumData.Attributes.ReleaseDate
		t.Copyright = track.AlbumData.Attributes.Copyright
		t.Publisher = track.AlbumData.Attributes.RecordLabel
		if r.Config.Metadata.Tags.SortOrder {
			t.AlbumArtistSort = track.AlbumData.Attributes.ArtistName
		}
	} else {
		t.DiscTotal = int16(track.DiscTotal)
		t.TrackTotal = int16(track.AlbumData.Attributes.TrackCount)
		t.AlbumArtist = track.AlbumData.Attributes.ArtistName
		t.Custom["UPC"] = track.AlbumData.Attributes.Upc
		t.Date = track.AlbumData.Attributes.ReleaseDate
		t.Copyright = track.AlbumData.Attributes.Copyright
		t.Publisher = track.AlbumData.Attributes.RecordLabel
		if r.Config.Metadata.Tags.SortOrder {
			t.AlbumArtistSort = track.AlbumData.Attributes.ArtistName
		}
	}

	if track.Resp.Attributes.ContentRating == "explicit" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryExplicit
	} else if track.Resp.Attributes.ContentRating == "clean" {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryClean
	} else {
		t.ItunesAdvisory = mp4tag.ItunesAdvisoryNone
	}

	mp4, err := mp4tag.Open(track.SavePath)
	if err != nil {
		return err
	}
	defer mp4.Close()
	mp4.UpperCustom(false)
	var deleteTags []string
	if len(t.Pictures) > 0 {
		existing, err := mp4.Read()
		if err != nil {
			return fmt.Errorf("read existing artwork: %w", err)
		}
		for i := range existing.Pictures {
			deleteTags = append(deleteTags, fmt.Sprintf("picture:%d", i+1))
		}
	}
	err = mp4.Write(t, deleteTags)
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) extractMvAudio(c string) (string, error) {
	mediaURL, err := url.Parse(c)
	if err != nil {
		return "", err
	}

	resp, err := download.Get(c)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	from, listType, err := m3u8.DecodeFrom(strings.NewReader(string(body)), true)
	if err != nil || listType != m3u8.MASTER {
		return "", errors.New("m3u8 not of media type")
	}

	master := from.(*m3u8.MasterPlaylist)

	type audioStream struct {
		url     string
		groupID string
		score   int
		rank    int
	}
	var streams []audioStream
	rankRe := regexp.MustCompile(`_gr(\d+)_`)

	for _, variant := range master.Variants {
		for _, alt := range variant.Alternatives {
			if alt.URI == "" {
				continue
			}
			score := audioScore(alt.GroupId, r.Config.Media.MV.AudioType)
			if score < 0 {
				continue
			}
			var rank int
			if m := rankRe.FindStringSubmatch(alt.URI); len(m) == 2 {
				rank, _ = strconv.Atoi(m[1])
			}
			streamURL, _ := mediaURL.Parse(alt.URI)
			streams = append(streams, audioStream{
				url:     streamURL.String(),
				groupID: alt.GroupId,
				score:   score,
				rank:    rank,
			})
		}
	}

	if len(streams) == 0 {
		return "", errors.New("no suitable audio stream found")
	}

	sort.Slice(streams, func(i, j int) bool {
		if streams[i].score != streams[j].score {
			return streams[i].score > streams[j].score
		}
		return streams[i].rank > streams[j].rank
	})
	fmt.Println("Audio: " + streams[0].groupID)
	return streams[0].url, nil
}

// audioScore rates an audio group ID by quality. Higher is better; -1 means
// the format is not allowed for the selected mv-audio-type.
func audioScore(groupID, preferredType string) int {
	switch {
	case groupID == "audio-atmos":
		if preferredType == "ac3" || preferredType == "aac" {
			return -1
		}
		return 10000
	case groupID == "audio-ac3":
		if preferredType == "aac" {
			return -1
		}
		if preferredType == "ac3" {
			return 10000
		}
		return 9000
	case strings.HasPrefix(groupID, "audio-stereo-"):
		// LC-AAC stereo, e.g. audio-stereo-256.
		return 8000 + parseAudioBitrate(groupID, "audio-stereo-")
	case strings.HasPrefix(groupID, "audio-HE-stereo-"):
		// HE-AAC stereo, e.g. audio-HE-stereo-64.
		return 7000 + parseAudioBitrate(groupID, "audio-HE-stereo-")
	case strings.HasPrefix(groupID, "audio-HE2-stereo-"):
		// HE-AAC v2 stereo, e.g. audio-HE2-stereo-32.
		return 6000 + parseAudioBitrate(groupID, "audio-HE2-stereo-")
	default:
		return -1
	}
}

func parseAudioBitrate(groupID, prefix string) int {
	bitrate, err := strconv.Atoi(strings.TrimPrefix(groupID, prefix))
	if err != nil {
		return 0
	}
	return bitrate
}

func (r *Runner) checkM3u8(b string, f string) (string, error) {
	EnhancedHls, err := wrapper.GetM3U8(r.Config.General.LiteServer, b)
	if err != nil {
		if errors.Is(err, wrapper.ErrNotConfigured) {
			return "", err
		}
		fmt.Println("Error connecting to lite-server:", err)
		return "", err
	}
	if f == "song" {
		if EnhancedHls != "" {
			fmt.Println("Received URL:", EnhancedHls)
		} else {
			fmt.Println("Received an empty response")
		}
	}
	return EnhancedHls, nil
}

func formatAvailability(available bool, quality string) string {
	if !available {
		return "Not Available"
	}
	return quality
}

func (r *Runner) extractMedia(b string, more_mode bool) (string, string, error) {
	masterUrl, err := url.Parse(b)
	if err != nil {
		return "", "", err
	}
	resp, err := download.Get(b)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", errors.New(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", "", errors.New("m3u8 not of master type")
	}
	master := from.(*m3u8.MasterPlaylist)
	var streamUrl *url.URL
	sort.Slice(master.Variants, func(i, j int) bool {
		return master.Variants[i].AverageBandwidth > master.Variants[j].AverageBandwidth
	})
	if r.Flags.Debug && more_mode {
		fmt.Println("\nDebug: All Available Variants:")
		var data [][]string
		for _, variant := range master.Variants {
			data = append(data, []string{variant.Codecs, variant.Audio, fmt.Sprint(variant.Bandwidth)})
		}
		table := tablewriter.NewWriter(os.Stdout)
		table.SetHeader([]string{"Codec", "Audio", "Bandwidth"})
		table.SetAutoMergeCells(true)
		table.SetRowLine(true)
		table.AppendBulk(data)
		table.Render()

		var hasAAC, hasLossless, hasHiRes, hasAtmos, hasDolbyAudio bool
		var aacQuality, losslessQuality, hiResQuality, atmosQuality, dolbyAudioQuality string

		for _, variant := range master.Variants {
			if variant.Codecs == "mp4a.40.2" { // AAC
				hasAAC = true
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitrate, _ := strconv.Atoi(split[2])
					currentBitrate := 0
					if aacQuality != "" {
						current := strings.Split(aacQuality, " | ")[2]
						current = strings.Split(current, " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						aacQuality = fmt.Sprintf("AAC | 2 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") { // Dolby Atmos
				hasAtmos = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrateStr := split[len(split)-1]
					if len(bitrateStr) == 4 && bitrateStr[0] == '2' {
						bitrateStr = bitrateStr[1:]
					}
					bitrate, _ := strconv.Atoi(bitrateStr)
					currentBitrate := 0
					if atmosQuality != "" {
						current := strings.Split(strings.Split(atmosQuality, " | ")[2], " ")[0]
						currentBitrate, _ = strconv.Atoi(current)
					}
					if bitrate > currentBitrate {
						atmosQuality = fmt.Sprintf("E-AC-3 | 16 Channel | %d Kbps", bitrate)
					}
				}
			} else if variant.Codecs == "alac" { // ALAC (Lossless or Hi-Res)
				split := strings.Split(variant.Audio, "-")
				if len(split) >= 3 {
					bitDepth := split[len(split)-1]
					sampleRate := split[len(split)-2]
					sampleRateInt, _ := strconv.Atoi(sampleRate)
					if sampleRateInt > 48000 { // Hi-Res
						hasHiRes = true
						hiResQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					} else { // Standard Lossless
						hasLossless = true
						losslessQuality = fmt.Sprintf("ALAC | 2 Channel | %s-bit/%d kHz", bitDepth, sampleRateInt/1000)
					}
				}
			} else if variant.Codecs == "ac-3" { // Dolby Audio
				hasDolbyAudio = true
				split := strings.Split(variant.Audio, "-")
				if len(split) > 0 {
					bitrate, _ := strconv.Atoi(split[len(split)-1])
					dolbyAudioQuality = fmt.Sprintf("AC-3 |  16 Channel | %d Kbps", bitrate)
				}
			}
		}

		fmt.Println("Available Audio Formats:")
		fmt.Println("------------------------")
		fmt.Printf("AAC             : %s\n", formatAvailability(hasAAC, aacQuality))
		fmt.Printf("Lossless        : %s\n", formatAvailability(hasLossless, losslessQuality))
		fmt.Printf("Hi-Res Lossless : %s\n", formatAvailability(hasHiRes, hiResQuality))
		fmt.Printf("Dolby Atmos     : %s\n", formatAvailability(hasAtmos, atmosQuality))
		fmt.Printf("Dolby Audio     : %s\n", formatAvailability(hasDolbyAudio, dolbyAudioQuality))
		fmt.Println("------------------------")

		fmt.Printf("%+v\n", r.Config)
		fmt.Println("===== SELECTOR =====")
		for _, variant := range master.Variants {
			fmt.Printf("Codec=%q Audio=%q AvgBW=%d BW=%d\n",
				variant.Codecs,
				variant.Audio,
				variant.AverageBandwidth,
				variant.Bandwidth,
			)
		}
		fmt.Printf("r.Flags.Atmos=%v r.Flags.AAC=%v AlacMax=%d\n",
			r.Flags.Atmos,
			r.Flags.AAC,
			r.Config.Media.AlacMax,
		)
		fmt.Println("====================")

		return "", "", nil
	}
	var Quality string
	for _, variant := range master.Variants {
		if r.Flags.Atmos {
			if variant.Codecs == "ec-3" && strings.Contains(variant.Audio, "atmos") {
				if r.Flags.Debug && !more_mode {
					fmt.Printf("Debug: Found Dolby Atmos variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-1])
				if err != nil {
					return "", "", err
				}
				if length_int <= r.Config.Media.AtmosMax {
					if !r.Flags.Debug && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
					break
				}
			} else if variant.Codecs == "ac-3" { // Add Dolby Audio support
				if r.Flags.Debug && !more_mode {
					fmt.Printf("Debug: Found Dolby Audio variant - %s (Bitrate: %d Kbps)\n",
						variant.Audio, variant.Bandwidth/1000)
				}
				streamUrlTemp, err := masterUrl.Parse(variant.URI)
				if err != nil {
					return "", "", err
				}
				streamUrl = streamUrlTemp
				split := strings.Split(variant.Audio, "-")
				Quality = fmt.Sprintf("%s Kbps", split[len(split)-1])
				break
			}
		} else if r.Flags.AAC {
			if variant.Codecs == "mp4a.40.2" {
				if r.Flags.Debug && !more_mode {
					fmt.Printf("Debug: Found AAC variant - %s (Bitrate: %d)\n", variant.Audio, variant.Bandwidth)
				}
				aacregex := regexp.MustCompile(`audio-stereo-\d+`)
				replaced := aacregex.ReplaceAllString(variant.Audio, "aac")
				if replaced == r.Config.Media.AacType {
					if !r.Flags.Debug && !more_mode {
						fmt.Printf("%s\n", variant.Audio)
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					split := strings.Split(variant.Audio, "-")
					Quality = fmt.Sprintf("%s Kbps", split[2])
					break
				}
			}
		} else {
			if variant.Codecs == "alac" {
				split := strings.Split(variant.Audio, "-")
				length := len(split)
				length_int, err := strconv.Atoi(split[length-2])
				if err != nil {
					return "", "", err
				}
				max := r.Config.Media.AlacMax
				if max == 0 {
					max = 192000
				}
				if length_int <= r.Config.Media.AlacMax {
					if !r.Flags.Debug && !more_mode {
						fmt.Printf("%s-bit / %s Hz\n", split[length-1], split[length-2])
					}
					streamUrlTemp, err := masterUrl.Parse(variant.URI)
					if err != nil {
						return "", "", err
					}
					streamUrl = streamUrlTemp
					KHZ := float64(length_int) / 1000.0
					Quality = fmt.Sprintf("%sB-%.1fkHz", split[length-1], KHZ)
					break
				}
			}
		}
	}
	if streamUrl == nil {
		return "", "", errors.New("no codec found")
	}
	return streamUrl.String(), Quality, nil
}

func (r *Runner) extractVideo(c string) (string, error) {
	streamURL, _, err := r.extractVideoVariant(c)
	return streamURL, err
}

func (r *Runner) extractVideoVariant(c string) (string, bool, error) {
	MediaUrl, err := url.Parse(c)
	if err != nil {
		return "", false, err
	}

	resp, err := download.Get(c)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false, errors.New(resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}
	videoString := string(body)

	from, listType, err := m3u8.DecodeFrom(strings.NewReader(videoString), true)
	if err != nil || listType != m3u8.MASTER {
		return "", false, errors.New("m3u8 not of media type")
	}

	video := from.(*m3u8.MasterPlaylist)

	re := regexp.MustCompile(`_(\d+)x(\d+)`)

	var streamUrl *url.URL
	var selectedVariant *m3u8.Variant
	sort.Slice(video.Variants, func(i, j int) bool {
		return video.Variants[i].AverageBandwidth > video.Variants[j].AverageBandwidth
	})

	maxHeight := r.Config.Media.MV.Max

	for _, variant := range video.Variants {
		matches := re.FindStringSubmatch(variant.URI)
		if len(matches) == 3 {
			height := matches[2]
			var h int
			_, err := fmt.Sscanf(height, "%d", &h)
			if err != nil {
				continue
			}
			if h <= maxHeight {
				streamUrl, err = MediaUrl.Parse(variant.URI)
				if err != nil {
					return "", false, err
				}
				selectedVariant = variant
				fmt.Println("Video: " + variant.Resolution + "-" + variant.VideoRange)
				break
			}
		}
	}

	if streamUrl == nil || selectedVariant == nil {
		return "", false, errors.New("no suitable video stream found")
	}

	usePlayReady := strings.Contains(strings.ToUpper(streamAllowedCPC(videoString, selectedVariant.URI)), "SL3000")
	return streamUrl.String(), usePlayReady, nil
}

func streamAllowedCPC(masterPlaylist string, variantURI string) string {
	var attrs map[string]string
	for _, rawLine := range strings.Split(masterPlaylist, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			attrs = m3u8.DecodeAttributeList(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			continue
		}
		if attrs == nil || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == variantURI {
			return attrs["ALLOWED-CPC"]
		}
		attrs = nil
	}
	return ""
}

// existingCoverPath finds a previously downloaded cover without fetching one.
func existingCoverPath(dir string) string {
	for _, ext := range []string{"jpg", "jpeg", "png", "webp"} {
		path := filepath.Join(dir, "cover."+ext)
		if exists, _ := fileExists(path); exists {
			return path
		}
	}
	return ""
}
