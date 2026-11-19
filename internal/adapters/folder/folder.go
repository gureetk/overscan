// SPDX-License-Identifier: AGPL-3.0-or-later

// Package folder reads a folder of films and series on the server: it finds
// the videos, tells films from episodes by the names of files and folders,
// and reads the .nfo files and pictures beside them, as Kodi, Jellyfin and
// Plex lay them out. docs/library-folders.md says how it names things.
//
// A folder is opened as an os.Root, so nothing outside it can be read, not
// even through a symbolic link. Errors never quote its path.
package folder

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Limits on a folder's walk.
const (
	maxVideos = 200_000
	maxDepth  = 12
	// MaxPictureBytes is the most of a picture that ReadPicture reads.
	MaxPictureBytes = 16 << 20
)

// Errors that say why a folder or a title cannot be read.
var (
	ErrMissing = errors.New("the folder does not exist")
	ErrDenied  = errors.New("Overscan may not read the folder")
	ErrNotDir  = errors.New("the path is not a folder")
	// ErrNoTitle is returned for a film or a series that the folder no
	// longer holds.
	ErrNoTitle = errors.New("the folder no longer holds it")
	// ErrTooMany is returned for a folder that holds more videos than
	// Overscan lists.
	ErrTooMany = fmt.Errorf("the folder holds more than %d videos", maxVideos)
)

// Folder is a folder of films and series, open for reading.
type Folder struct {
	root *os.Root
	fsys fs.FS
	name string
}

// Open opens the folder at dir, an absolute path.
func Open(dir string) (*Folder, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("the folder's path is not absolute")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, openError(err)
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, openError(err)
	}
	return &Folder{root: root, fsys: root.FS(), name: filepath.Base(filepath.Clean(dir))}, nil
}

// openError says why a folder cannot be opened, without its path.
func openError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrMissing
	case errors.Is(err, fs.ErrPermission):
		return ErrDenied
	case errors.Is(err, syscall.ENOTDIR):
		return ErrNotDir
	}
	return errors.New("the folder cannot be read")
}

// Close closes the folder.
func (f *Folder) Close() error { return f.root.Close() }

// Name is the folder's own name, the last part of its path.
func (f *Folder) Name() string { return f.name }

// Check reports whether the folder can be read.
func (f *Folder) Check() error {
	if _, err := fs.ReadDir(f.fsys, "."); err != nil {
		return openError(err)
	}
	return nil
}

// Film is a film in the folder.
type Film struct {
	Key       string // the video's path in the folder, with slashes
	Name      string
	Year      int // 0 if not known
	Container string
	Added     time.Time // when the file last changed
	Info      Info      // what its .nfo file says, if it has one
	// Poster and Backdrop are the addresses of its pictures, "" if none.
	// ReadPicture reads them.
	Poster, Backdrop string
	// TMDBID is its ID on TMDB, from its .nfo file or a tag in its name or
	// its folder's, as in [tmdbid-123]; 0 if none (D88).
	TMDBID int64
}

// Show is a series in the folder.
type Show struct {
	// Key is "dir:" and the series' folder in the folder, or for episodes
	// that are not in a series' folder, "name:" and the series' name
	// without case or punctuation.
	Key      string
	Name     string
	Year     int
	Added    time.Time // when its latest episode last changed
	Info     Info      // what its tvshow.nfo says, if it has one
	Poster   string
	Backdrop string
	TMDBID   int64 // its ID on TMDB, from tvshow.nfo or a tag in its folder's name; 0 if none (D88)
	// Episodes are its episodes, by season and number, from Show alone.
	Episodes []Episode
}

// Episode is an episode of a series.
type Episode struct {
	Key       string // the video's path in the folder
	Season    int
	Number    int
	Name      string // "" if its file and .nfo file give none
	Container string
	Plot      string
	Runtime   time.Duration
	Picture   string
	Added     time.Time
}

// found is what a walk of the folder found.
type found struct {
	dirs    map[string]*dirFiles // by path
	videos  []video
	skipped int // what could not be read
}

// dirFiles are the pictures and .nfo files in a folder, by lower-case name.
type dirFiles map[string]file

type file struct {
	path string
	mod  time.Time
	size int64
}

type video struct {
	file
	name string // without its extension
	ext  string
}

