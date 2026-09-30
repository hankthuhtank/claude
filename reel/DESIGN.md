# THE TRADING DESK: "Open to Close"

A 20-second motion reel for **thetradingdesk.org**, *The All-In-One Trading Terminal*.

## Concept

The film compresses one trading session into 20 seconds. It opens on the pre-market countdown and the opening bell. The session then runs through the desk's modules (patterns, options, risk, vocabulary, the path). It ends on the closing bell and a sign-off. The market clock in the frame runs **09:29:57 → 16:00:00**.

The site calls itself a *terminal*, so the film is a terminal session. It uses amber phosphor on warm black, and it breaks once to ticker-tape paper for the one idea that must never change: *size from the stop*.

### Movement: "Tape Reading"

Reading the tape was the old craft of learning a market by watching its stream of prints until the stream turned into language. The reel works the same way: a stream of ticks, dots and rules that resolves into words, and then into understanding. Every element is something that already exists on a trading desk:

- LED dot boards
- candles
- payoff diagrams
- split-flap boards
- order tickets
- the chart crosshair
- the market clock

Nothing is decorative. Every mark has to earn its place and every cut lands on the beat. The standard is meticulous, frame-by-frame craft, with each easing curve hand-tuned. Nothing should look like a template.

## Material palette

These colours come from the terminal and the tape, not from a trend.

| Token | Hex | Source |
| --- | --- | --- |
| `INK` | `#0B0A08` | a powered CRT, warm and never true black |
| `AMBER` | `#FF9F1C` | amber phosphor, the colour everything "prints" in |
| `HOT` | `#FFD9A0` | the white-hot core of a lit LED or phosphor bloom |
| `DIM` | amber @ 10–25% | grids, unlit LEDs, rules |
| `PAPER` | `#ECE4D2` | ticker tape and order-ticket stock |
| `PRINT` | `#16130F` | ink on paper |
| `STOP` | `#FF3B2F` | the only alarm colour: stops, traps, strike-throughs |

Candles stay monochrome amber, as on a phosphor terminal: **lit (filled) = up, outline = down**. That keeps red free to mean one thing only, *danger*.

## Type

