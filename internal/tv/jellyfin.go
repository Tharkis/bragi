package tv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Jellyfin is the connection Bragi uses to tell Jellyfin about new files.
type Jellyfin struct {
	URL    string
	APIKey string
}

func (s *Service) jellyfin(ctx context.Context) Jellyfin {
	u, _ := s.Store.GetSetting(ctx, SettingJellyfinURL)
	k, _ := s.Store.GetSetting(ctx, SettingJellyfinAPIKey)
	return Jellyfin{URL: strings.TrimRight(u, "/"), APIKey: k}
}

func (s *Service) jellyfinCall(ctx context.Context, j Jellyfin, method, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, j.URL+path, nil)
	if err != nil {
		return fmt.Errorf("invalid Jellyfin URL: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Token="%s"`, j.APIKey))
	resp, err := s.HC.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Jellyfin: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("Jellyfin rejected the API key")
	case resp.StatusCode >= 300:
		return fmt.Errorf("Jellyfin answered HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("the response does not look like Jellyfin: check the URL")
		}
	}
	return nil
}

// TestJellyfin checks a Jellyfin URL and API key and returns the server name.
func (s *Service) TestJellyfin(ctx context.Context, j Jellyfin) (string, error) {
	j.URL = strings.TrimRight(j.URL, "/")
	var info struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	if err := s.jellyfinCall(ctx, j, http.MethodGet, "/System/Info", &info); err != nil {
		return "", err
	}
	if info.Version == "" {
		return "", fmt.Errorf("the response does not look like Jellyfin: check the URL")
	}
	return info.ServerName, nil
}

// RefreshJellyfin asks Jellyfin to scan its libraries for new files. It does
// nothing when Jellyfin is not set up.
func (s *Service) RefreshJellyfin(ctx context.Context) error {
	j := s.jellyfin(ctx)
	if j.URL == "" || j.APIKey == "" {
		return nil
	}
	return s.jellyfinCall(ctx, j, http.MethodPost, "/Library/Refresh", nil)
}
