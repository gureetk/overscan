# Themes

A theme changes how the boxes that run Overscan's page look: the colours, the corners of what they show, the font, and the name beside the mark. It does not change the layout, what the page does or what it says, so every theme shows the same screens. The administrator installs themes in the admin and chooses one for the household and, if they like, one for each profile ([D97](decisions.md)). Stalker clients keep their own look.

## A theme file

A theme is a small JSON file. This one is in [`themes/evergreen.json`](themes/evergreen.json):

```json
{
  "overscan_theme": 1,
  "name": "Evergreen",
  "background": "#0e1512",
  "text": "#eef4f0",
  "accent": "#5bd69c",
  "focus": "#f4d35e",
  "focus_text": "#1a1405",
  "corners": "small",
  "brand": "The Den"
}
```

| Field | | What it is |
|---|---|---|
| `overscan_theme` | Needed | The file's format: `1`. |
| `name` | Needed | The theme's name in the admin, 1 to 40 characters, other than *Overscan*. A theme with the name of one installed already, in any case, replaces it. |
| `background` | Needed | The colour of the page's background, as `#rrggbb` or `#rgb`. |
| `text` | Needed | The colour of the text. Panels, lines, tiles and quieter text take colours between the background and the text, in the places Overscan's own greys have between its black and white, with their slight tint. |
| `accent` | | What Overscan shows in teal: progress bars, the guide's line for now, a few marks. Teal if not given. |
| `focus` | | The fill of the item in focus. The text colour if not given. |
| `focus_text` | | The text on the item in focus. The background colour if not given. |
| `font` | | `"Instrument Sans"`, the page's own and the default, or `"Sans"`, the box's own sans-serif font. |
| `corners` | | `"round"`, as Overscan's own and the default, `"small"`, half as round, or `"square"`. Round things, such as dots, stay round. |
| `brand` | | A name to show beside the mark in place of *Overscan*: up to 24 letters, digits and spaces, and the marks `' & . , ! ? -`. The mark itself stays. |

Nothing else may be in the file, and it may hold one theme. A file of more than 16 KB is refused: a theme takes a few lines.

## What Overscan checks

Overscan checks a file before it keeps it, and says what is wrong with one it refuses. Besides the form above, it checks that what the box shows stays readable on a television, with contrast as WCAG 2 measures it:

- the text against the background, at least 4.5 to 1;
- the text on the item in focus against its fill, at least 4.5 to 1;
- the item in focus against the background, at least 1.5 to 1, so that it stands out;
- the accent against the background, at least 2 to 1.

Overscan keeps the theme as it read it, with colours written out in full, in lower case. *Download* in the admin gives that file back, to change and install again.

## Installing and choosing

In the admin, on the *Settings* page, under *Themes*:

1. Choose the file and *Install*. The theme is listed with a small picture of its colours, and the log says `a theme installed in the admin`.
2. Choose it as *The household's theme*. The household's boxes show it within five minutes, or at once after *Settings → Reload the Portal* on the box. *Overscan* puts Overscan's own look back.
3. Once the household has two profiles, each has a choice of its own, *The household's* at first. A box shows the theme of the profile it plays as, else the household's.

*Remove* takes a theme away: what showed it shows the household's theme instead, or Overscan's own.

The admin's API does the same: `GET /themes` lists the themes and the household's, `POST /themes` installs the file that is the request's body, sent as `application/json`, `DELETE /themes/{id}` removes one, `PUT /theme` with `{"theme_id": 3}` chooses the household's (`0` for Overscan's own), and `PATCH /profiles/{id}` with `{"theme_id": 3}` a profile's (`0` for the household's). The [OpenAPI document](api/admin.yaml) has the details.

## How it works

The box's browser predates CSS variables, so a theme cannot just set a few colours that the page's stylesheet reads. Instead, the server writes a stylesheet for each theme from the page's own: each of the page's greys becomes the colour at the same place between the theme's background and text, the colours of the item in focus become the theme's focus colours in the same way, teal becomes the accent, the radii of corners are halved or taken away, the font name goes if the theme asks for the box's own, and a rule for the brand comes last. Overscan's own colours give the page's stylesheet back unchanged.

The page learns which stylesheet to load from the [device API](api/device.md)'s profiles, which it asks for when it starts and every five minutes, and loads it after its own, which it overrides. The stylesheet's name holds a hash of the theme and of the page's stylesheet, so the box keeps it as long as neither changes, and a new version of either makes a new name.

## Examples

Two themes to try, both made up for Overscan:

- [`themes/evergreen.json`](themes/evergreen.json): dark green, with a yellow focus, small corners, and *The Den* beside the mark;
- [`themes/paper.json`](themes/paper.json): light, like paper, with a rust accent, square corners and the box's own font.

## What a theme cannot do

- Change the layout, hide anything, or add words other than the brand.
- Hold pictures: a background, or a logo in place of the mark.
- Bring a stylesheet of its own.

A later format could add pictures or a stylesheet. No theme that imitates another product's portal ships with Overscan or enters the repository ([clean-room policy](clean-room.md)).
