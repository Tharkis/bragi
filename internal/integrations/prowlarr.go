package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// prowlarr talks to Prowlarr's v1 API. One connection covers every indexer
// configured in Prowlarr.
type prowlarr struct {
	cfg Config
	hc  *http.Client
}

func (p *prowlarr) Test(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/api/v1/system/status", nil)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("X-Api-Key", p.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := do(p.hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Prowlarr answered HTTP %d", resp.StatusCode)
	}
	var status struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &status); err != nil || status.Version == "" {
		return fmt.Errorf("the response does not look like Prowlarr: check the URL")
	}
	return nil
}

func (p *prowlarr) Search(ctx context.Context, q Query) ([]Release, error) {
	return nil, ErrNotImplemented
}
