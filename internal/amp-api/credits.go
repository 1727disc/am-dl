package ampapi

import (
	"amdl/internal/download"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// SongCredits maps a role to its credited names in the order returned by Apple.
type SongCredits map[string][]string

func creditRoleName(role string) string {
	letters := []rune(strings.ToLower(strings.TrimSpace(role)))
	start := true
	for i, letter := range letters {
		if unicode.IsLetter(letter) {
			if start {
				letters[i] = unicode.ToUpper(letter)
			}
			start = false
		} else if unicode.IsSpace(letter) || letter == '-' || letter == '/' {
			start = true
		}
	}
	return string(letters)
}

type creditRef struct {
	ID string `json:"id"`
}
type creditSong struct {
	ID            string `json:"id"`
	Relationships struct {
		Credits struct {
			Data []creditRef `json:"data"`
		} `json:"credits"`
	} `json:"relationships"`
}
type creditCategory struct {
	Relationships struct {
		Artists struct {
			Data []creditRef `json:"data"`
		} `json:"credit-artists"`
	} `json:"relationships"`
}
type creditArtist struct {
	Attributes struct {
		Name      string   `json:"name"`
		RoleNames []string `json:"roleNames"`
	} `json:"attributes"`
}

// GetSongCredits fetches the song's credits and resolves its resource map.
func GetSongCredits(storefront, id, language, token, mediaUserToken string) (SongCredits, error) {
	if storefront == "" || id == "" {
		return nil, fmt.Errorf("missing song ID or storefront")
	}
	if token == "" {
		var err error
		token, err = GetToken()
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/songs/%s", url.PathEscape(storefront), url.PathEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("include", "credits")
	q.Set("format[resources]", "map")
	q.Set("platform", "web")
	if language != "" {
		q.Set("l", language)
	}
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://beta.music.apple.com")
	req.Header.Set("Referer", "https://beta.music.apple.com/")
	if mediaUserToken != "" {
		req.Header.Set("Media-User-Token", mediaUserToken)
	}
	resp, err := download.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("credits request: %s", resp.Status)
	}
	var data struct {
		Data      []creditSong `json:"data"`
		Resources struct {
			Songs      map[string]creditSong     `json:"songs"`
			Categories map[string]creditCategory `json:"role-categories"`
			Artists    map[string]creditArtist   `json:"credit-artists"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode credits response: %w", err)
	}
	song, found := data.Resources.Songs[id]
	if !found {
		for _, s := range data.Data {
			if s.ID == id {
				song = s
				found = true
				break
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("song %s not found in credits response", id)
	}
	credits := make(SongCredits)
	for _, ref := range song.Relationships.Credits.Data {
		category, ok := data.Resources.Categories[ref.ID]
		if !ok {
			continue
		}
		for _, artistRef := range category.Relationships.Artists.Data {
			artist, ok := data.Resources.Artists[artistRef.ID]
			if !ok {
				continue
			}
			name := strings.TrimSpace(artist.Attributes.Name)
			if name == "" {
				continue
			}
			for _, rawRole := range artist.Attributes.RoleNames {
				role := creditRoleName(rawRole)
				if role == "" || role == "Composer" || role == "Performer" {
					continue
				}
				exists := false
				for _, previous := range credits[role] {
					if previous == name {
						exists = true
						break
					}
				}
				if !exists {
					credits[role] = append(credits[role], name)
				}
			}
		}
	}
	return credits, nil
}
