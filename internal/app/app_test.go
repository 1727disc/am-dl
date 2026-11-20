package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"amdl/internal/download"
	"amdl/internal/model"

	"github.com/itouakirai/go-mp4tag"
)

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

func TestWriteCoverReusesExisting(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(existing, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	originalClient := download.Client
	t.Cleanup(func() {
		download.Client = originalClient
	})
	r := NewRunner(config.ConfigSet{})
	r.Config.Metadata.Artwork.Format = "jpg"
	r.Config.Metadata.Artwork.Size = "600x600"
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	download.Client = server.Client()

	path, err := r.writeCover(dir, "cover", server.URL+"/image/{w}x{h}.jpg")
	if err != nil || path != existing {
		t.Fatalf("writeCover() = %q, %v; want %q, nil", path, err, existing)
	}
	if requested {
		t.Fatal("writeCover() requested an existing cover")
	}
	data, readErr := os.ReadFile(existing)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("existing cover changed to %q", string(data))
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
