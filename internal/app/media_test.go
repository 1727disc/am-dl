package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amdl/internal/config"
)

func TestStreamAllowedCPC(t *testing.T) {
	const master = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=1920x1080,ALLOWED-CPC="com.microsoft.playready:SL2000,urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed:WIDEVINE_HARDWARE"
video_1080.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000,RESOLUTION=3840x2160,ALLOWED-CPC="com.apple.streamingkeydelivery:Main/AppleMain,com.microsoft.playready:SL3000"
video_2160.m3u8
`

	if got := streamAllowedCPC(master, "video_1080.m3u8"); got != "com.microsoft.playready:SL2000,urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed:WIDEVINE_HARDWARE" {
		t.Fatalf("unexpected 1080p ALLOWED-CPC: %q", got)
	}
	if got := streamAllowedCPC(master, "video_2160.m3u8"); got != "com.apple.streamingkeydelivery:Main/AppleMain,com.microsoft.playready:SL3000" {
		t.Fatalf("unexpected 2160p ALLOWED-CPC: %q", got)
	}
	if got := streamAllowedCPC(master, "missing.m3u8"); got != "" {
		t.Fatalf("expected missing variant to have empty ALLOWED-CPC, got %q", got)
	}
}

func TestWriteM3UPlaylist(t *testing.T) {
	tempDir := t.TempDir()
	r := NewRunner(config.ConfigSet{})
	r.Flags.SaveM3U8 = true

	tracks := []AddedTrack{
		{
			Path:   filepath.Join(tempDir, "01. Song A.m4a"),
			Artist: "Artist A",
			Song:   "Song A",
		},
		{
			Path:   filepath.Join(tempDir, "02. Song B.flac"),
			Artist: "Artist B",
			Song:   "Song B",
		},
	}

	err := r.writeM3UPlaylist(tempDir, "My Playlist", tracks)
	if err != nil {
		t.Fatalf("writeM3UPlaylist failed: %v", err)
	}

	m3uPath := filepath.Join(tempDir, "My Playlist.m3u8")
	data, err := os.ReadFile(m3uPath)
	if err != nil {
		t.Fatalf("failed to read created m3u8 file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "#EXTM3U") {
		t.Fatalf("missing #EXTM3U in content: %s", content)
	}
	if !strings.Contains(content, "#EXTINF:-1,Artist A - Song A") || !strings.Contains(content, "01. Song A.m4a") {
		t.Fatalf("unexpected content for track 1: %s", content)
	}
	if !strings.Contains(content, "#EXTINF:-1,Artist B - Song B") || !strings.Contains(content, "02. Song B.flac") {
		t.Fatalf("unexpected content for track 2: %s", content)
	}
}

func TestSaveM3UPlaylist(t *testing.T) {
	tempDir := t.TempDir()
	r := NewRunner(config.ConfigSet{})

	// 1. When SaveM3U8 is false, no file should be created
	r.Flags.SaveM3U8 = false
	r.State.AddedTracks = []AddedTrack{
		{Path: filepath.Join(tempDir, "01. First.m4a"), Artist: "Artist 1", Song: "First"},
	}
	r.saveM3UPlaylist(tempDir, "Disabled", 0)
	if _, err := os.Stat(filepath.Join(tempDir, "Disabled.m3u8")); !os.IsNotExist(err) {
		t.Fatalf("expected m3u8 file to not exist when SaveM3U8=false")
	}

	// 2. When SaveM3U8 is true, only tracks from startIdx onwards should be saved
	r.Flags.SaveM3U8 = true
	startIdx := len(r.State.AddedTracks) // startIdx = 1

	r.State.AddedTracks = append(r.State.AddedTracks,
		AddedTrack{Path: filepath.Join(tempDir, "02. Second.m4a"), Artist: "Artist 2", Song: "Second"},
		AddedTrack{Path: filepath.Join(tempDir, "03. Third.m4a"), Artist: "Artist 3", Song: "Third"},
	)

	r.saveM3UPlaylist(tempDir, "Album Name", startIdx)
	m3uPath := filepath.Join(tempDir, "Album Name.m3u8")
	data, err := os.ReadFile(m3uPath)
	if err != nil {
		t.Fatalf("failed to read m3u8 file: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "01. First.m4a") {
		t.Fatalf("m3u8 should not contain track before startIdx, got: %s", content)
	}
	if !strings.Contains(content, "02. Second.m4a") || !strings.Contains(content, "03. Third.m4a") {
		t.Fatalf("m3u8 missing tracks after startIdx, got: %s", content)
	}
}

func TestSharedM3UPlaylistsUseTheirOwnQuality(t *testing.T) {
	requireSymlinks(t)
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Symlinking = true
	cfg.Paths.Master = filepath.Join(root, "Master")
	r := NewRunner(cfg)
	r.Flags.SaveM3U8 = true
	master := filepath.Join(cfg.Paths.Master, "Artist", "Album")
	var targets []string
	for _, quality := range []string{"Lossless", "AAC", "Atmos"} {
		dir := filepath.Join(root, quality, "Artist", "Album")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := r.registerSharedAssetDir(dir, master); err != nil {
			t.Fatal(err)
		}
		track := filepath.Join(dir, "01. Song.m4a")
		if err := os.WriteFile(track, []byte(quality), 0644); err != nil {
			t.Fatal(err)
		}
		if err := r.writeM3UPlaylist(dir, "Album", []AddedTrack{{Path: track, Song: "Song"}}); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "Album.m3u8")
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a link: %v", quality, err)
		}
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
		if err := r.writeM3UPlaylist(dir, "Album", []AddedTrack{{Path: track, Song: "Song"}}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(target)
		if err != nil || !strings.Contains(string(data), "01. Song.m4a") || strings.Contains(string(data), filepath.ToSlash(root)) {
			t.Fatalf("%s playlist: %q, %v", quality, data, err)
		}
	}
	if targets[0] == targets[1] || targets[1] == targets[2] || targets[0] == targets[2] {
		t.Fatalf("quality playlists share a file: %v", targets)
	}
}

func TestAlbumVersioningKeepsExistingAndRetriesSameFolder(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "Album")
	if err := os.Mkdir(original, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(original, "track.m4a")
	if err := os.WriteFile(marker, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(config.Default())
	version, err := r.prepareAlbumFolder(base, "Album", true)
	if err != nil {
		t.Fatal(err)
	}
	if version == original || !strings.HasPrefix(filepath.Base(version), "Album [") {
		t.Fatalf("unexpected version folder: %s", version)
	}
	retry, err := r.prepareAlbumFolder(base, "Album", true)
	if err != nil || retry != version {
		t.Fatalf("retry = %q, %v; want %q", retry, err, version)
	}
	next, err := NewRunner(config.Default()).prepareAlbumFolder(base, "Album", true)
	if err != nil || next == version || next == original {
		t.Fatalf("second version = %q, %v", next, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "original" {
		t.Fatalf("original changed: %q, %v", data, err)
	}
	plain, err := r.prepareAlbumFolder(base, "Album", false)
	if err != nil || plain != original {
		t.Fatalf("unversioned = %q, %v", plain, err)
	}
}
