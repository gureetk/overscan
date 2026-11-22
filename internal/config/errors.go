// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// fileError is a mistake in the configuration file, in words for whoever
// wrote it: the file and the line, and what is wrong in terms of the
// file's own keys. It never quotes the file's values, which can hold a
// source's credentials, so it leaves out the lines around the mistake that
// the YAML decoder's own message shows.
type fileError struct {
	text string
	err  error
}

func (e *fileError) Error() string { return e.text }
func (e *fileError) Unwrap() error { return e.err }

// readError says why the configuration file at path cannot be read.
func readError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &fileError{fmt.Sprintf("config: there is no file at %s", path), err}
	case errors.Is(err, fs.ErrPermission):
		return &fileError{fmt.Sprintf("config: %s may not be read by this user; run the command "+
			"as the user that the server runs as, such as with runuser -u overscan -- overscan …, or as root", path), err}
	}
	return fmt.Errorf("config: %w", err)
}

// decodeError puts an error from decoding the configuration file at path
// into words.
func decodeError(path string, err error) error {
	var (
		typ      *yaml.TypeError
		node     *yaml.UnexpectedNodeTypeError
		overflow *yaml.OverflowError
		unknown  *yaml.UnknownFieldError
		dup      *yaml.DuplicateKeyError
		syntax   *yaml.SyntaxError
	)
	var at *token.Token
	var text string
	switch {
	case errors.As(err, &typ):
		at = typ.Token
		text = fmt.Sprintf("%s takes %s, not %s", keyOf(at), wants(typ.DstType), kindOf(typ.SrcType))
	case errors.As(err, &node):
		at = node.Token
		text = nodeText(keyOf(at), node.Expected)
	case errors.As(err, &overflow):
		at = overflow.Token
		text = fmt.Sprintf("%s takes a smaller number", keyOf(at))
	case errors.As(err, &unknown):
		at = unknown.Token
		text = fmt.Sprintf("there is no setting %q", at.Value)
		if near := nearestKey(at.Value); near != "" {
			text += fmt.Sprintf("; did you mean %q?", near)
		}
	case errors.As(err, &dup):
		at = dup.Token
		text = fmt.Sprintf("%q is set a second time", at.Value)
	case errors.As(err, &syntax) && strings.Contains(syntax.Message, "already defined") && syntax.Token != nil:
		// The decoder reports a key given twice as a syntax error.
		at = syntax.Token
		text = fmt.Sprintf("%q is set a second time", at.Value)
	case errors.As(err, &syntax):
		at = syntax.Token
		text = syntax.Message + "; " + syntaxHint(at)
	case strings.HasPrefix(err.Error(), "time: "):
		// A source's refresh that time.ParseDuration cannot read, which
		// the decoder reports without a line.
		return &fileError{fmt.Sprintf("config %s: a source's refresh takes a number and a unit, such as 30m, 6h or 24h (%v)", path, err), err}
	default:
		return fmt.Errorf("config %s: %w", path, err)
	}
	if at == nil || at.Position == nil {
		return &fileError{fmt.Sprintf("config %s: %s", path, text), err}
	}
	return &fileError{fmt.Sprintf("config %s, line %d: %s", path, at.Position.Line, text), err}
}

// keyOf is the key whose value starts at tok, or "the value" if tok starts
// none.
func keyOf(tok *token.Token) string {
	if tok == nil || tok.Prev == nil || tok.Prev.Type != token.MappingValueType || tok.Prev.Prev == nil {
		return "the value"
	}
	return tok.Prev.Prev.Value
}

// nodeText says what key takes, where the file gives it the wrong kind of
// value.
func nodeText(key string, expected ast.NodeType) string {
	switch {
	case expected == ast.MappingType && key == "the value":
		return `each line takes a key, a colon and a space, then the value, such as listen: ":8080"`
	case expected == ast.MappingType:
		return fmt.Sprintf("%s takes settings of its own, each on a line of its own, indented under it", key)
	case expected == ast.SequenceType:
		return fmt.Sprintf(`%s takes a list: each item on a line of its own after "- ", or the items in brackets, such as [one, two]`, key)
	}
	return fmt.Sprintf("%s takes a single value", key)
}

// syntaxHint says what to look at near a syntax error at tok.
func syntaxHint(tok *token.Token) string {
	if tok != nil && strings.Contains(tok.Origin, "\t") {
		return "indent with spaces, since YAML takes no tabs"
	}
	return "check the indentation, a colon and a space after each key, and that quotes come in pairs"
}

var durationType = reflect.TypeOf(time.Duration(0))

// wants says what a setting of type t takes.
func wants(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == nil:
		return "a value"
	case t == durationType:
		return "a number and a unit, such as 30m, 6h or 24h"
	}
	return kindOf(t)
}

// kindOf names the kind of a value of type t, as the file's writer would.
func kindOf(t reflect.Type) string {
	if t == nil {
		return "nothing"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "text"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "settings of its own"
	case reflect.Pointer:
		return kindOf(t.Elem())
	}
	return "a value"
}

// nearestKey is the setting whose name is closest to name, if one is close
// enough to be what was meant: at most two letters added, left out or
// changed, or two swapped.
func nearestKey(name string) string {
	best, bestDistance := "", 3
	for _, key := range settingNames() {
		if d := distance(name, key); d < bestDistance && d < len(key) {
			best, bestDistance = key, d
		}
	}
	return best
}

// settingNames lists every key that the configuration file takes, at any
// level.
func settingNames() []string {
	var names []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "" || name == "-" {
				continue
			}
			names = append(names, name)
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(Config{}))
	return names
}

// distance is the optimal string alignment distance between a and b: the
// letters to add, leave out or change, or neighbours to swap, to turn one
// into the other.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}
