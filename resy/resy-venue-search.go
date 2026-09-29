package resy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// VenueHit is a single venue returned by a name search.
type VenueHit struct {
	ID       int
	Name     string
	Locality string
	Region   string
}

func (v VenueHit) String() string {
	where := strings.TrimSpace(strings.Trim(fmt.Sprintf("%s, %s", v.Locality, v.Region), " ,"))
	if where == "" {
		return fmt.Sprintf("%-7d %s", v.ID, v.Name)
	}
	return fmt.Sprintf("%-7d %s — %s", v.ID, v.Name, where)
}

// venueSearchResponse mirrors the shape of /3/venuesearch/search.
type venueSearchResponse struct {
	Search struct {
		Hits []struct {
			ID struct {
				Resy json.Number `json:"resy"`
			} `json:"id"`
			Name     string `json:"name"`
			Locality string `json:"locality"`
			Region   string `json:"region"`
		} `json:"hits"`
	} `json:"search"`
}

// SearchVenues resolves a venue name to candidate venue IDs. The raw response
// is returned alongside the hits so an unexpected payload shape can be
// inspected rather than silently producing zero results.
func (rc *ResyClient) SearchVenues(query string) ([]VenueHit, string, error) {
	resp, err := rc.resyApi.SearchVenues(query)
	if err != nil {
		return nil, "", err
	}

	var parsed venueSearchResponse
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return nil, resp, fmt.Errorf("could not parse venue search response: %w", err)
	}

	var hits []VenueHit
	for _, hit := range parsed.Search.Hits {
		id, err := hit.ID.Resy.Int64()
		if err != nil || id <= 0 {
			continue
		}
		hits = append(hits, VenueHit{
			ID:       int(id),
			Name:     hit.Name,
			Locality: hit.Locality,
			Region:   hit.Region,
		})
	}

	return hits, resp, nil
}
