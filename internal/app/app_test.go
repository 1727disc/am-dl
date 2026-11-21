package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ampapi "amdl/internal/amp-api"
	"amdl/internal/config"
	"amdl/internal/download"
	"amdl/internal/model"

	"github.com/itouakirai/go-mp4tag"
)

type searchRoundTripFunc func(*http.Request) (*http.Response, error)

func (f searchRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
}

func TestSharedAssetPaths(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Symlinking = true
	cfg.Paths.Master = root
	r := NewRunner(cfg)
	if err := r.registerSharedAssetDir(filepath.Join(root, "Lossless"), filepath.Join(root, "Master", "Artist")); err == nil {
		t.Fatal("Master overlapping an output folder must be rejected")
	}
	name := assetFolderName(strings.Repeat("歌", 200), "12345")
	if len(name) > 210 || !strings.Contains(name, "[12345]") {
		t.Fatalf("unexpected long asset folder name: %q (%d bytes)", name, len(name))
	}
}

func TestSharedAssetsAcrossCodecs(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	master := filepath.Join(root, "Master")
	coverRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		coverRequests++
		_, _ = w.Write([]byte("cover bytes"))
	}))
	defer server.Close()
	original := download.Client
	t.Cleanup(func() { download.Client = original })
	download.Client = server.Client()
	cfg := config.Default()
	cfg.Paths.Symlinking, cfg.Paths.Master = true, master
	cfg.Metadata.Artwork.Format = "jpg"
	cfg.Metadata.Lyrics.Format = "ttml"
	r := NewRunner(cfg)
	masterArtist := filepath.Join(master, "Artist [123]")
	masterRelease := filepath.Join(masterArtist, "Album [456]")
	var album ampapi.AlbumRespData
	album.Attributes.Name = "Album"
	track := &model.Track{ID: "789"}
	track.Resp.Attributes.Name = "Track"
	var sharedLyric, sharedVideo, sharedInfo, sharedArtist string
	for _, codec := range []string{"Lossless", "AAC", "Atmos"} {
		artistDir := filepath.Join(root, codec, "Artist")
		albumDir := filepath.Join(artistDir, "Album")
		if err := os.MkdirAll(albumDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := r.registerSharedAssetDir(artistDir, masterArtist); err != nil {
			t.Fatal(err)
		}
		if err := r.registerSharedAssetDir(albumDir, masterRelease); err != nil {
			t.Fatal(err)
		}
		if codec == "Atmos" {
			if err := os.WriteFile(filepath.Join(albumDir, "cover.jpg"), []byte("existing cover"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cover, err := r.writeCover(albumDir, "cover", server.URL+"/{w}x{h}.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if codec == "Atmos" {
			if info, err := os.Lstat(cover); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("existing cover changed: %v", err)
			}
			if data, err := os.ReadFile(cover); err != nil || string(data) != "existing cover" {
				t.Fatalf("existing cover changed: %q %v", data, err)
			}
		} else if info, err := os.Lstat(cover); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("cover is not a symlink: %v", err)
		}
		lyricName := "01. Track " + codec + ".ttml"
		if err := r.writeLyrics(albumDir, lyricName, "<tt>shared</tt>", track); err != nil {
			t.Fatal(err)
		}
		manual := filepath.Join(albumDir, "existing.ttml")
		if err := os.WriteFile(manual, []byte("existing lyrics"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.writeLyrics(albumDir, "existing.ttml", "replacement", track); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(manual); err != nil || string(data) != "existing lyrics" {
			t.Fatalf("existing lyrics changed: %q, %v", data, err)
		}
		if err := r.writeLyrics(albumDir, "local.lrc", "local lyrics", track); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(filepath.Join(albumDir, "local.lrc")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("LRC should stay local: %v", err)
		}
		if cached, found, err := r.cachedSharedLyrics(albumDir, track); err != nil || !found || cached != "<tt>shared</tt>" {
			t.Fatalf("cached lyrics %q, %v, %v", cached, found, err)
		}
		if err := r.withSharedAssets(albumDir, "release-extra", func(dir string) error { return saveFullAlbumInfo(dir, &album) }); err != nil {
			t.Fatal(err)
		}
		if err := r.withSharedAssets(albumDir, "", func(dir string) error {
			return writeFileIfAbsent(filepath.Join(dir, "square_animated_artwork.mp4"), []byte("motion"))
		}); err != nil {
			t.Fatal(err)
		}
		if err := r.withSharedAssets(filepath.Join(albumDir, "release-extra"), "", func(dir string) error {
			return writeFileIfAbsent(filepath.Join(dir, "square_preview_frame.png"), []byte("preview"))
		}); err != nil {
			t.Fatal(err)
		}
		if err := r.withSharedAssets(artistDir, "artist-extra", func(dir string) error {
			extra := filepath.Join(dir, "artist-extra")
			if err := os.MkdirAll(extra, 0755); err != nil {
				return err
			}
			for _, name := range []string{"logo.png", "biography.txt", "colors.json", "artist_wide_animated_artwork.mp4"} {
				if err := writeFileIfAbsent(filepath.Join(extra, name), []byte(name)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, check := range []struct {
			path   string
			shared *string
		}{
			{filepath.Join(albumDir, lyricName), &sharedLyric},
			{filepath.Join(albumDir, "square_animated_artwork.mp4"), &sharedVideo},
			{filepath.Join(albumDir, "release-extra", "release-info.txt"), &sharedInfo},
			{filepath.Join(artistDir, "artist-extra", "biography.txt"), &sharedArtist},
		} {
			info, err := os.Lstat(check.path)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("%s is not a symlink: %v", check.path, err)
			}
			resolved, err := filepath.EvalSymlinks(check.path)
			if err != nil {
				t.Fatal(err)
			}
			if *check.shared == "" {
				*check.shared = resolved
			} else if *check.shared != resolved {
				t.Fatalf("%s resolves to different asset", check.path)
			}
		}
	}
	if coverRequests != 1 {
		t.Fatalf("cover downloaded %d times, want once", coverRequests)
	}
}

func TestLyricsOnlyReusesMasterAcrossRuns(t *testing.T) {
	requireSymlinks(t)
	original := download.Client
	t.Cleanup(func() { download.Client = original })
	lyricsRequests := 0
	download.Client = &http.Client{Transport: searchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/v1/catalog/us/albums/123":
			body = `{"data":[{"attributes":{"name":"Album","artistName":"Artist"},"relationships":{"tracks":{"data":[{"id":"111","type":"songs","attributes":{"name":"Track","artistName":"Artist","trackNumber":1,"discNumber":1}}]}}}]}`
		case "/lyrics":
			lyricsRequests++
			body = `{"code":0,"data":{"lyrics":"<tt>shared</tt>"}}`
		default:
			t.Errorf("unexpected request: %s", req.URL.Path)
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Symlinking = true
	cfg.Paths.Master = filepath.Join(root, "Master")
	cfg.Paths.Alac, cfg.Paths.Aac = filepath.Join(root, "Lossless"), filepath.Join(root, "AAC")
	cfg.Paths.ArtistFolder, cfg.Paths.AlbumFolder, cfg.Paths.SongFile = "{ArtistName}", "{AlbumName}", "{SongNumber}. {SongName}"
	cfg.General.LiteServer = "http://wrapper.test"
	cfg.Metadata.Lyrics.Format = "ttml"
	for _, aac := range []bool{false, true} {
		r := NewRunner(cfg)
		r.Flags.LyricOnly, r.Flags.AAC = true, aac
		if err := r.ripAlbum("123", "test-token", "us", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for _, codec := range []string{"Lossless", "AAC"} {
		path := filepath.Join(root, codec, "Artist", "Album", "01. Track.ttml")
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink: %v", path, err)
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = target
		} else if first != target {
			t.Fatalf("different Master lyrics: %s and %s", first, target)
		}
	}
	if lyricsRequests != 1 {
		t.Fatalf("lyrics requested %d times, want once", lyricsRequests)
	}
}

func TestDeduplicateMotionFiles(t *testing.T) {
	dir := t.TempDir()
	content := bytes.Repeat([]byte("motion artwork"), 20000)
	different := append([]byte(nil), content...)
	different[len(different)-1] ^= 1
	files := map[string][]byte{
		"square_preview_frame.png":       content,
		"square_video_preview_frame.png": content,
		"tall_preview_frame.png":         different,
		"cover.png":                      content,
		"song.m4a":                       content,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := deduplicateMotionFiles(dir, "_preview_frame.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "square_video_preview_frame.png")); !os.IsNotExist(err) {
		t.Fatalf("duplicate remains: %v", err)
	}
	for _, name := range []string{"square_preview_frame.png", "tall_preview_frame.png", "cover.png", "song.m4a"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was not preserved: %v", name, err)
		}
	}
	temporary := filepath.Join(dir, "download.tmp")
	if err := os.WriteFile(temporary, content, 0600); err != nil {
		t.Fatal(err)
	}
	duplicate, err := duplicateMotionFile(temporary, "_preview_frame.png")
	if err != nil || filepath.Base(duplicate) != "square_preview_frame.png" {
		t.Fatalf("duplicate = %q, err = %v", duplicate, err)
	}
	if err := os.WriteFile(temporary, []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := duplicateMotionFile(temporary, "_preview_frame.png"); err != nil || duplicate != "" {
		t.Fatalf("distinct file matched %q, err = %v", duplicate, err)
	}
}

func TestMotionDuplicatesKeepShortestName(t *testing.T) {
	dir := t.TempDir()
	data := []byte("identical motion")
	longName := filepath.Join(dir, "aaaa_long_preview_frame.png")
	shortName := filepath.Join(dir, "z_preview_frame.png")
	if err := os.WriteFile(longName, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shortName, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := deduplicateMotionFiles(dir, "_preview_frame.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shortName); err != nil {
		t.Fatalf("short filename missing: %v", err)
	}
	if _, err := os.Stat(longName); !os.IsNotExist(err) {
		t.Fatalf("long filename remains: %v", err)
	}

	// A short-named download must replace an identical longer-named file.
	if err := os.Remove(shortName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(longName, data, 0600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "download.tmp")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := saveUniqueMotionFile(tmp, shortName, "_preview_frame.png")
	if err != nil || !saved {
		t.Fatalf("save shorter name: saved=%v err=%v", saved, err)
	}
	if _, err := os.Stat(shortName); err != nil {
		t.Fatalf("short filename missing: %v", err)
	}
	if _, err := os.Stat(longName); !os.IsNotExist(err) {
		t.Fatalf("long filename remains: %v", err)
	}

	// The longer name is skipped when the short copy already exists.
	saved, err = saveUniqueMotionFile(tmp, longName, "_preview_frame.png")
	if err != nil || saved {
		t.Fatalf("save longer name: saved=%v err=%v", saved, err)
	}
	if _, err := os.Stat(longName); !os.IsNotExist(err) {
		t.Fatalf("long filename was created: %v", err)
	}
}

func TestArtistMotionKeepsIdenticalVariants(t *testing.T) {
	artistDir := t.TempDir()
	dir := filepath.Join(artistDir, "artist-extra")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"artist_wide", "artist_fullscreen"} {
		for _, suffix := range []string{"_animated_artwork.mp4", "_preview_frame.png"} {
			if err := os.WriteFile(filepath.Join(dir, variant+suffix), []byte("identical"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	original := download.Client
	t.Cleanup(func() { download.Client = original })
	requests := 0
	download.Client = &http.Client{Transport: searchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		body := `{"data":[{"id":"123","attributes":{"name":"Artist"}}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	r := NewRunner(config.Default())
	r.Config.Metadata.Artwork.SaveArtistAnimated = true
	for i := 0; i < 2; i++ {
		if err := r.saveArtistDetails(artistDir, "us", "123", "unused-token", ""); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatalf("artist metadata requests = %d, want 1", requests)
	}
	for _, variant := range []string{"artist_wide", "artist_fullscreen"} {
		for _, suffix := range []string{"_animated_artwork.mp4", "_preview_frame.png"} {
			if _, err := os.Stat(filepath.Join(dir, variant+suffix)); err != nil {
				t.Errorf("%s%s was not preserved: %v", variant, suffix, err)
			}
		}
	}
}

func TestMixedTopSearch(t *testing.T) {
	original := download.Client
	t.Cleanup(func() { download.Client = original })
	download.Client = &http.Client{Transport: searchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "amp-api-edge.music.apple.com" || req.URL.Path != "/v1/catalog/jp/search" || req.URL.Query().Get("format[resources]") != "map" || req.URL.Query().Get("platform") != "web" || req.URL.Query().Get("term") != "sample music" {
			t.Errorf("unexpected top search request: %s", req.URL.String())
		}
		body := `{"results":{"top":{"data":[{"id":"1","type":"artists"},{"id":"2","type":"songs"},{"id":"ignored","type":"playlists"},{"id":"45","type":"music-videos"},{"id":"3","type":"albums"}]},"song":{"next":"/next","data":[{"id":"2","type":"songs"}]},"music_video":{"next":"/next","data":[{"id":"45","type":"music-videos"}]}},"resources":{"music-videos":{"45":{"id":"45","type":"music-videos","attributes":{"name":"Video","artistName":"Artist","contentRating":"clean","url":"https://music.apple.com/jp/music-video/video/45"}}},"artists":{"1":{"id":"1","type":"artists","attributes":{"name":"Artist","url":"https://music.apple.com/jp/artist/test/1"}}},"songs":{"2":{"id":"2","type":"songs","attributes":{"name":"Song","artistName":"Artist","albumName":"Album","contentRating":"explicit","url":"https://music.apple.com/jp/album/album/3"}}},"albums":{"3":{"id":"3","type":"albums","attributes":{"name":"Album","artistName":"Artist","contentRating":"clean","url":"https://music.apple.com/jp/album/album/3"}}}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	results, _, err := ampapi.SearchWeb("jp", "sample music", "", "ja", "test-token", 21, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}
	for i, want := range []struct{ kind, label, path string }{
		{"Artist", "[Artist] Artist", "/artist/"},
		{"Song", "[Song] Song [E]", "/song/2"},
		{"Video", "[Video] Video [C]", "/music-video/45"},
		{"Album", "[Album] Album [C]", "/album/"},
	} {
		item, label, ok := topSearchItem(results[i], "jp")
		if !ok || item.Type != want.kind || !strings.Contains(label, want.label) || !strings.Contains(item.URL, want.path) {
			t.Errorf("result %d: item=%+v label=%q ok=%v", i, item, label, ok)
		}
	}
	songs, hasNext, err := ampapi.SearchWeb("jp", "sample music", "song", "ja", "test-token", 15, 15)
	if err != nil || !hasNext || len(songs) != 1 || songs[0].ID != "2" {
		t.Fatalf("scoped search results: %+v next=%v err=%v", songs, hasNext, err)
	}
	videos, hasNext, err := ampapi.SearchWeb("jp", "sample music", "video", "ja", "test-token", 15, 15)
	if err != nil || !hasNext || len(videos) != 1 || videos[0].Type != "music-videos" {
		t.Fatalf("video search results: %+v next=%v err=%v", videos, hasNext, err)
	}
	if scope, query := parseSearchInput("video", []string{"sample"}); scope != "video" || strings.Join(query, " ") != "sample" {
		t.Fatalf("video search: %q %v", scope, query)
	}
	if scope, query := parseSearchInput("sample", []string{"music"}); scope != "" || strings.Join(query, " ") != "sample music" {
		t.Fatalf("unscoped search: %q %v", scope, query)
	}
	if scope, query := parseSearchInput("song", []string{"sample"}); scope != "song" || strings.Join(query, " ") != "sample" {
		t.Fatalf("scoped search: %q %v", scope, query)
	}
}

func TestLyricsOnlyAlbumAndPlaylist(t *testing.T) {
	original := download.Client
	t.Cleanup(func() { download.Client = original })
	var requests []string
	download.Client = &http.Client{Transport: searchRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.Path)
		var body string
		switch req.URL.Path {
		case "/v1/catalog/us/albums/123", "/v1/catalog/us/playlists/pl.test":
			body = `{"data":[{"attributes":{"name":"Collection","artistName":"Artist","artwork":{"url":"https://unexpected.test/cover.jpg"},"editorialVideo":{"motionDetailSquare":{"video":"https://unexpected.test/video.m3u8"}}},"relationships":{"tracks":{"data":[{"id":"111","type":"songs","attributes":{"name":"Track","artistName":"Artist","trackNumber":1,"discNumber":1}},{"id":"222","type":"music-videos","attributes":{"name":"Video"}}]}}}]}`
		case "/lyrics":
			if req.URL.Query().Get("adamId") != "111" {
				t.Errorf("unexpected lyrics request: %s", req.URL.RawQuery)
			}
			body = `{"code":0,"data":{"lyrics":"<tt>lyrics</tt>"}}`
		default:
			t.Errorf("unexpected media request: %s", req.URL.Path)
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Alac, cfg.Paths.ArtistFolder = root, ""
	cfg.Paths.AlbumFolder, cfg.Paths.PlaylistFolder = "Album", "Playlist"
	cfg.Paths.SongFile = "{SongNumber}. {SongName}"
	cfg.General.LiteServer = "http://wrapper.test"
	cfg.Metadata.Lyrics.Format = "ttml"
	cfg.Metadata.Lyrics.SaveFile = false
	cfg.Metadata.Extras.SaveReleaseExtras = true
	cfg.Metadata.Artwork.SaveReleaseAnimated = true
	cfg.Metadata.Artwork.SaveArtist = true
	r := NewRunner(cfg)
	r.Flags.LyricOnly = true
	albumDir := filepath.Join(root, "Album")
	if err := os.MkdirAll(albumDir, 0755); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(albumDir, "01. Track.m4a")
	if err := os.WriteFile(audioPath, []byte("existing audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.ripAlbum("123", "test-token", "us", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.ripPlaylist("pl.test", "test-token", "us", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Album", "Playlist"} {
		data, err := os.ReadFile(filepath.Join(root, name, "01. Track.ttml"))
		if err != nil || string(data) != "<tt>lyrics</tt>" {
			t.Fatalf("%s lyrics: %q, %v", name, data, err)
		}
		if _, err := os.Stat(filepath.Join(root, name, "release-extra")); !os.IsNotExist(err) {
			t.Errorf("%s: release extras unexpectedly created", name)
		}
	}
	if data, err := os.ReadFile(audioPath); err != nil || string(data) != "existing audio" {
		t.Fatalf("existing audio changed: %q, %v", data, err)
	}
	if len(requests) != 4 {
		t.Fatalf("expected only two metadata and two lyrics requests, got %v", requests)
	}
	if r.State.Counter.Success != 2 || r.State.Counter.Total != 2 {
		t.Fatalf("unexpected counters: %+v", r.State.Counter)
	}
}

func TestReleaseExtraInformation(t *testing.T) {
	var playlist ampapi.PlaylistRespData
	data := `{"attributes":{"name":"Example playlist","curatorName":"Apple Music","trackCount":2,"description":{"short":"Short note","standard":"Standard note"},"editorialVideo":{"motionDetailSquare":{"previewFrame":{"url":"https://example.test/frame/{w}x{h}.jpg","width":1200,"height":1200}}}},"relationships":{"tracks":{"data":[{"attributes":{"name":"First","durationInMillis":60000}},{"attributes":{"name":"Second","durationInMillis":90000}}]}}}`
	if err := json.Unmarshal([]byte(data), &playlist); err != nil {
		t.Fatal(err)
	}
	if playlist.Attributes.EditorialVideo.MotionDetailSquare.PreviewFrame.Width != 1200 {
		t.Fatal("playlist preview frame was not decoded")
	}
	dir := t.TempDir()
	if err := saveFullPlaylistInfo(dir, &playlist); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "release-extra", "release-info.txt")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Playlist: Example playlist", "Short note", "Standard note", "Playlist length: 2:30.000", "Disc 1", "  1 First", "  2 Second"} {
		if !bytes.Contains(contents, []byte(want)) {
			t.Errorf("playlist information missing %q: %s", want, contents)
		}
	}
	var album ampapi.AlbumRespData
	album.Attributes.Name = "Example album"
	albumDir := t.TempDir()
	if err := saveFullAlbumInfo(albumDir, &album); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(albumDir, "release-extra", "release-info.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestPlaylistTagsReplaceOriginalArtwork(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is needed to create an MP4 fixture")
	}
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "song.m4a")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "0.1", "-c:a", "aac", audioPath).CombinedOutput(); err != nil {
		t.Fatalf("create MP4: %v: %s", err, out)
	}
	makeCover := func(c color.Color) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, c)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	oldCover := makeCover(color.RGBA{R: 255, A: 255})
	playlistCover := makeCover(color.RGBA{B: 255, A: 255})
	mp4, err := mp4tag.Open(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := mp4.Write(&mp4tag.MP4Tags{TrackNumber: 4, DiscNumber: 2, Pictures: []*mp4tag.MP4Picture{{Format: mp4tag.ImageTypeAuto, Data: oldCover}}}, nil); err != nil {
		t.Fatal(err)
	}
	mp4.Close()
	coverPath := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(coverPath, playlistCover, 0600); err != nil {
		t.Fatal(err)
	}
	track := &model.Track{PreType: "playlists", TaskNum: 15, TaskTotal: 20, SavePath: audioPath, CoverPath: coverPath}
	track.Resp.Attributes.TrackNumber = 4
	track.Resp.Attributes.DiscNumber = 2
	track.PlaylistData.Attributes.Name = "Playlist"
	r := &Runner{}
	r.Config.Metadata.Artwork.Embed = true
	if err := r.writeMP4Tags(track, "", nil); err != nil {
		t.Fatal(err)
	}
	mp4, err = mp4tag.Open(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mp4.Close()
	tags, err := mp4.Read()
	if err != nil {
		t.Fatal(err)
	}
	if tags.TrackNumber != 15 || tags.TrackTotal != 20 || tags.DiscNumber != 1 || tags.DiscTotal != 1 || tags.Album != "Playlist" {
		t.Fatalf("incorrect playlist metadata: track %d/%d disc %d/%d album %q", tags.TrackNumber, tags.TrackTotal, tags.DiscNumber, tags.DiscTotal, tags.Album)
	}
	if len(tags.Pictures) != 1 || !bytes.Equal(tags.Pictures[0].Data, playlistCover) {
		t.Fatalf("expected only playlist artwork, got %d pictures", len(tags.Pictures))
	}
}

func TestTrackFileNumbersForPlaylists(t *testing.T) {
	tests := []struct {
		name, collection, pattern     string
		useSongInfo                   bool
		wantSong, wantDisc, wantTrack int
	}{
		{"playlist uses playlist position", "playlists", "{DiscNumber}-{SongNumber}-{TrackNumber}", false, 15, 1, 15},
		{"playlist uses album position", "playlists", "{DiscNumber}-{SongNumber}-{TrackNumber}", true, 4, 2, 4},
		{"station uses station position", "stations", "{DiscNumber}-{SongNumber}-{TrackNumber}", false, 15, 1, 15},
		{"album keeps disc position", "albums", "{DiscNumber}-{SongNumber}-{TrackNumber}", false, 4, 2, 4},
		{"album keeps total position without disc", "albums", "{SongNumber}-{TrackNumber}", false, 15, 2, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{}
			r.Config.Paths.SongFile = tt.pattern
			r.Config.Metadata.Tags.UseSongInfoForPlaylist = tt.useSongInfo
			track := &model.Track{PreType: tt.collection, TaskNum: 15}
			track.Resp.Attributes.DiscNumber = 2
			track.Resp.Attributes.TrackNumber = 4
			song, disc, number := r.trackFileNumbers(track)
			if song != tt.wantSong || disc != tt.wantDisc || number != tt.wantTrack {
				t.Fatalf("got song/disc/track %d/%d/%d; want %d/%d/%d", song, disc, number, tt.wantSong, tt.wantDisc, tt.wantTrack)
			}
		})
	}
}

func TestCheckURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		kind       string
		storefront string
		id         string
	}{
		{name: "album", url: "https://music.apple.com/us/album/example/1234567890", kind: "album", storefront: "us", id: "1234567890"},
		{name: "album query", url: "https://music.apple.com/jp/album/example/1234567890?i=9876543210", kind: "album", storefront: "jp", id: "1234567890"},
		{name: "song id", url: "https://music.apple.com/gb/song/id9876543210", kind: "song", storefront: "gb", id: "9876543210"},
		{name: "playlist", url: "https://music.apple.com/de/playlist/example/pl.abcdef123456", kind: "playlist", storefront: "de", id: "pl.abcdef123456"},
		{name: "station", url: "https://music.apple.com/us/station/example/ra.abcdef123456", kind: "station", storefront: "us", id: "ra.abcdef123456"},
		{name: "music video", url: "https://music.apple.com/us/music-video/example/1234567890", kind: "mv", storefront: "us", id: "1234567890"},
		{name: "artist", url: "https://music.apple.com/us/artist/example/1234567890", kind: "artist", storefront: "us", id: "1234567890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storefront, id := checkUrl(tt.url, tt.kind)
			if storefront != tt.storefront {
				t.Fatalf("storefront = %q, want %q", storefront, tt.storefront)
			}
			if id != tt.id {
				t.Fatalf("id = %q, want %q", id, tt.id)
			}
		})
	}
}

func TestAudioScore(t *testing.T) {
	tests := []struct {
		name      string
		groupID   string
		preferred string
		want      int
	}{
		{name: "atmos default", groupID: "audio-atmos", preferred: "atmos", want: 10000},
		{name: "atmos rejected for aac", groupID: "audio-atmos", preferred: "aac", want: -1},
		{name: "ac3 preferred", groupID: "audio-ac3", preferred: "ac3", want: 10000},
		{name: "stereo bitrate", groupID: "audio-stereo-256", preferred: "aac", want: 8256},
		{name: "he stereo bitrate", groupID: "audio-HE-stereo-64", preferred: "aac", want: 7064},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audioScore(tt.groupID, tt.preferred); got != tt.want {
				t.Fatalf("r.audioScore(%q, %q) = %d, want %d", tt.groupID, tt.preferred, got, tt.want)
			}
		})
	}
}

func TestReleaseYear(t *testing.T) {
	if got := releaseYear("2024-01-02"); got != "2024" {
		t.Fatalf("releaseYear = %q, want %q", got, "2024")
	}
	if got := releaseYear(""); got != "" {
		t.Fatalf("releaseYear = %q, want empty", got)
	}
}

func TestFirstGenre(t *testing.T) {
	if got := firstGenre([]string{"Pop", "Rock"}); got != "Pop" {
		t.Fatalf("firstGenre = %q, want %q", got, "Pop")
	}
	if got := firstGenre(nil); got != "" {
		t.Fatalf("firstGenre = %q, want empty", got)
	}
}

func TestFlagValueFromArgs(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--lite-server=http://localhost:10020"}, want: "http://localhost:10020"},
		{args: []string{"--lite-server", "http://localhost:10020"}, want: "http://localhost:10020"},
		{args: []string{"--lite-server"}, want: ""},
		{args: []string{"url"}, want: ""},
	}
	for _, tt := range tests {
		if got := flagValueFromArgs(tt.args, "lite-server"); got != tt.want {
			t.Fatalf("r.flagValueFromArgs(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestWriteCoverRefreshesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	originalClient := download.Client
	t.Cleanup(func() { download.Client = originalClient })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new")) }))
	defer server.Close()
	download.Client = server.Client()
	r := NewRunner(config.Default())
	r.Flags.Refresh = true
	r.refreshSelection = map[string]bool{"Covers": true}
	r.Config.Metadata.Artwork.Format = "jpg"
	path, err := r.writeCover(dir, "cover", server.URL+"/image/{w}x{h}.jpg")
	if err != nil || path != existing {
		t.Fatalf("writeCover() = %q, %v", path, err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "new" {
		t.Fatalf("cover = %q, %v", data, err)
	}
}

func TestLyricsRefreshUpdatesSharedAndExistingFiles(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Symlinking = true
	cfg.Paths.Master = filepath.Join(dir, "master")
	r := NewRunner(cfg)
	local := filepath.Join(dir, "lossless")
	if err := os.MkdirAll(local, 0755); err != nil {
		t.Fatal(err)
	}
	if err := r.registerSharedAssetDir(local, filepath.Join(cfg.Paths.Master, "release")); err != nil {
		t.Fatal(err)
	}
	track := &model.Track{ID: "123", Storefront: "us"}
	track.Resp.Attributes.Name = "Song"
	if err := r.writeLyrics(local, "song.ttml", "old", track); err != nil {
		t.Fatal(err)
	}
	r.Flags.Refresh = true
	r.refreshSelection = map[string]bool{"Lyrics": true}
	if err := r.writeLyrics(local, "song.ttml", "new", track); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(local, "song.ttml")); err != nil || string(data) != "new" {
		t.Fatalf("lyrics = %q, %v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(local, "song.ttml")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("lyrics link lost: %v", err)
	}
}

func TestAssetRefreshPreservesSymlinks(t *testing.T) {
	requireSymlinks(t)
	dir := t.TempDir()
	master := filepath.Join(dir, "master", "cover.jpg")
	if err := os.MkdirAll(filepath.Dir(master), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(master, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lossless", "cover.jpg")
	if err := linkSharedFile(master, link); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "download")
	if err := os.WriteFile(source, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := updateAssetFromTemp(source, link)
	if err != nil || !changed {
		t.Fatalf("refresh: %v, changed %v", err, changed)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link not preserved: %v", err)
	}
	if data, err := os.ReadFile(master); err != nil || string(data) != "new" {
		t.Fatalf("master = %q, %v", data, err)
	}
	changed, err = updateAssetFromTemp(source, link)
	if err != nil || changed {
		t.Fatalf("unchanged refresh: %v, changed %v", err, changed)
	}
	if _, err := updateAssetFromTemp(filepath.Join(dir, "missing"), link); err == nil {
		t.Fatal("missing download succeeded")
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != "new" {
		t.Fatalf("failed refresh damaged asset: %q, %v", data, err)
	}
}

func TestGetProgName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"amdl_windows_amd64.exe", "amdl_windows_amd64.exe"},
		{`C:\tools\amdl_windows_amd64.exe`, "amdl_windows_amd64.exe"},
		{`.\amdl.exe`, "amdl.exe"},
		{"/usr/local/bin/amdl", "amdl"},
		{"./amdl_linux_arm64", "amdl_linux_arm64"},
		{`C:\Users\qwer\AppData\Local\Temp\go-build3482394\b001\exe\main.exe`, "go run main.go"},
		{"/tmp/go-build123/b001/exe/main", "go run main.go"},
		{"", "amdl"},
		{".", "amdl"},
		{"/", "amdl"},
		{`\`, "amdl"},
		{`C:\`, "amdl"},
		{"C:amdl.exe", "amdl.exe"},
		{`\\server\share\amdl.exe`, "amdl.exe"},
		{"main.exe", "main.exe"},
		{"main", "main"},
	}

	for _, tt := range tests {
		got := getProgName(tt.input)
		if got != tt.want {
			t.Errorf("getProgName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
