// SPDX-License-Identifier: AGPL-3.0-or-later

package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"sync"

	"overscan.gureet.ca/web"
)

// page is the page's own stylesheet, as the build made it.
var page = sync.OnceValues(func() (string, string) {
	names, err := fs.Glob(web.MAG, "mag/dist/app.*.css")
	if err != nil || len(names) != 1 {
		panic(fmt.Sprintf("the page's stylesheet: %v, %v", names, err)) // embedded at build time
	}
	b, err := fs.ReadFile(web.MAG, names[0])
	if err != nil {
		panic(err)
	}
	return names[0], string(b)
})

// Path returns the name, beside the page, of the stylesheet that a theme
// kept as data makes: its ID, and a hash of the theme and of the page's
// own stylesheet, so that a new theme or a new page makes a new name.
func Path(id int64, data []byte) string {
	name, _ := page()
	sum := sha256.Sum256(append([]byte(name+"\n"), data...))
	return "theme-" + strconv.FormatInt(id, 10) + "." + hex.EncodeToString(sum[:5]) + ".css"
}

// pathForm matches a theme's stylesheet's name.
var pathForm = regexp.MustCompile(`^theme-([1-9][0-9]{0,17})\.[0-9a-f]{10}\.css$`)

// ParsePath returns the theme ID in the name of a theme's stylesheet.
func ParsePath(name string) (int64, bool) {
	m := pathForm.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	return id, err == nil
}

// Stylesheet returns the stylesheet that a theme kept as data makes.
func Stylesheet(data []byte) (string, error) {
	t, err := Parse(data)
	if err != nil {
		return "", err
	}
	_, base := page()
	return Render(base, t), nil
}
