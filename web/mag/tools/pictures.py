# SPDX-License-Identifier: AGPL-3.0-or-later
"""Draws the home screen's pictures for Overscan's MAG page: landscapes
made from gradients and noise, so that they are Overscan's own. The same
seed always gives the same pictures. Needs NumPy and Pillow.

    python3 pictures.py ../src/assets/pictures
"""
import sys

import numpy as np
from PIL import Image, ImageFilter

W, H = 1280, 720


def rng(seed):
    return np.random.default_rng(seed)


def hexrgb(h):
    h = h.lstrip('#')
    return np.array([int(h[i:i + 2], 16) for i in (0, 2, 4)], dtype=np.float64)


def gradient(stops, n):
    """A 1-D gradient of n colours through stops [(pos, '#rrggbb'), ...]."""
    pos = np.array([p for p, _ in stops])
    cols = np.array([hexrgb(c) for _, c in stops])
    t = np.linspace(0, 1, n)
    return np.stack([np.interp(t, pos, cols[:, k]) for k in range(3)], axis=1)


def vertical(stops):
    g = gradient(stops, H)
    return np.repeat(g[:, None, :], W, axis=1)


def fractal(r, n, octaves=6, persistence=0.5, base=8):
    """1-D fractal noise in [-1, 1] over n samples."""
    out = np.zeros(n)
    amp, total = 1.0, 0.0
    for o in range(octaves):
        k = base * 2 ** o
        pts = r.uniform(-1, 1, k + 3)
        x = np.linspace(0, k, n)
        i = np.floor(x).astype(int)
        f = x - i
        f = f * f * (3 - 2 * f)
        out += amp * (pts[i] * (1 - f) + pts[i + 1] * f)
        total += amp
        amp *= persistence
    return out / total


def glow(img, cx, cy, radius, colour, strength):
    yy, xx = np.mgrid[0:H, 0:W]
    d = np.sqrt(((xx - cx) / radius) ** 2 + ((yy - cy) / (radius * 0.75)) ** 2)
    a = np.clip(1 - d, 0, 1) ** 2.2 * strength
    return img * (1 - a[..., None]) + hexrgb(colour) * a[..., None]


def ridge_layer(img, ridge, colour, haze_colour=None, haze=0.0):
    """Fills below the ridge line with colour, lighter towards its top if haze."""
    yy = np.arange(H)[:, None]
    mask = yy >= ridge[None, :]
    col = np.broadcast_to(hexrgb(colour), img.shape).copy()
    if haze_colour is not None and haze > 0:
        depth = np.clip((yy - ridge[None, :]) / 140.0, 0, 1)
        a = (1 - depth) * haze
        col = col * (1 - a[..., None]) + hexrgb(haze_colour) * a[..., None]
    # Soften the edge by a pixel.
    edge = np.clip(yy - ridge[None, :] + 1, 0, 1)
    m = (mask * edge)[..., None]
    return img * (1 - m) + col * m


def grain(img, r, amount):
    return img + r.normal(0, amount, img.shape[:2])[..., None]


def save(img, path):
    im = Image.fromarray(np.clip(img, 0, 255).astype(np.uint8), 'RGB')
    im.save(path, 'JPEG', quality=84, optimize=True)
    print(path)


def dusk(out):
    r = rng(11)
    img = vertical([(0, '#151a33'), (0.28, '#2b2a55'), (0.46, '#6a4a7a'), (0.58, '#c0707a'),
                    (0.66, '#f0a36a'), (1, '#f6c27a')])
    img = glow(img, W * 0.66, H * 0.60, 520, '#ffd59a', 0.55)
    img = glow(img, W * 0.66, H * 0.61, 130, '#fff1d6', 0.8)
    layers = [
        (0.55, 110, '#6f5680', '#c88c8a', 0.45, 5),
        (0.61, 120, '#4f3f66', '#a87884', 0.4, 6),
        (0.68, 120, '#342a4c', '#7a5c78', 0.35, 6),
        (0.76, 110, '#201b33', '#4c3d5c', 0.3, 7),
        (0.86, 80, '#100e1a', '#2a2236', 0.2, 7),
    ]
    for base, amp, colour, hazec, haze, octv in layers:
        ridge = H * base + amp * fractal(r, W, octaves=octv, base=2)
        img = ridge_layer(img, ridge, colour, hazec, haze)
    img = grain(img, r, 2.2)
    save(img, f'{out}/dusk.jpg')


