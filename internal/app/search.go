package app

import (
	"amdl/internal/amp-api"
	"fmt"
	"github.com/AlecAivazis/survey/v2"
	"strings"
)

type SearchResultItem struct {
	Type   string
	Name   string
	Detail string
	URL    string
	ID     string
}

func parseSearchInput(first string, rest []string) (string, []string) {
	switch first {
	case "song", "album", "artist", "video":
		return first, rest
	default:
		query := make([]string, 0, len(rest)+1)
		query = append(query, first)
		query = append(query, rest...)
		return "", query
	}
}

func searchTitle(name, rating string) string {
	switch rating {
	case "explicit":
		return name + " [E]"
	case "clean":
		return name + " [C]"
	default:
		return name
	}
}

func topSearchItem(result ampapi.WebSearchResult, storefront string) (SearchResultItem, string, bool) {
	a := result.Attributes
	item := SearchResultItem{ID: result.ID, Name: a.Name, URL: a.URL}
	if item.ID == "" || item.Name == "" {
		return item, "", false
	}
	var detail string
	switch result.Type {
	case "songs":
		item.Type = "Song"
		detail = fmt.Sprintf("%s (%s)", a.ArtistName, a.AlbumName)
		// Song URLs sometimes point to albums. Use the song ID explicitly so the
		// selected result always downloads just that song.
		item.URL = fmt.Sprintf("https://music.apple.com/%s/song/%s", storefront, result.ID)
	case "albums":
		item.Type = "Album"
		year := ""
		if len(a.ReleaseDate) >= 4 {
			year = a.ReleaseDate[:4]
		}
		detail = fmt.Sprintf("%s (%s, %d tracks)", a.ArtistName, year, a.TrackCount)
	case "artists":
		item.Type = "Artist"
		detail = strings.Join(a.GenreNames, ", ")
	case "music-videos":
		item.Type = "Video"
		detail = a.ArtistName
		item.URL = fmt.Sprintf("https://music.apple.com/%s/music-video/%s", storefront, result.ID)
	default:
		return item, "", false
	}
	if item.URL == "" {
		return item, "", false
	}
	label := fmt.Sprintf("[%s] %s", item.Type, searchTitle(item.Name, a.ContentRating))
	if detail != "" {
		label += " - " + detail
	}
	return item, label, true
}

func (r *Runner) handleTopSearch(queryParts []string, token string) (string, error) {
	query := strings.TrimSpace(strings.Join(queryParts, " "))
	if query == "" {
		return "", fmt.Errorf("search requires a query")
	}
	fmt.Printf("Searching top results: %q in storefront %q\n", query, r.Config.General.Storefront)
	results, _, err := ampapi.SearchWeb(r.Config.General.Storefront, query, "", r.Config.General.Language, token, 21, 0)
	if err != nil {
		return "", fmt.Errorf("error fetching search results: %w", err)
	}
	var items []SearchResultItem
	var options []string
	for _, result := range results {
		item, label, ok := topSearchItem(result, r.Config.General.Storefront)
		if ok {
			items = append(items, item)
			options = append(options, label)
		}
	}
	if len(items) == 0 {
		fmt.Println("No results found.")
		return "", nil
	}
	prompt := &survey.Select{Message: "Use arrow keys to navigate, Enter to select:", Options: options, PageSize: 15}
	selectedIndex := 0
	if err := survey.AskOne(prompt, &selectedIndex); err != nil {
		return "", nil
	}
	selected := items[selectedIndex]
	quality, err := r.promptForQuality(selected, token)
	if err != nil {
		return "", fmt.Errorf("could not process quality selection: %w", err)
	}
	if quality == "" {
		fmt.Println("Selection cancelled.")
		return "", nil
	}
	if quality != "default" {
		r.setDlFlags(quality)
	}
	return selected.URL, nil
}

// QualityOption holds information about a downloadable quality.

type QualityOption struct {
	ID          string
	Description string
}

// setDlFlags configures the global download flags based on the user's quality selection.

func (r *Runner) setDlFlags(quality string) {
	r.Flags.Atmos = false
	r.Flags.AAC = false

	switch quality {
	case "atmos":
		r.Flags.Atmos = true
		fmt.Println("Quality set to: Dolby Atmos")
	case "aac":
		r.Flags.AAC = true
		r.Config.Media.AacType = "aac"
		fmt.Println("Quality set to: High-Quality (AAC)")
	case "alac":
		fmt.Println("Quality set to: Lossless (ALAC)")
	}
}

