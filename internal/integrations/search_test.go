package integrations

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTorznabSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("t") != "tvsearch" || q.Get("q") != "Severance" || q.Get("season") != "1" || q.Get("ep") != "2" || q.Get("cat") != "5000" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>AggregateSearch</title>
<item><title>Severance.S01E02.1080p.WEB-DL</title><guid>g1</guid><link>http://jackett/dl/1</link>
<pubDate>Fri, 18 Feb 2022 10:00:00 +0000</pubDate><size>1500000000</size><jackettindexer id="tl">TorrentLeech</jackettindexer>
<torznab:attr name="seeders" value="42"/><torznab:attr name="infohash" value="ABCDEF"/></item>
<item><title>No link</title></item>
</channel></rss>`)
	}))
	defer srv.Close()
	ix, _ := NewIndexer("torznab", Config{BaseURL: srv.URL, APIKey: "k"}, hc)
	rs, err := ix.Search(context.Background(), Query{Text: "Severance", Season: 1, Episode: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d releases, want 1", len(rs))
	}
	r := rs[0]
	if r.Seeders != 42 || r.SizeBytes != 1500000000 || r.Indexer != "TorrentLeech" || r.InfoHash != "abcdef" || r.Protocol != Torrent || r.PublishedAt.Year() != 2022 {
		t.Fatalf("release = %+v", r)
	}
}

func TestInfoHash(t *testing.T) {
	info := "d6:lengthi10e4:name5:a.mkv12:piece lengthi16384e6:pieces0:e"
	torrent := []byte("d8:announce3:url4:info" + info + "e")
	sum := sha1.Sum([]byte(info))
	got, err := infoHash(torrent)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("infoHash = %q, %v", got, err)
	}
	if _, err := infoHash([]byte("<html>")); err == nil {
		t.Fatal("HTML accepted as a torrent")
	}
	h, err := magnetHash("magnet:?xt=urn:btih:MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U&dn=x")
	if err != nil || h != "6162636465666768696a6b6c6d6e6f7071727374" {
		t.Fatalf("base32 magnet = %q, %v", h, err)
	}
}
