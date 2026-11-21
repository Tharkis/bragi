package db

import (
	"context"
	"path/filepath"
	"testing"
)

// The library rebuild keeps ids and the rows that point at them.
func TestLibraryRebuildKeepsReferences(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "bragi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO libraries (id, media_type, name, path) VALUES (7, 'book', 'Books', '/data/grimmory')`)
	mustExec(`INSERT INTO media_items (media_type, level, library_id, title) VALUES ('book', 'work', 7, 'Dune')`)
	// Run the rebuild again over data.
	mustExec(`DELETE FROM schema_migrations WHERE version = 7`)
	if err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	var lib int
	d.QueryRowContext(ctx, `SELECT library_id FROM media_items WHERE title = 'Dune'`).Scan(&lib)
	if lib != 7 {
		t.Fatalf("library_id after rebuild = %d", lib)
	}
	// Books and audiobooks may share a folder; two book libraries may not.
	mustExec(`INSERT INTO libraries (media_type, name, path) VALUES ('audiobook', 'Audiobooks', '/data/grimmory')`)
	if _, err := d.ExecContext(ctx, `INSERT INTO libraries (media_type, name, path) VALUES ('book', 'Again', '/data/grimmory')`); err == nil {
		t.Fatal("duplicate book library allowed")
	}
	// Foreign keys are back on: removing the library clears the link.
	mustExec(`DELETE FROM libraries WHERE id = 7`)
	var n int
	d.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_items WHERE library_id IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatal("foreign keys not enforced after the rebuild")
	}
}