// walk walks the folder from start and finds its videos, and the pictures
// and .nfo files beside them. It leaves out hidden folders, a title's
// extras and what is deeper than maxDepth.
func (f *Folder) walk(ctx context.Context, start string) (*found, error) {
	w := &found{dirs: map[string]*dirFiles{}}
	err := fs.WalkDir(f.fsys, start, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if p == start {
				return err
			}
			w.skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != start && (skipFolder(d.Name()) || strings.Count(p, "/") >= maxDepth) {
				return fs.SkipDir
			}
			return nil
		}
		ext := extension(d.Name())
		isVideo := videoExtensions[ext]
		if !isVideo && ext != "nfo" && !slices.Contains(pictureExtensions, ext) {
			return nil
		}
		info, err := fs.Stat(f.fsys, p) // follows a link that stays inside
		if err != nil || !info.Mode().IsRegular() {
			if isVideo {
				w.skipped++
			}
			return nil
		}
		fl := file{path: p, mod: info.ModTime(), size: info.Size()}
		if !isVideo {
			dir := path.Dir(p)
			if w.dirs[dir] == nil {
				w.dirs[dir] = &dirFiles{}
			}
			(*w.dirs[dir])[strings.ToLower(d.Name())] = fl
			return nil
		}
		if len(w.videos) == maxVideos {
			return ErrTooMany
		}
		w.videos = append(w.videos, video{file: fl, name: base(d.Name()), ext: ext})
		return nil
	})
	if err != nil && !errors.Is(err, ErrTooMany) && !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) {
		err = openError(err)
	}
	return w, err
}

// files returns the pictures and .nfo files in a folder.
func (w *found) files(dir string) dirFiles {
	if d := w.dirs[dir]; d != nil {
		return *d
	}
	return dirFiles{}
}

// kind of a video: a film or an episode, and for an episode, its series.
type kind struct {
	episode bool
	season  int
	number  int
	name    string // an episode's own name, from its file's
	show    string // the series' Key
	showDir string // the series' folder, "" for one known by name
	title   string // the series' name, from its folder or the file
	year    int
}

// classify tells what a video is: a film, an episode or an extra (ok
// false).
func (f *Folder) classify(v video) (k kind, ok bool) {
	if isExtra(v.name) {
		return kind{}, false
	}
	dir := path.Dir(v.path)
	dirName := path.Base(dir)
	if dir == "." {
		dirName = f.name
	}
	season, inSeason := seasonOfFolder(dirName)
	if dir == "." {
		inSeason = false
	}
	s, n, before, after, isEpisode := episodeOf(v.name)
	if !isEpisode && inSeason {
		n, after, isEpisode = numberInSeason(v.name)
		s = season
	}
	if !isEpisode {
		return kind{}, true
	}
	k = kind{episode: true, season: s, number: n, name: episodeName(after)}
	showName := seriesName(before)
	switch {
	case inSeason:
		k.showDir = path.Dir(dir)
	case dir == "." && (showName == "" || strings.HasPrefix(normal(f.name), normal(showName))):
		k.showDir = "." // the folder is one series' own
	case dir != "." && (showName == "" || strings.HasPrefix(normal(dirName), normal(showName))):
		k.showDir = dir
	}
	if k.showDir != "" {
		folderName := f.name
		if k.showDir != "." {
			folderName = path.Base(k.showDir)
		}
		k.show = "dir:" + k.showDir
		k.title, k.year = cleanName(folderName)
		if k.title == "" {
			k.title = folderName
		}
		return k, true
	}
	if normal(showName) == "" {
		return kind{}, false
	}
	k.show = "name:" + normal(showName)
	k.title = showName
	return k, true
}

// Count walks the folder and counts its films and series, without reading
// their .nfo files, and the videos that could not be read.
func (f *Folder) Count(ctx context.Context) (films, series, skipped int, err error) {
	w, err := f.walk(ctx, ".")
	if err != nil {
		return 0, 0, 0, err
	}
	shows := map[string]bool{}
	for _, v := range w.videos {
		k, ok := f.classify(v)
		switch {
		case !ok:
		case k.episode:
			shows[k.show] = true
		default:
			films++
		}
	}
	return films, len(shows), w.skipped, nil
}

// Films returns the folder's films, by name, and how many videos could not
// be read.
func (f *Folder) Films(ctx context.Context) ([]Film, int, error) {
	w, err := f.walk(ctx, ".")
	if err != nil {
		return nil, 0, err
	}
	single := w.filmsByDir(f)
	var films []Film
	for _, v := range w.videos {
		k, ok := f.classify(v)
		if !ok || k.episode {
			continue
		}
		films = append(films, f.film(ctx, w, v, single[path.Dir(v.path)] == 1))
	}
	slices.SortFunc(films, func(a, b Film) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Key, b.Key))
	})
	return films, w.skipped, nil
}

// filmsByDir counts the films in each folder.
func (w *found) filmsByDir(f *Folder) map[string]int {
	n := map[string]int{}
	for _, v := range w.videos {
		if k, ok := f.classify(v); ok && !k.episode {
			n[path.Dir(v.path)]++
		}
	}
	return n
}

