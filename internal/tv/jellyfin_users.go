package tv

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// JellyfinUserName looks up a Jellyfin user's name through the Jellyfin
// connection under Settings, Connections.
func (s *Service) JellyfinUserName(ctx context.Context, userID string) (string, error) {
	j := s.jellyfin(ctx)
	if j.URL == "" || j.APIKey == "" {
		return "", errors.New("Jellyfin is not set up")
	}
	var u struct {
		Name string `json:"Name"`
	}
	if err := s.jellyfinCall(ctx, j, http.MethodGet, "/Users/"+url.PathEscape(userID), &u); err != nil {
		return "", err
	}
	return u.Name, nil
}
