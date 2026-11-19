<!-- SPDX-License-Identifier: Apache-2.0 -->

# TMDB notes

What Overscan asks of The Movie Database (TMDB) for the films and series in folders on the server, with a key that the administrator gives (D88), and what TMDB asks of applications in return.

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real source confirms it). Nothing here is Observed yet: the workspace that wrote Overscan's TMDB client could not reach TMDB, and the client was tested against a fake written from these notes.

1. TMDB's developer documentation: [Getting Started](https://developer.themoviedb.org), [Append To Response](https://developer.themoviedb.org/docs/append-to-response), the [movie details](https://developer.themoviedb.org/docs/movie-details) method and the [FAQ](https://developer.themoviedb.org/docs/faq).
2. The documentation of [golang-tmdb](https://pkg.go.dev/github.com/cyruzin/golang-tmdb) (MIT), a Go client: the JSON names of its types. Only the documentation was read, never the client's code.
3. Answers on TMDB's support forum about its limits, by its staff and members, linked from [this thread](https://www.themoviedb.org/talk/67c3413c79656ddf5a6dfd1c).
4. Guides that show requests to the API: [Apidog's](https://apidog.com/blog/tmdb-api-key/).

## 1. The key and the requests

- The API is at `https://api.themoviedb.org/3`. **Documented** [4].
- A key comes from the API section of an account's settings on TMDB. **Documented** [1]. There are two kinds: an API key, sent as the `api_key` parameter, and an API read access token, sent as `Authorization: Bearer <token>`. **Documented** [4]. Overscan takes a key that has the form of a JSON web token (three parts, starting `eyJ`) for a token, and anything else for an API key. **Assumed.**
- `language` asks for text in a language such as `pt-BR`; without it, TMDB answers in `en-US`. **Documented** [1, 4].
- `GET /configuration` holds the API's configuration, images included. **Documented** [1]. Overscan asks it to check a key: TMDB answers 401 for a key it refuses. **Assumed.**
- A detail method takes `append_to_response`, a comma-separated list of methods of the same namespace, at most 20, whose answers come in the same answer: the details of a film, a series, a season, an episode and a person. **Documented** [1].
- Use HTTPS. **Documented** [1].

## 2. What Overscan asks

- `GET /search/movie` with `query`, `year`, `include_adult=false` and `page`. **Documented** [4]. A result has `id`, `title`, `original_title`, `release_date`, `overview`, `poster_path` and `backdrop_path`, among others. **Documented** [2].
- `GET /search/tv` with `query`, `first_air_date_year` and `include_adult=false`. **Assumed**, by the form of the film search. A result has `id`, `name`, `first_air_date` and `poster_path`. **Documented** [2]; `original_name` is **Assumed**.
- `GET /movie/{id}?append_to_response=credits`: `title`, `overview`, `release_date`, `runtime` (minutes), `genres` (each with a `name`), `poster_path`, `backdrop_path` and `imdb_id`; `vote_average`, out of 10; and in `credits`, the cast with `name`, `character` and `order`, and the crew with `name` and `job`. **Documented** [1, 2].
- `GET /tv/{id}?append_to_response=credits`: `name`, `first_air_date`, `overview`, `genres`, `credits`, and `seasons`, each with `season_number`, `name`, `overview`, `poster_path` and `episode_count`. **Documented** [2]. `poster_path`, `backdrop_path` and `vote_average` as for a film. **Assumed.**
- `GET /tv/{id}/season/{number}`: the season's `episodes`, each with `episode_number`, `name`, `overview`, `still_path`, `runtime` and `air_date`. **Documented** [2].
- A title that TMDB does not know answers 404. **Assumed.**

## 3. Pictures

- A picture is at `https://image.tmdb.org/t/p/`, then a size, then its path, such as `w500` and `/abc.jpg`. Sizes include `w92`, `w185`, `w342`, `w500`, `w780`, `w1280` and `original`. **Documented** [4], and the configuration lists them [1]. Overscan asks for `w342` posters, `w1280` backdrops and `w300` stills, and keeps its own copy of each, as it does every picture (D54).

## 4. Limits

- TMDB stopped its limit of 40 requests in 10 seconds in December 2019; it limits each address to about 20 connections at once, to the API and to the pictures alike, and answers 429 to too many. **Documented** [3]. Overscan makes one request at a time, at least 300 ms apart when it looks titles up in the background, and stops at a 429.

## 5. What TMDB asks of applications

- Show the TMDB logo, and the notice "This product uses the TMDB API but is not endorsed or certified by TMDB.", prominently, in an "About" or "Credits" section; the logo less prominent than the application's own, and one of TMDB's approved logos, unaltered. Call it "TMDB" or "The Movie Database", and link to themoviedb.org. **Documented** [1].
- The API is free for use that is not commercial, with attribution; commercial use needs a licence from TMDB. **Documented** [1].
- Overscan ships no key: each administrator gives their own, under TMDB's terms. Its Settings page shows the notice beside the key, with a link to TMDB; it does not show TMDB's logo yet (D88).
