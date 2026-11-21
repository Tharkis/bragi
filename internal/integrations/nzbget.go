package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// nzbget talks to NZBGet's JSON-RPC API.
type nzbget struct {
	cfg Config
	hc  *http.Client
}

func (n *nzbget) call(ctx context.Context, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	payload, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.BaseURL+"/jsonrpc", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if n.cfg.Username != "" || n.cfg.Password != "" {
		req.SetBasicAuth(n.cfg.Username, n.cfg.Password)
	}
	resp, err := do(n.hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NZBGet answered HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return fmt.Errorf("the response does not look like NZBGet: check the URL")
	}
	if rpc.Error != nil {
		return fmt.Errorf("NZBGet error: %s", rpc.Error.Message)
	}
	return json.Unmarshal(rpc.Result, out)
}

func (n *nzbget) Test(ctx context.Context) error {
	var version string
	if err := n.call(ctx, "version", nil, &version); err != nil {
		return err
	}
	if version == "" {
		return fmt.Errorf("NZBGet did not report a version")
	}
	return nil
}

func (n *nzbget) Add(ctx context.Context, r Release) (string, error) {
	return "", ErrNotImplemented
}

func (n *nzbget) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	return nil, ErrNotImplemented
}

func (n *nzbget) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	return ErrNotImplemented
}
