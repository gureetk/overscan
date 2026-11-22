// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lastBeforeUpgradeTests is the last migration made before 1.0's promise
// that an upgrade keeps what a household set up (D115, D117). Every later
// migration has an upgrade test of its own, named TestMigrationNNNN…: it
// makes a database with the schema before it, holding what a household sets
// up, applies the migration, and checks that none of it was lost or changed
// (TestMigration0043SourcePublicOnly shows the way).
const lastBeforeUpgradeTests = 42

// Released migrations are never edited, since databases already have them:
// a change to the schema goes in a new migration. The SHA-256 of each is in
// testdata/migrations.sha256, and the pull request that adds a migration
// adds its line.
func TestMigrationsAreUnchanged(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "migrations.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[string]string{}
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("testdata/migrations.sha256: %q is not a line of sha256sum", line)
		}
		want[name] = sum
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := fs.ReadFile(migrationFiles, "migrations/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		switch w, ok := want[e.Name()]; {
		case !ok:
			t.Errorf("%s has no line in testdata/migrations.sha256: add the line that `sha256sum %s` prints, in the pull request that adds it", e.Name(), e.Name())
		case w != got:
			t.Errorf("%s has changed: a migration that a release holds is never edited, since databases already have it; put the change in a new migration", e.Name())
		}
		delete(want, e.Name())
	}
	for name := range want {
		t.Errorf("testdata/migrations.sha256 lists %s, which is not a migration", name)
	}
}

// Every migration after lastBeforeUpgradeTests comes with its upgrade test.
func TestNewMigrationsHaveUpgradeTests(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[int]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "TestMigration") {
				continue
			}
			var n int
			if _, err := fmt.Sscanf(strings.TrimPrefix(fn.Name.Name, "TestMigration"), "%04d", &n); err == nil {
				tests[n] = true
			}
		}
	}
	for _, m := range migrations {
		if m.version > lastBeforeUpgradeTests && !tests[m.version] {
			t.Errorf("migration %s has no upgrade test: write TestMigration%04d…, which makes a database with schema %d holding what a household sets up, applies %s, and checks that nothing of it was lost or changed",
				m.name, m.version, m.version-1, m.name)
		}
	}
}
