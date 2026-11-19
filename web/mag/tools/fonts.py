# SPDX-License-Identifier: AGPL-3.0-or-later
"""Makes the MAG page's fonts from Instrument Sans (SIL OFL 1.1,
https://github.com/Instrument/instrument-sans): the four weights the page
uses, cut down to Latin text, with tabular figures as the default figures,
as WOFF. Needs fontTools.

    python3 fonts.py UPSTREAM/fonts/ttf ../src/assets/fonts
"""
import sys
from fontTools.ttLib import TTFont
from fontTools import subset

SRC, OUT = sys.argv[1], sys.argv[2]
WEIGHTS = {400: 'Regular', 500: 'Medium', 600: 'SemiBold', 700: 'Bold'}
UNICODES = (list(range(0x20, 0x7f)) + list(range(0xa0, 0x180)) + list(range(0x2010, 0x2028))
            + list(range(0x2030, 0x203b)) + [0x20ac, 0x2122, 0x2212, 0x2190, 0x2191, 0x2192, 0x2193])

for weight, style in WEIGHTS.items():
    font = TTFont(f'{SRC}/InstrumentSans-{style}.ttf')
    # Tabular figures by default: the box may not apply font features, and
    # times, numbers and clocks line up in columns.
    gsub = font['GSUB'].table
    tnum = {}
    for fr in gsub.FeatureList.FeatureRecord:
        if fr.FeatureTag == 'tnum':
            for li in fr.Feature.LookupListIndex:
                for st in gsub.LookupList.Lookup[li].SubTable:
                    tnum.update(getattr(st, 'mapping', {}) or {})
    for table in font['cmap'].tables:
        for code, glyph in list(table.cmap.items()):
            if glyph in tnum and (0x30 <= code <= 0x39 or code in (0x2b, 0x2212)):
                table.cmap[code] = tnum[glyph]
    opts = subset.Options()
    opts.layout_features = ['kern', 'ccmp', 'locl', 'liga']
    opts.name_IDs = ['*']
    opts.name_languages = ['*']
    opts.notdef_outline = True
    opts.glyph_names = False
    opts.hinting = True
    sub = subset.Subsetter(opts)
    sub.populate(unicodes=UNICODES)
    sub.subset(font)
    font.flavor = 'woff'
    name = f'{OUT}/instrument-sans-{weight}.woff'
    font.save(name)
    print(name)
