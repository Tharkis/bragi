package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var hc = &http.Client{Timeout: 5 * time.Second}

func TestKindsAreConstructible(t *testing.T) {
	for _, k := range Kinds() {
		cfg := Config{BaseURL: "http://localhost"}
		var err error
		switch k.Role {
		case RoleIndexer:
			_, err = NewIndexer(k.ID, cfg, hc)
		case RoleDownloader:
			_, err = NewDownloadClient(k.ID, cfg, hc)
		}
		if err != nil {
			t.Errorf("kind %s: %v", k.ID, err)
		}
	}
}

func TestProwlarr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != "good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"appName": "Prowlarr", "version": "1.20.0"})
	}))
	defer srv.Close()

	ok, _ := NewIndexer("prowlarr", Config{BaseURL: srv.URL + "/", APIKey: "good"}, hc)
	if err := ok.Test(context.Background()); err != nil {
		t.Fatalf("good key: %v", err)
	}
	bad, _ := NewIndexer("prowlarr", Config{BaseURL: srv.URL, APIKey: "bad"}, hc)
	if err := bad.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad key: got %v, want ErrUnauthorized", err)
	}
	wrong, _ := NewIndexer("prowlarr", Config{BaseURL: srv.URL + "/prowlarr", APIKey: "good"}, hc)
	if err := wrong.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("wrong URL base: got %v, want 404 error", err)
	}
}

func torznabServer(t *testing.T, wantPath string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath || r.URL.Query().Get("t") != "caps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("apikey") != "good" {
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Invalid API Key"/>`)
			return
		}
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><caps><server title="Test"/><searching/></caps>`)
	}))
}

func TestJackett(t *testing.T) {
	srv := torznabServer(t, "/api/v2.0/indexers/all/results/torznab/api")
	defer srv.Close()

	ok, _ := NewIndexer("jackett", Config{BaseURL: srv.URL, APIKey: "good"}, hc)
	if err := ok.Test(context.Background()); err != nil {
		t.Fatalf("good key: %v", err)
	}
	bad, _ := NewIndexer("jackett", Config{BaseURL: srv.URL, APIKey: "nope"}, hc)
	if err := bad.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad key: got %v, want ErrUnauthorized", err)
	}
}

func TestNewznab(t *testing.T) {
	srv := torznabServer(t, "/api")
	defer srv.Close()

	ok, _ := NewIndexer("newznab", Config{BaseURL: srv.URL + "/api", APIKey: "good"}, hc)
	if err := ok.Test(context.Background()); err != nil {
		t.Fatalf("good key: %v", err)
	}
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html><body>login</body></html>")
	}))
	defer html.Close()
	notIndexer, _ := NewIndexer("torznab", Config{BaseURL: html.URL, APIKey: "good"}, hc)
	if err := notIndexer.Test(context.Background()); err == nil {
		t.Fatal("HTML page accepted as an indexer")
	}
}

func TestQBittorrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			r.ParseForm()
			if r.Header.Get("Referer") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
				io.WriteString(w, "Fails.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "abc", Path: "/"})
			io.WriteString(w, "Ok.")
		case "/api/v2/app/version":
			if c, err := r.Cookie("SID"); err != nil || c.Value != "abc" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, "v4.6.7")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ok, _ := NewDownloadClient("qbittorrent", Config{BaseURL: srv.URL, Username: "admin", Password: "secret"}, hc)
	if err := ok.Test(context.Background()); err != nil {
		t.Fatalf("good login: %v", err)
	}
	bad, _ := NewDownloadClient("qbittorrent", Config{BaseURL: srv.URL, Username: "admin", Password: "wrong"}, hc)
	if err := bad.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad login: got %v, want ErrUnauthorized", err)
	}
}

func TestUnreachable(t *testing.T) {
	a, _ := NewIndexer("prowlarr", Config{BaseURL: "http://127.0.0.1:1", APIKey: "k"}, hc)
	err := a.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("got %v, want a 'could not reach' error", err)
	}
}
