// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver
)

// What two releases legitimately answer differently, whatever they keep.
// Each needs a reason; anything else that differs fails the test.
var allowed = []struct {
	path   *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`^(blocked )?box POST session\.version$`), "the server's version"},
	{regexp.MustCompile(`^(blocked )?box POST session\.(token|expires)$`), "each session has a token of its own, for a day from when it starts"},
	{regexp.MustCompile(`^box GET channels\.version$`), "the lineup's version is a hash of what a release sends for it"},
	{regexp.MustCompile(`^box GET profiles\.theme$`), "a theme's stylesheet is named for a hash of the page's own stylesheet, which each release changes"},
	// Times that the server records by itself. A restart usually changes
	// them, but not when it comes within the same second.
	{regexp.MustCompile(`\.(refreshed_at|read_at|tried_at|last_seen)$`), "when the server last read a source or saw a device"},
}

func isAllowed(path string) bool {
	for _, a := range allowed {
		if a.path.MatchString(path) {
			return true
		}
	}
	return false
}

// changed returns the paths at which a and b differ, as deep as they go.
func changed(path string, a, b any, out map[string]bool) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			out[path] = true
			return
		}
		for k := range av {
			changed(path+"."+k, av[k], bv[k], out)
		}
		for k := range bv {
			if _, ok := av[k]; !ok {
				out[path+"."+k] = true
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			out[path] = true
			return
		}
		for i := range av {
			changed(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	default:
		if !equalScalars(a, b) {
			out[path] = true
		}
	}
}

func equalScalars(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// covers reports where now lacks or changes what before had: every key of
// an object, every element of an array, in order, and every value. now may
// have more keys. Paths in skip, and those that allowed lists, are not
// compared.
func covers(path string, before, now any, skip map[string]bool, out *[]string) {
	if skip[path] || isAllowed(path) {
		return
	}
	switch bv := before.(type) {
	case map[string]any:
		nv, ok := now.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: was an object, is now %s", path, short(now)))
			return
		}
		for _, k := range slices.Sorted(maps.Keys(bv)) {
			if _, ok := nv[k]; !ok {
				if !skip[path+"."+k] && !isAllowed(path+"."+k) {
					*out = append(*out, fmt.Sprintf("%s.%s: was %s, is now missing", path, k, short(bv[k])))
				}
				continue
			}
			covers(path+"."+k, bv[k], nv[k], skip, out)
		}
	case []any:
		nv, ok := now.([]any)
		if !ok || len(nv) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: was %s, is now %s", path, short(before), short(now)))
			return
		}
		for i := range bv {
			covers(fmt.Sprintf("%s[%d]", path, i), bv[i], nv[i], skip, out)
		}
	default:
		if !equalScalars(before, now) {
			*out = append(*out, fmt.Sprintf("%s: was %s, is now %s", path, short(before), short(now)))
		}
	}
}

func short(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(data) > 160 {
		return string(data[:160]) + "…"
	}
	return string(data)
}

// A database, as the checks read it.
type database struct {
	columns map[string][]string       // each table's columns, in order
	rows    map[string]map[string]int // each table's rows, as JSON arrays of the columns' values, counted
	schema  []string                  // what sqlite_schema says, sorted
}

// droppedColumns are the columns that migrations removed, having carried
// what they held over elsewhere: which a database without them no longer
// needs.
var droppedColumns = map[string]string{
	"favourites.account_id": "schema 31 gave favourites to the account's first profile (D94)",
	"progress.account_id":   "schema 31 gave resume points to the account's first profile (D94)",
}

func readDatabase(path string) (*database, error) {
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	d := &database{columns: map[string][]string{}, rows: map[string]map[string]int{}}
	rows, err := db.Query("SELECT type, name, tbl_name, coalesce(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY type, name")
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var typ, name, table, text string
		if err := rows.Scan(&typ, &name, &table, &text); err != nil {
			rows.Close()
			return nil, err
		}
		d.schema = append(d.schema, typ+" "+name+" ON "+table+": "+text)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range tables {
		cols, err := columnsOf(db, t)
		if err != nil {
			return nil, err
		}
		d.columns[t] = cols
	}
	for _, t := range tables {
		d.rows[t], err = rowsOf(db, t, d.columns[t])
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

func columnsOf(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// rowsOf reads a table's rows, with only the columns given, as JSON arrays.
func rowsOf(db *sql.DB, table string, cols []string) (map[string]int, error) {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = quote(c)
	}
	rows, err := db.Query("SELECT " + strings.Join(q, ", ") + " FROM " + quote(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		data, err := json.Marshal(vals)
		if err != nil {
			return nil, err
		}
		out[string(data)]++
	}
	return out, rows.Err()
}

// keptRows reports what of before's rows the upgraded database lost or
// changed: every table and column it had, unless droppedColumns says why,
// and every row, as its columns that both have hold it. The upgraded
// database may have more tables, columns and rows.
func keptRows(beforePath, afterPath string) ([]string, error) {
	before, err := readDatabase(beforePath)
	if err != nil {
		return nil, err
	}
	after, err := readDatabase(afterPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+afterPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var problems []string
	for _, t := range slices.Sorted(maps.Keys(before.columns)) {
		now, ok := after.columns[t]
		if !ok {
			problems = append(problems, fmt.Sprintf("table %s, with %d rows, is gone", t, total(before.rows[t])))
			continue
		}
		var common []string
		for _, c := range before.columns[t] {
			if slices.Contains(now, c) {
				common = append(common, c)
			} else if _, ok := droppedColumns[t+"."+c]; !ok {
				problems = append(problems, fmt.Sprintf("column %s.%s is gone", t, c))
			}
		}
		if len(common) == 0 {
			continue
		}
		was, err := rowsOfDatabase(beforePath, t, common)
		if err != nil {
			return nil, err
		}
		is, err := rowsOf(db, t, common)
		if err != nil {
			return nil, err
		}
		lost := 0
		var example string
		for row, n := range was {
			if is[row] < n {
				lost += n - is[row]
				if example == "" {
					example = row
				}
			}
		}
		if lost > 0 {
			problems = append(problems, fmt.Sprintf("table %s lost or changed %d of its %d rows, such as %.200s (columns %s)",
				t, lost, total(was), example, strings.Join(common, ", ")))
		}
	}
	return problems, nil
}

func rowsOfDatabase(path, table string, cols []string) (map[string]int, error) {
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return rowsOf(db, table, cols)
}

func total(rows map[string]int) int {
	n := 0
	for _, c := range rows {
		n += c
	}
	return n
}

// sameSchema reports where the upgraded database's schema differs from a
// new database's.
func sameSchema(upgradedPath, newPath string) ([]string, error) {
	upgraded, err := readDatabase(upgradedPath)
	if err != nil {
		return nil, err
	}
	fresh, err := readDatabase(newPath)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, s := range upgraded.schema {
		if !slices.Contains(fresh.schema, s) {
			problems = append(problems, "only the upgraded database has "+s)
		}
	}
	for _, s := range fresh.schema {
		if !slices.Contains(upgraded.schema, s) {
			problems = append(problems, "only a new database has "+s)
		}
	}
	uv, err := userVersion(upgradedPath)
	if err != nil {
		return nil, err
	}
	nv, err := userVersion(newPath)
	if err != nil {
		return nil, err
	}
	if uv != nv {
		problems = append(problems, fmt.Sprintf("the upgraded database has schema version %d, a new one %d", uv, nv))
	}
	return problems, nil
}

func userVersion(path string) (int, error) {
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	err = db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}