def aurora(out):
    r = rng(23)
    img = vertical([(0, '#03050c'), (0.45, '#06142a'), (0.72, '#0c2a3d'), (1, '#0a1a24')])
    # Stars.
    n = 900
    xs, ys = r.uniform(0, W, n), r.uniform(0, H * 0.75, n) ** 1.0
    bright = r.uniform(0.15, 1, n) ** 3
    stars = np.zeros((H, W))
    stars[ys.astype(int), xs.astype(int)] = bright * 255
    stars = np.array(Image.fromarray(np.clip(stars, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(0.6)), dtype=np.float64) * 1.8
    img = img + stars[..., None] * np.array([0.9, 0.95, 1.0])
    # Curtains of light: a wavy band, with rays along it.
    curtain = np.zeros((H, W, 3))
    yy = np.arange(H)[:, None]
    for k, (cy, amp, colour, top, strength) in enumerate([
        (0.36, 60, '#3ddc9a', '#5a7cff', 1.0),
        (0.26, 45, '#2fd3b0', '#7b5cff', 0.6),
        (0.46, 35, '#7ef0a0', '#3db7ff', 0.45),
    ]):
        x = np.arange(W)
        centre = H * cy + amp * np.sin(x / W * 2 * np.pi * (1.2 + 0.4 * k) + k) + 25 * fractal(r, W, octaves=4, base=4)
        rays = 0.55 + 0.45 * fractal(r, W, octaves=5, base=40)
        below = np.clip((yy - centre[None, :]) / 70.0, 0, 1)
        above = np.clip((centre[None, :] - yy) / 220.0, 0, 1)
        band = np.where(yy >= centre[None, :], np.exp(-below * 4), (1 - above) ** 1.6)
        a = band * rays[None, :] * strength
        mix = np.clip((centre[None, :] - yy) / 200.0, 0, 1)[..., None]
        col = hexrgb(colour) * (1 - mix) + hexrgb(top) * mix
        curtain += a[..., None] * col
    curtain = np.array(Image.fromarray(np.clip(curtain, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(3)), dtype=np.float64)
    img = img + curtain * 0.85
    # Hills and a line of trees.
    ridge = H * 0.80 + 30 * fractal(r, W, octaves=5, base=3)
    trees = np.zeros(W)
    x = 0
    while x < W:
        w = int(r.uniform(10, 26))
        h = r.uniform(18, 60)
        xs = np.arange(x, min(W, x + w))
        trees[xs] = np.maximum(trees[xs], h * (1 - np.abs(xs - x - w / 2) / (w / 2)))
        x += int(r.uniform(4, 14))
    img = ridge_layer(img, ridge - trees, '#04070b')
    img = grain(img, r, 2.0)
    save(img, f'{out}/aurora.jpg')


def coast(out):
    r = rng(37)
    horizon = int(H * 0.62)
    sky = vertical([(0, '#0a1630'), (0.30, '#1b3560'), (0.52, '#4d6f9c'), (0.62, '#a9b9cf'), (1, '#a9b9cf')])
    img = sky.copy()
    img = glow(img, W * 0.74, H * 0.22, 60, '#f4f1e6', 0.95)
    img = glow(img, W * 0.74, H * 0.22, 260, '#7f9cc6', 0.35)
    # The sea: darker, with the sky's light in streaks.
    yy = np.arange(H)[:, None]
    depth = np.clip((yy - horizon) / (H - horizon), 0, 1)
    sea = vertical([(0, '#8193b0'), (0.62, '#7a8daa'), (0.66, '#33496b'), (0.85, '#16243b'), (1, '#0b1322')])
    streak = np.zeros((H, W))
    for _ in range(420):
        y = int(horizon + (r.uniform(0, 1) ** 1.8) * (H - horizon))
        x0 = r.uniform(-100, W)
        ln = r.uniform(40, 260) * (1 + (y - horizon) / 80)
        xs = np.arange(int(max(0, x0)), int(min(W, x0 + ln)))
        streak[y, xs] += r.uniform(0.04, 0.14)
    streak = np.array(Image.fromarray(np.clip(streak * 255, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(1.2)), dtype=np.float64) / 255
    # The moon's path on the water.
    path = np.exp(-((np.arange(W) - W * 0.74) / (40 + 0 * np.arange(W))) ** 2)[None, :] * (1 - depth) ** 0.6
    shimmer = (r.uniform(0, 1, (H, W)) > 0.55) * path
    shimmer = np.array(Image.fromarray(np.clip(shimmer * 255, 0, 255).astype(np.uint8)).filter(ImageFilter.GaussianBlur(1.0)), dtype=np.float64) / 255
    water = sea + (streak[..., None] * 160 + shimmer[..., None] * 140) * np.array([0.85, 0.9, 1.0])
    below = (yy >= horizon)[..., None]
    img = np.where(below, water, img)
    # A headland on the left, with a few lights.
    land = horizon - (55 + 40 * fractal(r, W, octaves=5, base=2)) * np.clip(1 - np.arange(W) / (W * 0.42), 0, 1) ** 0.7
    headland = ((yy >= land[None, :]) & (yy <= horizon))[..., None]
    img = np.where(headland, hexrgb('#0d1626'), img)
    for _ in range(14):
        x = int(r.uniform(30, W * 0.33))
        y = int(r.uniform(land[x] + 6, horizon - 2))
        img = glow(img, x, y, 7, '#ffd9a0', 0.9)
    img = grain(img, r, 2.0)
    save(img, f'{out}/coast.jpg')


def dunes(out):
    r = rng(53)
    img = vertical([(0, '#3a2c4a'), (0.30, '#8a5a62'), (0.50, '#e09a6a'), (0.58, '#f5c48a'), (1, '#f5c48a')])
    img = glow(img, W * 0.22, H * 0.50, 420, '#ffe2b0', 0.5)
    x = np.arange(W)
    yy = np.arange(H)[:, None]
    specs = [
        (0.56, 40, '#d99a73', '#a8614f', 1.1, 0.4),
        (0.63, 55, '#c27e5e', '#7d4440', 0.9, 1.7),
        (0.72, 65, '#a3604a', '#5a2f32', 0.7, 3.1),
        (0.84, 70, '#7a4236', '#3a1f24', 0.55, 4.4),
    ]
    for base, amp, lit, shade, freq, phase in specs:
        crest = H * base - amp * (0.5 + 0.5 * np.sin(x / W * 2 * np.pi * freq + phase)) - 14 * fractal(r, W, octaves=4, base=3)
        slope = np.gradient(crest)
        # The side facing the sun, on the left, is lit; the other is in shade.
        light = np.clip(0.5 - slope * 0.35, 0, 1)
        col = hexrgb(lit)[None, :] * light[:, None] + hexrgb(shade)[None, :] * (1 - light[:, None])
        depth = np.clip((yy - crest[None, :]) / 220.0, 0, 1)[..., None]
        layer = col[None, :, :] * (1 - depth * 0.45)
        mask = (yy >= crest[None, :])[..., None]
        img = np.where(mask, layer, img)
    img = grain(img, r, 2.6)
    save(img, f'{out}/dunes.jpg')


if __name__ == '__main__':
    out = sys.argv[1]
    for fn in (dusk, aurora, coast, dunes):
        fn(out)
