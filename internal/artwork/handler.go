// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/adapters/plex"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Handler serves GET /artwork/logo/{entry}/{key}: the logo of a lineup
// entry that devices see, while key is still its logo's (LogoPath); and
// /artwork/poster/{title}/{key}, /artwork/thumb/{title}/{key} (a poster,
// small), /artwork/backdrop/{title}/{key} and /artwork/still/{episode}/{key},
// the pictures of films, series and episodes that devices see (TitlePath). Pictures are no secret, and boxes
// and Stalker clients load them as plain images, so this needs no session.
func Handler(st *store.Store, c *Cache, log *slog.Logger) http.Handler {
	mux := routes.NewMux()
	serve := func(w http.ResponseWriter, r *http.Request, kind Kind, lookup func(id int64) ([]string, store.ArtSource, error)) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.NotFound(w, r)
			return
		}
		addresses, src, err := lookup(id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			log.Error("cannot look up a picture", "kind", kind.Name, "id", id, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		address := ""
		for _, a := range addresses {
			if a != "" && Key(a) == r.PathValue("key") {
				address = a
				break
			}
		}
		if address == "" {
			http.NotFound(w, r)
			return
		}
		var file, contentType string
		switch {
		case plex.IsPicture(address):
			if src.Type != source.TypePlex {
				err = ErrUnusable
				break
			}
			// A Plex server's picture needs the token, which only the
			// source's sealed settings hold: the address kept has none
			// (D72).
			file, contentType, err = c.GetWith(r.Context(), kind, address, func() ([]byte, error) {
				settings, err := st.SourceSettings(context.Background(), src.ID)
				if err != nil {
					return nil, err
				}
				fetch := plex.New(settings, nil, "").PictureFor(address)
				if fetch == "" {
					return nil, ErrUnusable
				}
				return c.get(fetch, src.UserAgent, src.PublicOnly)
			})
		case !folder.IsPicture(address):
			file, contentType, err = c.GetWith(r.Context(), kind, address, func() ([]byte, error) {
				return c.get(address, src.UserAgent, src.PublicOnly)
			})
		case src.Type == source.TypeFolder:
			// A picture in a folder of films is read from the folder, which
			// it cannot leave (D67).
			file, contentType, err = c.GetWith(r.Context(), kind, address, func() ([]byte, error) {
				settings, err := st.SourceSettings(context.Background(), src.ID)
				if err != nil {
					return nil, err
				}
				return folder.ReadPicture(settings.URL, address)
			})
		default:
			err = ErrUnusable
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		// The path changes with the picture, so it may be kept for good.
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		http.ServeContent(w, r, "", info.ModTime(), f)
	}
	mux.HandleFunc("GET /artwork/logo/{id}/{key}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, LogoKind, func(id int64) ([]string, store.ArtSource, error) {
			logo, src, err := st.EntryLogo(r.Context(), id)
			return []string{logo}, store.ArtSource{UserAgent: src.UserAgent, PublicOnly: src.PublicOnly}, err
		})
	})
	titleArt := func(r *http.Request, backdrop bool) func(id int64) ([]string, store.ArtSource, error) {
		return func(id int64) ([]string, store.ArtSource, error) {
			posters, backdrops, src, err := st.TitleArt(r.Context(), id)
			if backdrop {
				return backdrops, src, err
			}
			return posters, src, err
		}
	}
	mux.HandleFunc("GET /artwork/poster/{id}/{key}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, PosterKind, titleArt(r, false))
	})
	mux.HandleFunc("GET /artwork/thumb/{id}/{key}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, ThumbKind, titleArt(r, false))
	})
	mux.HandleFunc("GET /artwork/backdrop/{id}/{key}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, BackdropKind, titleArt(r, true))
	})
	mux.HandleFunc("GET /artwork/still/{id}/{key}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, StillKind, func(id int64) ([]string, store.ArtSource, error) {
			picture, src, err := st.EpisodeArt(r.Context(), id)
			return []string{picture}, src, err
		})
	})
	mux.HandleFunc("/artwork/", http.NotFound)
	return mux
}
