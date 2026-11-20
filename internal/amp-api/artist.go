package ampapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"amdl/internal/download"
)

// ArtistMetadata contains the optional artist details returned by the catalog API.
type ArtistMetadata struct {
	Name           string   `json:"name"`
	ArtistBio      string   `json:"artistBio"`
	BornOrFormed   string   `json:"bornOrFormed"`
	Origin         string   `json:"origin"`
	IsGroup        *bool    `json:"isGroup"`
	GenreNames     []string `json:"genreNames"`
	KeyColor       string   `json:"keyColor"`
	EditorialVideo map[string]struct {
		Video        string `json:"video"`
		PreviewFrame struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"previewFrame"`
	} `json:"editorialVideo"`
	EditorialArtwork struct {
		Logo struct {
			URL    string `json:"url"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"musicContentColorLogoTrimmed"`
	} `json:"editorialArtwork"`
}

// GetArtistMetadata retrieves artist information and an optional editorial logo.
func GetArtistMetadata(storefront, id, language, token, mediaUserToken string) (ArtistMetadata, error) {
	var empty ArtistMetadata
	if storefront == "" || id == "" {
		return empty, fmt.Errorf("missing artist ID or storefront")
	}
	if token == "" {
		var err error
		token, err = GetToken()
		if err != nil {
			return empty, err
		}
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/artists/%s", url.PathEscape(storefront), url.PathEscape(id)), nil)
	if err != nil {
		return empty, err
	}
	query := req.URL.Query()
	query.Set("extend", "artistBio,bornOrFormed,origin,isGroup,keyColor,editorialArtwork,editorialVideo")
	query.Set("art[url]", "c,f")
	query.Set("platform", "web")
	if language != "" {
		query.Set("l", language)
	}
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://beta.music.apple.com")
	req.Header.Set("Referer", "https://beta.music.apple.com/")
	if mediaUserToken != "" {
		req.Header.Set("Media-User-Token", mediaUserToken)
	}
	resp, err := download.Client.Do(req)
	if err != nil {
		return empty, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("artist request: %s", resp.Status)
	}
	type artist struct {
		ID         string         `json:"id"`
		Attributes ArtistMetadata `json:"attributes"`
	}
	var data struct {
		Data      []artist `json:"data"`
		Resources struct {
			Artists map[string]artist `json:"artists"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return empty, fmt.Errorf("decode artist response: %w", err)
	}
	if a, ok := data.Resources.Artists[id]; ok {
		return a.Attributes, nil
	}
	for _, a := range data.Data {
		if a.ID == id || (a.ID == "" && len(data.Data) == 1) {
			return a.Attributes, nil
		}
	}
	return empty, fmt.Errorf("artist %s not found in response", id)
}
