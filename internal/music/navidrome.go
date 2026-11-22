package music

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Navidrome settings. Navidrome scans on its own schedule; with these set,
// Bragi asks it to scan right after an album is imported.
const (
	SettingNavidromeURL      = "navidrome_url"
	SettingNavidromeUser     = "navidrome_user"
	SettingNavidromePassword = "navidrome_password"
)

// Navidrome is a connection to Navidrome's Subsonic API.
type Navidrome struct {
	URL      string
	User     string
	Password string
}

// Navidrome returns the saved connection; URL is empty when none is set.
func (s *Service) Navidrome(ctx context.Context) Navidrome {
	u, _ := s.Store.GetSetting(ctx, SettingNavidromeURL)
	n, _ := s.Store.GetSetting(ctx, SettingNavidromeUser)
	p, _ := s.Store.GetSetting(ctx, SettingNavidromePassword)
	return Navidrome{URL: strings.TrimRight(u, "/"), User: n, Password: p}
}

// call sends one Subsonic request with token authentication.
func (n Navidrome) call(ctx context.Context, hc *http.Client, method string) error {
	if n.URL == "" || n.User == "" {
		return errors.New("enter Navidrome's URL and a username")
	}
	salt := make([]byte, 8)
	rand.Read(salt)
	saltHex := hex.EncodeToString(salt)
	sum := md5.Sum([]byte(n.Password + saltHex))
	q := url.Values{"u": {n.User}, "t": {hex.EncodeToString(sum[:])}, "s": {saltHex}, "v": {"1.16.1"}, "c": {"bragi"}, "f": {"json"}}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.URL+"/rest/"+method+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("invalid Navidrome URL: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Navidrome: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Navidrome answered HTTP %d: check the URL", resp.StatusCode)
	}
	var out struct {
		R struct {
			Status string `json:"status"`
			Type   string `json:"type"`
			Error  struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.R.Status == "" {
		return errors.New("the response does not look like Navidrome: check the URL")
	}
	if out.R.Status != "ok" {
		if out.R.Error.Code == 40 || out.R.Error.Code == 41 {
			return errors.New("Navidrome rejected the username or password")
		}
		if out.R.Error.Code == 50 {
			return errors.New("that Navidrome user is not an admin, so it cannot start a scan")
		}
		return fmt.Errorf("Navidrome said: %s", out.R.Error.Message)
	}
	return nil
}

// TestNavidrome checks a connection can sign in.
func (s *Service) TestNavidrome(ctx context.Context, n Navidrome) error {
	return n.call(ctx, s.HC, "ping")
}

// RefreshNavidrome asks Navidrome to scan for new files. Without a
// connection set it does nothing.
func (s *Service) RefreshNavidrome(ctx context.Context) error {
	n := s.Navidrome(ctx)
	if n.URL == "" {
		return nil
	}
	return n.call(ctx, s.HC, "startScan")
}
