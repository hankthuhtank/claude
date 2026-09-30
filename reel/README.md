# The Trading Desk: "Open to Close"

A 20-second motion reel for [thetradingdesk.org](https://thetradingdesk.org). It is built entirely in code: a deterministic canvas renderer with a synthesised, cue-locked score.

| File | Use |
| --- | --- |
| `out/thetradingdesk-reel-1080p60.mp4` | Master. 1920×1080, 60 fps, H.264 High (about 11 Mbps), AAC 320k, −13 LUFS / −1 dBTP |
| `out/thetradingdesk-reel-web.mp4` | Lighter encode (about 9.6 MB) for embedding on the site |
| `out/poster-title.png`, `out/poster-endcard.png` | Cover and thumbnail stills |

For the concept, palette, type, motion rules, beat sheet and research notes, see [DESIGN.md](DESIGN.md).

## How it's made

- **Picture.** `src/scenes.js` expresses every scene as a pure function of time. `src/main.js` accumulates 8 sub-frames per frame (32 sub-frames through whips and zooms) for a 180° shutter. It then adds bloom, chromatic aberration on impacts, vignette, a faint CRT scan and animated grain, all in canvas 2D.
- **Capture.** `render.cjs` drives headless Chromium through Playwright and pulls lossless PNGs over CDP from 3 parallel pages. It takes about 2 minutes for all 1200 frames.
- **Sound.** `audio/score.py` synthesises the whole track with numpy/scipy (drums, sub, pad, bells, foley, a sonified path) from `cues.json`. The renderer exports that cue list, so every hit lands on its frame.
- **Fonts.** Big Shoulders, Martian Mono and Doto (all OFL). `tools/make_fonts.py` pins the axes canvas can't reach.

## Rebuild

```bash
npm install                       # fonts + playwright (uses the preinstalled Chromium)
pip install numpy scipy pyloudnorm fonttools brotli imageio-ffmpeg pillow
python3 tools/make_fonts.py       # cut pinned font instances into src/fonts
node render.cjs --scale 1 --sub 8 --workers 3 --out frames --cues cues.json
python3 audio/score.py cues.json audio/score.wav
ffmpeg -framerate 60 -i frames/f%04d.png -i audio/score.wav -map 0:v -map 1:a \
  -c:v libx264 -preset slow -crf 16 -tune grain -pix_fmt yuv420p -colorspace bt709 \
  -color_primaries bt709 -color_trc bt709 -c:a aac -b:a 320k -movflags +faststart -t 20 out/reel.mp4
```

For a quick preview, use `node render.cjs --scale 0.5 --sub 1 --frames 120,240,480 --out scratch/p`, then `python3 tools/contact.py scratch/p sheet.png`.

## Vertical cut (TikTok / Reels)

`out/thetradingdesk-tiktok-1080x1920.mp4` is a 1080×1920, 60 fps H.264 file. It is built from `src/scenes-v.js` and rendered with `node render.cjs --vertical`.

It differs from the widescreen master in these ways:

- **Brand:** your exact colours, Chakra Petch and IBM Plex Mono, and the real logo (`src/assets/logo.png`) lit like a neon sign at the end.
- **Content:** your six instruments and the site's real counts.
- **Layout:** type is kept clear of the TikTok/Reels UI: the tabs at the top, the buttons on the right and the caption at the bottom.