- **Big Shoulders** (Patric King, the City of Chicago's typeface): the display face. It is set in caps, with 800–900 weights for impact and 200 for contrast. Chicago is where futures (CBOT, 1848) and listed options (CBOE, 1973) began, so the display voice of an options-and-futures education comes from the city of the pits. The optical-size axis is pinned to 72.
- **Martian Mono** (Evil Martians): the terminal voice, used for captions, labels and chrome. It is cut into condensed (wdth 75) and normal (wdth 100) instances.
- **Doto** (round-dot instance): the LED board, used for the market clock and counters.

Canvas can't reach custom axes, so `tools/make_fonts.py` pins opsz, ROND and wdth and keeps weight variable.

## Motion rules: *Ticks & Trends*

1. **Ticks** are discrete and never eased: digits, LEDs, typed characters, split flaps, counters and the cursor. They land on 16th notes.
2. **Trends** are continuous: camera moves, big type and lines. They use expo or quint ease-outs with long tails, and springs with a little overshoot on landing. Nothing moves linearly except the tape.
3. **Every cut is on the grid** of the 120 BPM score. Big hits fall on downbeats.
4. **Match cuts carry the story**:
   - LED dots → letters
   - the letter *I* → a candle
   - the price line → the payoff line
   - the vega bell → a paper flood
   - an ink stroke of *100* → the dark of the next scene
   - strike-throughs → the logo rule
5. **Camera blur**: 180° shutter, with 8 sub-frames accumulated per output frame.
6. **Signature device: the chart crosshair.** A hairline cross with axis read-outs locks onto each subject before its scene fires, with a `<GO>` flash in the chrome. It is the viewer's cursor through the film.

## Beat sheet (120 BPM · bar = 2 s · 1200 frames @ 60 fps)

| Time | Scene | What happens | Site copy used |
| --- | --- | --- | --- |
| 0.00–2.00 | **Pre-market** | Giant LED clock 09:29:57 → :58 → :59 → **09:30:00**; slow push-in; bell roll on the last beat | |
| 2.00–4.00 | **One terminal** | LEDs burst into particles and re-form as the title; the title fuses solid; subline types; four function keys pop; **[PATTERNS]** is pressed | "One terminal for the whole trader's education: patterns, options, risk, and the path in between." |
| 4.00–6.00 | **Patterns** | The *I* becomes a single candle with O/H/L/C read-outs; exponential zoom-out reveals a head-and-shoulders; neckline and labels draw | "from single candles to chart structures" |
| 6.00–8.00 | **Options** | Price line → payoff axis; four legs fly in and **sum** into an iron condor; the payoff morphs through Δ Γ Θ ν curves | "multi-leg payoff, the greeks as curves" |
| 8.00–10.50 | **Risk** (paper) | The vega bell floods to paper. SIZE FROM THE STOP. `$200 ÷ $2 = 100 SHARES`; a split-flap instrument cycles while the arithmetic stays still | "Size from the stop… The instrument changes, the arithmetic does not." |
| 10.50–12.50 | **Vocabulary** | 16th-note type flipbook (0DTE, IV CRUSH, VWAP…, *PDT RULE* struck and retired 06.04.26); LED counter → **61 TERMS** | "sixty-one terms… the trap most people miss" |
| 12.50–14.50 | **The path** | A stepped route draws eight stations, each one a note of a rising arpeggio; a bookmark drops | "Eight steps, in order. Your place is kept." |
| 14.50–16.50 | **No login** | A sign-up card is struck through on three beats | "No login, no email, nothing fake." |
| 16.50–18.00 | **Free. Always.** | Full-bleed type; everything implodes to one cursor; closing bell at 16:00:00 | "free, and always will be" |
| 18.00–20.00 | **Sign-off** | Lockup: THE TRADING DESK · the all-in-one trading terminal · `> thetradingdesk.org▮` | |

## Sound

The score is synthesised from scratch (`audio/score.py`), with every hit driven by the same cue list as the picture (`cues.json`).

- **Groove**: A minor, i–VI–III–VII, four-on-the-floor. There is a sidechained sub, a warm detuned pad, and offbeat hats that go to 16ths in the vocabulary flipbook.
- **Bells**: the opening and closing bells are an original inharmonic brass bell, tuned to the key. The NYSE bell is a registered sound mark, so it is evoked, not copied.
- **Foley**: LED ticks, typed keys, split-flap clatter, whooshes on whips and a sub drop on the sign-off.
- **Sonification**: the eight path stations play a rising A-minor-pentatonic line.
- **Master**: −13 LUFS integrated, −1 dBTP. That sits between YouTube (−14) and Reels/TikTok (−10 to −13).

## Research notes

- **Site content** comes from thetradingdesk.org, gathered through search-engine snippets because the page was not reachable from the build machine:
  - hero line, calculators list, glossary ("sixty-one terms", "the trap most people miss")
  - pattern reference, eight-step path, "No login, no email, nothing fake."
  - "Size from the stop", "free and always will be", optional 1:1 sessions
  - FINRA Rule 4210 PDT removal (effective 4 June 2026)
- **Showreel practice**: lead with the strongest shot, close on the second-strongest, cut to music, and waste no frames (School of Motion, Creative Boom).
- **Legibility**: no more than 3 words per second for comprehension, and all-caps is recognised faster on screen (JGED legibility studies; SBCC type-in-motion notes). Copy is budgeted per scene against this.
- **2026 motion trends**: crafted, tactile texture (grain, paper fibre) as a reaction to generic AI output, oversized neo-brutalist type, and retro-terminal UI (Videobolt, Envato, Creative Bloq, Setproduct).
- **Loudness**: YouTube −14 LUFS; Instagram and TikTok −10 to −13; true peak ≤ −1 dBTP.
- **NYSE bell**: a brass bell in D with a D# overtone, struck 9 times (the registered sound mark). That is the reason to evoke rather than copy it.
- **Big Shoulders**: the City of Chicago's typeface, rooted in the city's railway, protest and dance histories (Chicago Design System).
- **CBOE**: founded 1973 as the first exchange to list standardised options. **CBOT**: 1848, the first US grain futures exchange.