// Film returns the film with the given key, or ErrNoTitle.
func (f *Folder) Film(ctx context.Context, key string) (Film, error) {
	if !fs.ValidPath(key) || key == "." || !videoExtensions[extension(key)] {
		return Film{}, ErrNoTitle
	}
	dir := path.Dir(key)
	w := &found{dirs: map[string]*dirFiles{}}
	entries, err := fs.ReadDir(f.fsys, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Film{}, ErrNoTitle
		}
		return Film{}, openError(err)
	}
	var target *video
	films := 0
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		info, err := fs.Stat(f.fsys, p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		fl := file{path: p, mod: info.ModTime(), size: info.Size()}
		ext := extension(e.Name())
		switch {
		case videoExtensions[ext]:
			v := video{file: fl, name: base(e.Name()), ext: ext}
			if k, ok := f.classify(v); ok && !k.episode {
				films++
				if p == key {
					target = &v
				}
			}
		case ext == "nfo" || slices.Contains(pictureExtensions, ext):
			if w.dirs[dir] == nil {
				w.dirs[dir] = &dirFiles{}
			}
			(*w.dirs[dir])[strings.ToLower(e.Name())] = fl
		}
	}
	if target == nil {
		return Film{}, ErrNoTitle
	}
	return f.film(ctx, w, *target, films == 1), nil
}

// genericNames are names of a film's file that say nothing of the film, so
// its folder names it.
var genericNames = map[string]bool{"movie": true, "film": true, "video": true, "main": true, "feature": true}

// film makes a film of a video. alone says whether it is the only film in
// its folder, which is then the film's own when its name says so: the
// folder names the film, and its pictures and movie.nfo are the film's.
func (f *Folder) film(ctx context.Context, w *found, v video, alone bool) Film {
	dir := path.Dir(v.path)
	files := w.files(dir)
	name, year := cleanName(v.name)
	own := false
	if alone && dir != "." {
		folderName, folderYear := cleanName(path.Base(dir))
		if folderName != "" && (sameName(folderName, name) || folderYear > 0 || genericNames[strings.ToLower(v.name)]) {
			own = true
			name, year = folderName, cmp.Or(folderYear, year)
		}
	}
	if name == "" {
		name = v.name
	}
	film := Film{Key: v.path, Name: name, Year: year, Container: v.ext, Added: v.mod}
	nfoNames := []string{strings.ToLower(v.name) + ".nfo"}
	if own {
		nfoNames = append(nfoNames, "movie.nfo")
	}
	if info, ok := f.readNFO(ctx, files, nfoNames...); ok {
		film.Info = info
		if info.Title != "" {
			film.Name = info.Title
		}
		film.Year = cmp.Or(info.Year, film.Year)
	}
	film.TMDBID = film.Info.TMDBID
	if film.TMDBID == 0 {
		film.TMDBID = tmdbIDOf(v.name)
	}
	if film.TMDBID == 0 && own {
		film.TMDBID = tmdbIDOf(path.Base(dir))
	}
	lower := strings.ToLower(v.name)
	posters := []string{lower + "-poster", lower}
	backdrops := []string{lower + "-fanart", lower + "-backdrop"}
	if own {
		posters = append(posters, "poster", "folder", "cover", "default", "movie")
		backdrops = append(backdrops, "fanart", "backdrop", "background", "art")
	}
	film.Poster = files.picture(posters...)
	film.Backdrop = files.picture(backdrops...)
	return film
}

// Shows returns the folder's series, by name, without their episodes, and
// how many videos could not be read.
func (f *Folder) Shows(ctx context.Context) ([]Show, int, error) {
	w, err := f.walk(ctx, ".")
	if err != nil {
		return nil, 0, err
	}
	shows := f.shows(ctx, w, "", false)
	return shows, w.skipped, nil
}

// Show returns the series with the given key, with its episodes, or
// ErrNoTitle.
func (f *Folder) Show(ctx context.Context, key string) (Show, error) {
	start := "."
	if dir, ok := strings.CutPrefix(key, "dir:"); ok {
		// A series' folder that is gone, or that a link takes outside,
		// holds it no longer.
		if info, err := fs.Stat(f.fsys, dir); !fs.ValidPath(dir) || err != nil || !info.IsDir() {
			return Show{}, ErrNoTitle
		}
		start = dir
	} else if !strings.HasPrefix(key, "name:") {
		return Show{}, ErrNoTitle
	}
	w, err := f.walk(ctx, start)
	if err != nil {
		return Show{}, err
	}
	shows := f.shows(ctx, w, key, true)
	if len(shows) == 0 {
		return Show{}, ErrNoTitle
	}
	return shows[0], nil
}

