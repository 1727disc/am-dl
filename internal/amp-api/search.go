package ampapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"amdl/internal/download"
)

// WebSearchResult is a song, album, artist, or music video from web catalog search.
type WebSearchResult struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Name          string   `json:"name"`
		ArtistName    string   `json:"artistName"`
		AlbumName     string   `json:"albumName"`
		ReleaseDate   string   `json:"releaseDate"`
		TrackCount    int      `json:"trackCount"`
		ContentRating string   `json:"contentRating"`
		GenreNames    []string `json:"genreNames"`
		URL           string   `json:"url"`
	} `json:"attributes"`
}

// SearchWeb follows the web player's catalog search and resolves the ordered
// result references using its resource map. An empty scope selects top results.
func SearchWeb(storefront, term, scope, language, token string, limit, offset int) ([]WebSearchResult, bool, error) {
	if token == "" {
		var err error
		token, err = GetToken()
		if err != nil {
			return nil, false, err
		}
	}
	group := "top"
	types := "activities,albums,apple-curators,artists,curators,editorial-items,music-movies,music-videos,playlists,record-labels,songs,stations,tv-episodes,uploaded-videos"
	switch scope {
	case "":
	case "song":
		group, types = "song", "songs"
	case "album":
		group, types = "album", "albums"
	case "artist":
		group, types = "artist", "artists"
	case "video":
		group, types = "music_video", "music-videos"
	default:
		return nil, false, fmt.Errorf("invalid search type: %s", scope)
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://amp-api-edge.music.apple.com/v1/catalog/%s/search", storefront), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Origin", "https://music.apple.com")
	q := url.Values{}
	q.Set("art[music-videos:url]", "c")
	q.Set("art[url]", "f")
	q.Set("extend", "artistUrl")
	q.Set("fields[albums]", "artistName,artistUrl,artwork,contentRating,editorialArtwork,editorialNotes,name,playParams,releaseDate,url,trackCount")
	q.Set("fields[artists]", "url,name,artwork")
	q.Set("format[resources]", "map")
	q.Set("include[albums]", "artists")
	q.Set("include[music-videos]", "artists")
	q.Set("include[songs]", "artists")
	q.Set("include[stations]", "radio-show")
	q.Set("l", language)
	q.Set("limit", strconv.Itoa(limit))
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	q.Set("omit[resource]", "autos")
	q.Set("platform", "web")
	q.Set("relate[albums]", "artists")
	q.Set("relate[songs]", "albums")
	q.Set("term", term)
	q.Set("types", types)
	q.Set("with", "lyricHighlights,lyrics,naturalLanguage,serverBubbles,subtitles")
	req.URL.RawQuery = q.Encode()
	resp, err := download.Client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("API request failed with status: %s", resp.Status)
	}
	var payload struct {
		Results map[string]struct {
			Next string `json:"next"`
			Data []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
		} `json:"results"`
		Resources map[string]map[string]WebSearchResult `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false, err
	}
	page := payload.Results[group]
	items := make([]WebSearchResult, 0, len(page.Data))
	for _, ref := range page.Data {
		if ref.Type != "songs" && ref.Type != "albums" && ref.Type != "artists" && ref.Type != "music-videos" {
			continue
		}
		item, ok := payload.Resources[ref.Type][ref.ID]
		if !ok {
			continue
		}
		if item.ID == "" {
			item.ID = ref.ID
		}
		if item.Type == "" {
			item.Type = ref.Type
		}
		items = append(items, item)
	}
	return items, page.Next != "", nil
}