// promptForQuality asks the user to select a download quality for the chosen media.

func (r *Runner) promptForQuality(item SearchResultItem, token string) (string, error) {
	if r.Flags.LyricOnly {
		return "default", nil
	}
	if item.Type == "Artist" {
		fmt.Println("Artist selected. Proceeding to list all albums/videos.")
		return "default", nil
	}
	if item.Type == "Video" {
		fmt.Println("Music video selected. Using music video settings.")
		return "default", nil
	}

	fmt.Printf("\nFetching available qualities for: %s\n", item.Name)

	qualities := []QualityOption{
		{ID: "alac", Description: "Lossless (ALAC)"},
		{ID: "aac", Description: "High-Quality (AAC)"},
		{ID: "atmos", Description: "Dolby Atmos"},
	}
	qualityOptions := []string{}
	for _, q := range qualities {
		qualityOptions = append(qualityOptions, q.Description)
	}

	prompt := &survey.Select{
		Message:  "Select a quality to download:",
		Options:  qualityOptions,
		PageSize: 5,
	}

	selectedIndex := 0
	err := survey.AskOne(prompt, &selectedIndex)
	if err != nil {
		// This can happen if the user presses Ctrl+C
		return "", nil
	}

	return qualities[selectedIndex].ID, nil
}

// handleSearch manages the entire interactive search process.

func (r *Runner) handleSearch(searchType string, queryParts []string, token string) (string, error) {
	if searchType == "" {
		return r.handleTopSearch(queryParts, token)
	}
	query := strings.Join(queryParts, " ")
	validTypes := map[string]bool{"album": true, "song": true, "artist": true, "video": true}
	if !validTypes[searchType] {
		return "", fmt.Errorf("invalid search type: %s. Use 'album', 'song', 'artist', or 'video'", searchType)
	}

	fmt.Printf("Searching for %ss: \"%s\" in storefront \"%s\"\n", searchType, query, r.Config.General.Storefront)

	offset := 0
	limit := 15 // Increased limit for better navigation

	for {
		results, hasNext, err := ampapi.SearchWeb(r.Config.General.Storefront, query, searchType, r.Config.General.Language, token, limit, offset)
		if err != nil {
			return "", fmt.Errorf("error fetching search results: %w", err)
		}

		var items []SearchResultItem
		var displayOptions []string

		// Special options for navigation
		const prevPageOpt = "⬅️  Previous Page"
		const nextPageOpt = "➡️  Next Page"

		// Add previous page option if applicable
		if offset > 0 {
			displayOptions = append(displayOptions, prevPageOpt)
		}

		for _, result := range results {
			item, label, ok := topSearchItem(result, r.Config.General.Storefront)
			if !ok {
				continue
			}
			// Scoped search already identifies the result type.
			label = strings.TrimPrefix(label, "["+item.Type+"] ")
			items = append(items, item)
			displayOptions = append(displayOptions, label)
		}

		if len(items) == 0 && offset == 0 {
			fmt.Println("No results found.")
			return "", nil
		}

		// Add next page option if applicable
		if hasNext {
			displayOptions = append(displayOptions, nextPageOpt)
		}

		prompt := &survey.Select{
			Message:  "Use arrow keys to navigate, Enter to select:",
			Options:  displayOptions,
			PageSize: limit, // Show a full page of results
		}

		selectedIndex := 0
		err = survey.AskOne(prompt, &selectedIndex)
		if err != nil {
			// User pressed Ctrl+C
			return "", nil
		}

		selectedOption := displayOptions[selectedIndex]

		// Handle pagination
		if selectedOption == nextPageOpt {
			offset += limit
			continue
		}
		if selectedOption == prevPageOpt {
			offset -= limit
			continue
		}

		// Adjust index to match the `items` slice if "Previous Page" was an option
		itemIndex := selectedIndex
		if offset > 0 {
			itemIndex--
		}

		selectedItem := items[itemIndex]

		quality, err := r.promptForQuality(selectedItem, token)
		if err != nil {
			return "", fmt.Errorf("could not process quality selection: %w", err)
		}
		if quality == "" { // User cancelled quality selection
			fmt.Println("Selection cancelled.")
			return "", nil
		}

		if quality != "default" {
			r.setDlFlags(quality)
		}

		return selectedItem.URL, nil
	}
}

// END: New functions for search functionality

// CONVERSION FEATURE: Determine if source codec is lossy (rough heuristic by extension/codec name).