// shows makes the series of a walk's episodes: all of them, or only the one
// with the key only, with their episodes if episodes says so.
func (f *Folder) shows(ctx context.Context, w *found, only string, episodes bool) []Show {
	byKey := map[string]*Show{}
	var order []string
	for _, v := range w.videos {
		k, ok := f.classify(v)
		if !ok || !k.episode || only != "" && k.show != only {
			continue
		}
		s := byKey[k.show]
		if s == nil {
			s = &Show{Key: k.show, Name: k.title, Year: k.year}
			if k.showDir != "" {
				files := w.files(k.showDir)
				if info, ok := f.readNFO(ctx, files, "tvshow.nfo"); ok {
					s.Info = info
					if info.Title != "" {
						s.Name = info.Title
					}
					s.Year = cmp.Or(info.Year, s.Year)
				}
				s.TMDBID = s.Info.TMDBID
				if s.TMDBID == 0 {
					s.TMDBID = tmdbIDOf(path.Base(k.showDir))
				}
				s.Poster = files.picture("poster", "folder", "cover", "default", "show")
				s.Backdrop = files.picture("fanart", "backdrop", "background", "art")
			}
			byKey[k.show] = s
			order = append(order, k.show)
		}
		if v.mod.After(s.Added) {
			s.Added = v.mod
		}
		if episodes {
			s.Episodes = append(s.Episodes, f.episode(ctx, w, v, k))
		}
	}
	out := make([]Show, 0, len(order))
	for _, key := range order {
		s := byKey[key]
		slices.SortStableFunc(s.Episodes, func(a, b Episode) int {
			return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Number, b.Number), cmp.Compare(a.Key, b.Key))
		})
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Show) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Key, b.Key))
	})
	return out
}

// episode makes an episode of a video, with what its .nfo file says.
func (f *Folder) episode(ctx context.Context, w *found, v video, k kind) Episode {
	files := w.files(path.Dir(v.path))
	lower := strings.ToLower(v.name)
	e := Episode{Key: v.path, Season: k.season, Number: k.number, Name: k.name, Container: v.ext, Added: v.mod,
		Picture: files.picture(lower+"-thumb", lower)}
	if info, ok := f.readNFO(ctx, files, lower+".nfo"); ok {
		e.Name = cmp.Or(info.Title, e.Name)
		e.Plot = info.Plot
		e.Runtime = info.Runtime
		if info.Season >= 0 && info.Episode >= 0 {
			e.Season, e.Number = info.Season, info.Episode
		}
	}
	return e
}

// readNFO reads the first of the named .nfo files that there is and that
// reads as one.
func (f *Folder) readNFO(ctx context.Context, files dirFiles, names ...string) (Info, bool) {
	for _, name := range names {
		fl, ok := files[name]
		if !ok || fl.size > maxNFOBytes || ctx.Err() != nil {
			continue
		}
		data, err := fs.ReadFile(f.fsys, fl.path)
		if err != nil {
			continue
		}
		if info, ok := parseNFO(data); ok {
			return info, true
		}
	}
	return Info{}, false
}

// picture returns the address of the first picture with one of the names,
// without their extensions, "" if there is none.
func (files dirFiles) picture(names ...string) string {
	for _, name := range names {
		for _, ext := range pictureExtensions {
			if fl, ok := files[name+"."+ext]; ok && fl.size > 0 && fl.size <= MaxPictureBytes {
				return pictureAddress(fl)
			}
		}
	}
	return ""
}

// pictureAddress is the address of a picture in the folder: its path, after
// its time and size, so that the address changes when the picture does.
func pictureAddress(fl file) string {
	return "local:" + strconv.FormatInt(fl.mod.Unix(), 10) + "." + strconv.FormatInt(fl.size, 10) + ":" + fl.path
}

// IsPicture reports whether an address is a picture in a folder.
func IsPicture(address string) bool { return strings.HasPrefix(address, "local:") }

// ReadPicture reads the picture at address in the folder at dir, at most
// MaxPictureBytes of it.
func ReadPicture(dir, address string) ([]byte, error) {
	rest, ok := strings.CutPrefix(address, "local:")
	_, p, found := strings.Cut(rest, ":")
	if !ok || !found || !fs.ValidPath(p) || !slices.Contains(pictureExtensions, extension(p)) {
		return nil, errors.New("not the address of a picture in a folder")
	}
	f, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	file, err := f.root.Open(p)
	if err != nil {
		return nil, ErrNoTitle
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxPictureBytes+1))
	if err != nil {
		return nil, errors.New("the picture cannot be read")
	}
	if len(data) > MaxPictureBytes {
		return nil, errors.New("the picture is too large")
	}
	return data, nil
}

// OpenVideo opens the video at a film's or an episode's key in the folder at
// dir, for playing.
func OpenVideo(dir, key string) (*os.File, error) {
	if !fs.ValidPath(key) || !videoExtensions[extension(key)] {
		return nil, ErrNoTitle
	}
	f, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	file, err := f.root.Open(key)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, ErrNoTitle
	case err != nil:
		return nil, openError(err)
	}
	return file, nil
}
