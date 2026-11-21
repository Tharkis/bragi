package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/store"
)

// secretMask replaces API keys and passwords in responses. A PUT that sends
// the mask back keeps the stored secret.
const secretMask = "********"

type integrationHandlers struct {
	s     *Server
	table store.IntegrationTable
}

func (h integrationHandlers) role() integrations.Role {
	if h.table == store.Indexers {
		return integrations.RoleIndexer
	}
	return integrations.RoleDownloader
}

func (h integrationHandlers) what() string {
	if h.table == store.Indexers {
		return "indexer"
	}
	return "download client"
}

func mask(i store.Integration) store.Integration {
	if i.APIKey != "" {
		i.APIKey = secretMask
	}
	if i.Password != "" {
		i.Password = secretMask
	}
	return i
}

// validate checks the kind exists for this table and required fields are set.
func (h integrationHandlers) validate(i *store.Integration) error {
	i.Name = strings.TrimSpace(i.Name)
	i.BaseURL = strings.TrimRight(strings.TrimSpace(i.BaseURL), "/")
	kind, ok := integrations.KindByID(i.Kind)
	if !ok || kind.Role != h.role() {
		return fmt.Errorf("unknown %s kind %q", h.what(), i.Kind)
	}
	if i.Name == "" {
		i.Name = kind.Label
	}
	u, err := url.Parse(i.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("URL must start with http:// or https://")
	}
	for _, f := range kind.Fields {
		if !f.Required {
			continue
		}
		var v string
		switch f.Name {
		case "apiKey":
			v = i.APIKey
		case "username":
			v = i.Username
		case "password":
			v = i.Password
		case "category":
			v = i.Category
		default:
			continue
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	if i.Priority == 0 {
		i.Priority = 25
	}
	return nil
}

// test runs the adapter's connection check.
func (h integrationHandlers) test(ctx context.Context, i store.Integration) error {
	cfg := integrations.Config{BaseURL: i.BaseURL, APIKey: i.APIKey, Username: i.Username, Password: i.Password, Category: i.Category}
	if h.table == store.Indexers {
		a, err := integrations.NewIndexer(i.Kind, cfg, h.s.hc)
		if err != nil {
			return err
		}
		return a.Test(ctx)
	}
	a, err := integrations.NewDownloadClient(i.Kind, cfg, h.s.hc)
	if err != nil {
		return err
	}
	return a.Test(ctx)
}

type testResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (h integrationHandlers) writeTest(w http.ResponseWriter, r *http.Request, i store.Integration) {
	ctx, cancel := withTimeout(r, testTimeout)
	defer cancel()
	if err := h.test(ctx, i); err != nil {
		writeJSON(w, http.StatusOK, testResult{OK: false, Error: sentence(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, testResult{OK: true})
}

func (h integrationHandlers) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.s.store.ListIntegrations(r.Context(), h.table)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	for n := range items {
		items[n] = mask(items[n])
	}
	writeJSON(w, http.StatusOK, items)
}

func (h integrationHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	i, err := h.s.store.GetIntegration(r.Context(), h.table, id)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	writeJSON(w, http.StatusOK, mask(*i))
}

func (h integrationHandlers) create(w http.ResponseWriter, r *http.Request) {
	var i store.Integration
	if !readJSON(w, r, &i) {
		return
	}
	if err := h.validate(&i); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.s.store.CreateIntegration(r.Context(), h.table, i)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	writeJSON(w, http.StatusCreated, mask(*out))
}

// mergeSecrets keeps stored secrets when the client sent the mask back.
func mergeSecrets(in *store.Integration, existing *store.Integration) {
	if in.APIKey == secretMask {
		in.APIKey = existing.APIKey
	}
	if in.Password == secretMask {
		in.Password = existing.Password
	}
}

func (h integrationHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	existing, err := h.s.store.GetIntegration(r.Context(), h.table, id)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	var i store.Integration
	if !readJSON(w, r, &i) {
		return
	}
	i.ID = id
	mergeSecrets(&i, existing)
	if err := h.validate(&i); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.s.store.UpdateIntegration(r.Context(), h.table, i)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	writeJSON(w, http.StatusOK, mask(*out))
}

func (h integrationHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.s.store.DeleteIntegration(r.Context(), h.table, id); err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testUnsaved tests settings from the form before they are saved. If the
// form edits a saved row (id set) and sends masked secrets, the stored
// secrets are used.
func (h integrationHandlers) testUnsaved(w http.ResponseWriter, r *http.Request) {
	var i store.Integration
	if !readJSON(w, r, &i) {
		return
	}
	if i.ID > 0 {
		existing, err := h.s.store.GetIntegration(r.Context(), h.table, i.ID)
		if err != nil {
			h.s.storeError(w, err, h.what())
			return
		}
		mergeSecrets(&i, existing)
	}
	if err := h.validate(&i); err != nil {
		writeJSON(w, http.StatusOK, testResult{OK: false, Error: err.Error()})
		return
	}
	h.writeTest(w, r, i)
}

func (h integrationHandlers) testSaved(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	i, err := h.s.store.GetIntegration(r.Context(), h.table, id)
	if err != nil {
		h.s.storeError(w, err, h.what())
		return
	}
	h.writeTest(w, r, *i)
}

// sentence capitalizes an error message for display in the UI.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
