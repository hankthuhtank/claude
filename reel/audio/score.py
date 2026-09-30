"""Original score + sound design for "Open to Close" (20.000 s, 120 BPM, A minor).

Every hit is driven by cues.json, exported from the picture (render.cjs --cues),
so sound and image run on one clock. Output: audio/score.wav (48 kHz float32),
mastered to about -13 LUFS integrated, -1 dBTP.

    python3 audio/score.py cues.json audio/score.wav
"""
import json
import sys

import numpy as np
import pyloudnorm as pyln
from scipy import signal
from scipy.io import wavfile

SR = 48000
DUR = 20.0
N = int(SR * DUR)
PAD_N = N + SR * 3                      # room for tails, trimmed at the end
BEAT = 0.5
rng = np.random.default_rng(1848)       # CBOT, founded 1848

mix = np.zeros((2, PAD_N))              # dry bus
duck_bus = np.zeros((2, PAD_N))         # pad + sub: sidechained to the kick
verb = np.zeros((2, PAD_N))             # reverb send
delay = np.zeros((2, PAD_N))            # ping-pong send (arp)


# ── helpers ───────────────────────────────────────────────────────────────
def tt(dur):
    return np.arange(int(dur * SR)) / SR


def mtof(m):
    return 440.0 * 2 ** ((m - 69) / 12)


def db(x):
    return 10 ** (x / 20)


def put(sig, t, gain_db=0.0, pan=0.0, rev=0.0, bus=None, dly=0.0):
    """Mix a mono or stereo signal in at time t (s)."""
    bus = mix if bus is None else bus
    i = int(round(t * SR))
    if i >= PAD_N:
        return
    if sig.ndim == 1:
        a = (pan + 1) * np.pi / 4
        sig = np.stack([sig * np.cos(a), sig * np.sin(a)])
    n = min(sig.shape[1], PAD_N - i)
    g = db(gain_db)
    bus[:, i:i + n] += sig[:, :n] * g
    if rev:
        verb[:, i:i + n] += sig[:, :n] * g * rev
    if dly:
        delay[:, i:i + n] += sig[:, :n] * g * dly


def bp(x, lo, hi, order=2):
    return signal.sosfilt(signal.butter(order, [lo, hi], 'bandpass', fs=SR, output='sos'), x)


def hp(x, f, order=2):
    return signal.sosfilt(signal.butter(order, f, 'highpass', fs=SR, output='sos'), x)


def lp(x, f, order=2):
    return signal.sosfilt(signal.butter(order, f, 'lowpass', fs=SR, output='sos'), x)


def svf(x, fc, q=0.7, mode='bp'):
    """Chamberlin state-variable filter with per-sample cutoff (for sweeps)."""
    fc = np.broadcast_to(np.asarray(fc, dtype=float), x.shape)
    f = 2 * np.sin(np.pi * np.clip(fc, 20, SR / 6) / SR)
    d = 1 / q
    lo = ba = 0.0
    out = np.empty_like(x)
    for n in range(len(x)):
        lo += f[n] * ba
        hi = x[n] - lo - d * ba
        ba += f[n] * hi
        out[n] = ba if mode == 'bp' else lo if mode == 'lp' else hi
    return out


def env(n, a=0.002, d=0.1, curve=1.0):
    t = np.arange(n) / SR
    e = np.minimum(1, t / max(a, 1e-5)) * np.exp(-np.maximum(0, t - a) / d)
    return e ** curve


def addp(*xs):
    """Sum mono signals of different lengths (zero-padded)."""
    n = max(len(x) for x in xs)
    y = np.zeros(n)
    for x in xs:
        y[:len(x)] += x
    return y


def noise(dur):
    return rng.standard_normal(int(dur * SR))


# ── instruments ───────────────────────────────────────────────────────────
def kick(f0=150, f1=44, dur=0.5, dec=0.32, click=0.5):
    t = tt(dur)
    f = f1 + (f0 - f1) * np.exp(-t / 0.03)
    body = np.sin(2 * np.pi * np.cumsum(f) / SR) * np.exp(-t / dec)
    body = np.tanh(body * 1.6) / np.tanh(1.6)
    c = hp(noise(0.006), 1800) * env(int(0.006 * SR), 0.0003, 0.0015) * click
    body[:len(c)] += c
    return body


def clap():
    n = int(0.35 * SR)
    x = np.zeros(n)
    for k, off in enumerate([0, 0.009, 0.019, 0.03]):
        i = int(off * SR)
        b = noise(0.02) * env(int(0.02 * SR), 0.0005, 0.005 if k < 3 else 0.09)
        x[i:i + len(b)] += b
    tail = noise(0.35) * env(n, 0.03, 0.09)
    x += tail * 0.4
    return bp(x, 900, 3200) * 1.4


def hat(open_=False):
    d = 0.14 if open_ else 0.035
    x = noise(d * 3) * env(int(d * 3 * SR), 0.0005, d)
    return hp(x, 7000, 3) * 0.9


def tick(v=1.0, f=3300):
    t = tt(0.05)
    ping = np.sin(2 * np.pi * f * t) * np.exp(-t / 0.012)
    cl = hp(noise(0.05), 2500) * np.exp(-t / 0.002)
    return (ping * 0.5 + cl) * v


def keyclick(v=1.0):
    t = tt(0.07)
    thock = bp(noise(0.07), 700, 2200) * np.exp(-t / 0.016)
    cl = hp(noise(0.07), 3500) * np.exp(-t / 0.0015)
    return (thock * 0.9 + cl * 0.6) * v * (0.8 + 0.4 * rng.random())


def blip(f0, f1, dur=0.06, dec=0.03):
    t = tt(dur)
    f = np.linspace(f0, f1, len(t))
    return np.sin(2 * np.pi * np.cumsum(f) / SR) * np.exp(-t / dec) * np.minimum(1, t / 0.002)


def bell(f=880.0, dur=2.6, v=1.0):
    """Original inharmonic brass bell (hum, prime, minor-third tierce, quint, nominal…)."""
    t = tt(dur)
    parts = [(0.5, 0.40, 1.6), (1.0, 1.0, 1.1), (1.2, 0.55, 0.8), (1.5, 0.30, 0.6), (2.0, 0.55, 0.55),
             (2.52, 0.22, 0.38), (3.0, 0.16, 0.3), (4.07, 0.10, 0.22), (5.4, 0.06, 0.14)]
    x = np.zeros(len(t))
    for r, a, d in parts:
        det = 1 + 0.0009 * (rng.random() - 0.5)
        x += a * np.sin(2 * np.pi * f * r * det * t + rng.random() * 6.28) * np.exp(-t / d)
        x += 0.35 * a * np.sin(2 * np.pi * f * r * (det + 0.0016) * t) * np.exp(-t / d)  # beating pair
    strike = bp(noise(dur), 2500, 7000) * np.exp(-t / 0.004) * 0.8
    return (x / 3.2 + strike) * v


def pluck(f, dur=0.9):
    t = tt(dur)
    fm = np.sin(2 * np.pi * f * 2 * t) * 1.8 * np.exp(-t / 0.06)
    x = np.sin(2 * np.pi * f * t + fm) * np.exp(-t / 0.22)
    x += 0.35 * np.sin(2 * np.pi * f * 2 * t) * np.exp(-t / 0.08)
    return x * np.minimum(1, t / 0.003)


def boom(f0=95, f1=36, dur=1.6, dec=0.55):
    t = tt(dur)
    f = f1 + (f0 - f1) * np.exp(-t / 0.18)
    return np.tanh(1.4 * np.sin(2 * np.pi * np.cumsum(f) / SR) * np.exp(-t / dec))


def whoosh(dur, up=True, v=1.0, lo=250, hi=5000):
    n = int(dur * SR)
    u = np.linspace(0, 1, n)
    fc = lo * (hi / lo) ** (u if up else 1 - u)
    x = svf(noise(dur), fc, q=1.4)
    shape = np.sin(np.pi * u) ** 1.5
    return x * shape * v


def riser(dur, v=1.0):
    n = int(dur * SR)
    u = np.linspace(0, 1, n)
    x = svf(noise(dur), 300 * (9000 / 300) ** (u ** 1.6), q=2.0) * u ** 2.2
    f = 220 * (4 ** (u ** 2))
    tone = signal.sawtooth(2 * np.pi * np.cumsum(f) / SR) * 0.12 * u ** 3
    return (x + lp(tone, 3000)) * v


def impact(v=1.0, low=True):
    x = kick(170, 42, 0.7, 0.4, 0.9) * 1.0
    n = noise(0.5) * env(int(0.5 * SR), 0.001, 0.07)
    x[:len(n)] += lp(n, 5000) * 0.45
    if low:
        b = boom(110, 38, 1.2, 0.35) * 0.7
        m = max(len(x), len(b)); y = np.zeros(m); y[:len(x)] += x; y[:len(b)] += b; x = y
    return x * v


def crackle(dur, rate, v=1.0, bright=4000):
    n = int(dur * SR)
    x = np.zeros(n)
    k = int(dur * rate)
    for _ in range(k):
        i = rng.integers(0, max(1, n - 300))
        g = hp(noise(0.004), bright) * np.exp(-np.arange(int(0.004 * SR)) / SR / 0.0008) * (0.3 + rng.random())
        x[i:i + len(g)] += g
    return x * v


def saw_pad(freqs, dur, cutoff, att=0.25, rel=0.9):
    t = tt(dur + rel)
    x = np.zeros((2, len(t)))
    for f in freqs:
        for k, (cents, pan) in enumerate([(-9, -0.7), (0, 0.0), (8, 0.7)]):
            ff = f * 2 ** (cents / 1200)
            s = signal.sawtooth(2 * np.pi * ff * t + rng.random() * 6.28)
            a = (pan + 1) * np.pi / 4
            x[0] += s * np.cos(a); x[1] += s * np.sin(a)
    e = np.minimum(1, t / att) * np.where(t > dur, np.exp(-(t - dur) / (rel / 3)), 1.0)
    x *= e / (len(freqs) * 2.2)
    sos = signal.butter(2, cutoff, 'lowpass', fs=SR, output='sos')
    return signal.sosfilt(sos, x, axis=1)


# ── arrangement ───────────────────────────────────────────────────────────
CH = {'Am': [57, 60, 64, 71], 'F': [57, 60, 64, 67], 'C': [55, 60, 64, 67], 'G': [55, 59, 62, 64]}
ROOT = {'Am': 33, 'F': 29, 'C': 36, 'G': 31}
BARS = ['Am', 'Am', 'F', 'C', 'G', 'Am', 'F', 'C', 'G', 'Am']
CUT = [700, 2600, 2600, 2800, 900, 3000, 3200, 3400, 2400, 3600]
PADV = [-4, -3, -3, -3, -6, -3, -3, -3, -5, 0]


def groove():
    kicks = []
    for b in np.arange(2.0, 17.0, BEAT):
        if 8.0 <= b < 10.5 and (b - 8.0) % 1.0 != 0:
            continue                                  # half-time on paper
        kicks.append(b)
    for k in kicks:
        put(kick(), k, -9)
    # claps on 2 & 4 (not on paper)
    for b in np.arange(2.5, 17.0, 1.0):
        if 8.0 <= b < 10.5:
            continue
        put(clap(), b, -10, pan=0.05, rev=0.25)
    # hats: offbeat 8ths; 16ths through the vocabulary flipbook
    for b in np.arange(2.0, 17.0, 0.125):
        if 8.0 <= b < 10.5:
            continue
        off8 = abs((b % BEAT) - 0.25) < 1e-6
        in_vocab = 10.5 <= b < 12.5
        if off8:
            put(hat(open_=(int(b * 2) % 4 == 3)), b, -13, pan=0.25)
        elif in_vocab or (b % BEAT) in (0.125, 0.375):
            put(hat(), b, -22 if not in_vocab else -17, pan=0.3)
    # the market clock: quiet ticks through the intro
    for b in np.arange(0.0, 2.0, 0.25):
        put(tick(0.7, 5200), b, -19, pan=-0.2)
    return kicks


def harmony(kicks):
    for bi, ch in enumerate(BARS):
        t0 = bi * 2.0
        dur = 2.0 if bi < 9 else 2.4
        pad = saw_pad([mtof(m) for m in CH[ch]], dur, CUT[bi], att=0.4 if bi == 0 else 0.12, rel=1.2)
        if bi == 0:
            pad *= np.minimum(1, np.arange(pad.shape[1]) / (2.0 * SR)) ** 2   # swell in
        put(pad, t0, PADV[bi] - 3, bus=duck_bus, rev=0.35)
        # sub: sustained root, octave-doubled for small speakers
        if 1 <= bi <= 8:
            t = tt(2.0)
            f = mtof(ROOT[ch])
            s = np.sin(2 * np.pi * f * t) + 0.25 * np.sin(2 * np.pi * 2 * f * t)
            s *= np.minimum(1, t / 0.01) * np.minimum(1, (2.0 - t) / 0.02)
            if bi == 8:
                s *= np.where(t > 1.05, 0, 1) * np.minimum(1, (1.05 - t).clip(0) / 0.02 + (t > 1.05))
            put(np.tanh(s * 1.3), t0, -17, bus=duck_bus)
            # mid-bass so the line survives phone speakers: 8th-note pulse, lowpassed saw
            for q in range(8 if bi != 8 else 4):
                tq = tt(0.24); f2 = mtof(ROOT[ch] + 12)
                sb = signal.sawtooth(2 * np.pi * f2 * tq) * np.exp(-tq / 0.12) * np.minimum(1, tq / 0.004)
                put(lp(sb, 520), t0 + q * 0.25, -19 if q % 2 else -23, bus=duck_bus)
    # sidechain: duck pad+sub under every kick
    g = np.ones(PAD_N)
    tn = np.arange(PAD_N) / SR
    for k in kicks:
        i = int(k * SR); n = int(0.4 * SR)
        g[i:i + n] = np.minimum(g[i:i + n], 1 - 0.7 * np.exp(-np.arange(n) / SR / 0.11))
    duck_bus[:] *= g
    # the implosion sucks the harmony out, then the sign-off blooms
    suck = np.ones(PAD_N)
    a, b = int(17.05 * SR), int(17.45 * SR)
    suck[a:b] = np.linspace(1, 0.15, b - a) ** 2
    suck[b:int(18.0 * SR)] = 0.15
    duck_bus[:] *= suck
    del tn


def foley(cues):
    for c in cues:
        t, k = c['t'], c['type']
        if k == 'power':
            hum = sum(np.sin(2 * np.pi * 60 * h * tt(0.6)) / h for h in (1, 2, 3, 5))
            hum *= np.minimum(1, tt(0.6) / 0.15) * np.exp(-tt(0.6) / 0.25)
            put(addp(hum * 0.5, crackle(0.35, 120, 0.6)), t, -22, rev=0.1)
            put(tick(1.0, 2400), t, -14)
        elif k == 'tick':
            put(tick(c.get('v', 1.0), 3100 + 400 * rng.random()), t, -11, pan=(rng.random() - 0.5) * 0.4)
        elif k == 'bell':
            f = 880.0 if not c.get('close') else 659.25
            put(bell(f, 2.6, 0.9 if c['i'] < 3 else 1.1), t, -6 if c['i'] < 3 else -4, pan=0.15 * (c['i'] % 2 * 2 - 1), rev=0.45)
        elif k == 'drop':
            put(impact(1.0), t, -6, rev=0.3)
        elif k == 'burst':
            put(np.stack([crackle(0.6, 900, 1, 3500) * np.exp(-tt(0.6) / 0.25), crackle(0.6, 900, 1, 3500) * np.exp(-tt(0.6) / 0.25)]), t, -17, rev=0.3)
        elif k == 'scan':
            put(blip(700, 3600, 0.24, 0.2) * 0.6 + whoosh(0.24, True, 0.5, 1500, 7000), t, -20, pan=-0.3)
        elif k == 'key':
            put(keyclick(c.get('v', 0.4)), t, -13, pan=(rng.random() - 0.5) * 0.3)
        elif k == 'pop':
            put(blip(1100 + 150 * c['i'], 1350 + 150 * c['i'], 0.08, 0.04), t, -21, pan=-0.3 + 0.2 * c['i'])
        elif k == 'press':
            put(addp(keyclick(1.4), blip(1318, 1318, 0.05, 0.03)), t, -12)
            put(blip(1760, 1760, 0.08, 0.05), t + 0.06, -18, rev=0.2)
        elif k == 'hit':
            put(impact(0.7, low=False), t, -11, rev=0.25)
        elif k == 'whoosh':
            put(whoosh(c['d'], c.get('dir', 1) > 0, 1.0), t, -13, pan=0.0, rev=0.2)
        elif k == 'draw':
            x = bp(noise(0.32), 2500, 5000) * (0.5 + 0.5 * np.sin(2 * np.pi * 38 * tt(0.32))) * np.sin(np.pi * np.linspace(0, 1, int(0.32 * SR)))
            put(x, t, -24, pan=0.2)
        elif k == 'collapse':
            put(blip(900, 160, 0.3, 0.12) + 0.4 * whoosh(0.3, False, 1, 300, 3000), t, -17)
        elif k == 'zip':
            f = 600 * 1.26 ** c['i']
            put(blip(f, f * 1.9, 0.14, 0.08), t, -20, pan=-0.45 + 0.3 * c['i'])
        elif k == 'merge':
            put(kick(120, 50, 0.3, 0.12, 0.3) + np.pad(bp(noise(0.12), 1500, 6000) * np.exp(-tt(0.12) / 0.03), (0, 8640)), t, -11, rev=0.2)
        elif k == 'morph':
            x = blip(260, 420, 0.18, 0.1) * (1 + 0.3 * np.sin(2 * np.pi * 18 * tt(0.18)))
            put(x, t, -18, rev=0.15)
        elif k == 'riser2':
            put(riser(0.2, 1.0), t, -14)
        elif k == 'paper':
            x = lp(noise(0.4), 3500) * env(int(0.4 * SR), 0.004, 0.06)
            put(x, t, -9)
            put(crackle(0.3, 260, 0.8, 2000), t, -20, pan=0.2)
            put(kick(110, 55, 0.25, 0.08, 0.2), t, -10)
        elif k == 'print':
            x = np.sin(2 * np.pi * 140 * tt(0.08)) * np.exp(-tt(0.08) / 0.02)
            x += bp(noise(0.08), 600, 3000) * np.exp(-tt(0.08) / 0.012) * 0.8
            put(x, t, -10, pan=(rng.random() - 0.5) * 0.2)
        elif k == 'stamp':
            put(impact(0.9, low=True), t, -8, rev=0.18)
            put(lp(noise(0.12), 2500) * np.exp(-tt(0.12) / 0.02), t, -8)
        elif k == 'spin':
            x = np.zeros(int(0.32 * SR))
            tt_ = 0.0
            while tt_ < 0.3:
                i = int(tt_ * SR); cl = tick(0.6, 2800)[:600]; x[i:i + len(cl)] += cl[:len(x) - i]
                tt_ += 0.012 + 0.08 * (tt_ / 0.3) ** 2
            put(x, t, -16)
        elif k == 'land':
            put(tick(1.0, 1900) + np.pad(kick(140, 90, 0.05, 0.02, 0.2), (0, 0))[:2400], t, -14)
        elif k == 'flap':
            n = c.get('n', 8)
            for j in range(n):
                cl = bp(noise(0.02), 1800, 6000) * np.exp(-tt(0.02) / 0.003) * (0.5 + 0.6 * rng.random())
                put(cl, t + j * 0.008 + 0.045, -17, pan=-0.4 + 0.8 * j / n)
        elif k == 'term':
            i = c['i']
            put(keyclick(1.0), t, -15, pan=(i % 2 - 0.5) * 0.3)
            put(blip(mtof(69 + [0, 3, 5, 7, 10, 12, 15, 17, 19, 22][i]), mtof(69 + [0, 3, 5, 7, 10, 12, 15, 17, 19, 22][i]), 0.08, 0.035), t, -25, dly=0.2)
        elif k == 'drop2':
            put(impact(0.85), t, -8, rev=0.3)
            put(crackle(0.25, 700, 1, 3000), t, -20)
        elif k == 'note':
            m = [69, 72, 74, 76, 79, 81, 84, 88][c['k']]
            put(pluck(mtof(m)), t, -9, pan=-0.5 + c['k'] / 7, rev=0.25, dly=0.35)
        elif k == 'drop3':
            put(kick(160, 80, 0.2, 0.06, 0.4), t + 0.1, -14)
            put(bell(1760, 1.2, 0.5), t + 0.1, -22, rev=0.3)
        elif k == 'slam':
            v = [0.8, 0.85, 0.95, 1.0, 0.9][c['i']]
            put(impact(v), t, -7, rev=0.28)
        elif k == 'slash':
            x = whoosh(0.12, False, 1, 1500, 9000) + crackle(0.12, 500, 0.5, 4000)
            put(x, t, -12, pan=0.3)
        elif k == 'implode':
            d = 0.4
            x = whoosh(d, True, 1, 200, 8000) * np.linspace(0, 1, int(d * SR)) ** 2
            x[-int(0.004 * SR):] *= np.linspace(1, 0, int(0.004 * SR))
            put(x, t, -10)
            put(riser(0.45, 0.8), t + 0.05, -16)
        elif k == 'final':
            put(impact(1.0), t, -5, rev=0.45)
            put(boom(80, 30, 2.5, 0.9), t, -12)
            sh = hp(noise(2.5), 6000) * env(int(2.5 * SR), 0.01, 0.6)
            put(np.stack([sh, hp(noise(2.5), 6000) * env(int(2.5 * SR), 0.01, 0.6)]), t, -22, rev=0.5)
            put(bell(440, 3.0, 0.9), t, -13, rev=0.5)
        elif k == 'sheen':
            x = sum(np.sin(2 * np.pi * np.cumsum(np.linspace(f, f * 1.5, int(0.6 * SR))) / SR) for f in (2637, 3520, 4186))
            put(x * np.sin(np.pi * np.linspace(0, 1, int(0.6 * SR))) ** 2 * 0.3, t, -28, rev=0.4, pan=0.2)
    # the intro riser into the opening bell
    put(riser(2.0, 1.0), 0.0, -13)


def reverb(x, secs=2.2):
    n = int(secs * SR)
    tail = np.exp(-np.arange(n) / SR / (secs / 6.5))
    ir = np.stack([lp(rng.standard_normal(n), 6000) * tail, lp(rng.standard_normal(n), 6000) * tail])
    ir[:, :int(0.018 * SR)] = 0
    ir /= np.sqrt((ir ** 2).sum(axis=1, keepdims=True))
    return np.stack([signal.fftconvolve(x[i], ir[i])[:PAD_N] for i in range(2)])


def pingpong(x, d=0.375, fb=0.38, n=4):
    y = np.zeros_like(x)
    k = int(d * SR)
    for r in range(1, n + 1):
        ch = r % 2
        y[ch, k * r:] += x[0, :PAD_N - k * r] * fb ** r + x[1, :PAD_N - k * r] * fb ** r
    return lp(y, 5000)


def limiter(x, ceil_db=-1.0, look=0.004, rel=0.08):
    c = db(ceil_db)
    up = signal.resample_poly(x, 4, 1, axis=1)
    pk = np.abs(up).max(axis=0).reshape(-1, 4).max(axis=1)[:x.shape[1]]
    g = np.minimum(1, c / np.maximum(pk, 1e-9))
    from scipy.ndimage import minimum_filter1d
    la = int(look * SR)
    g = minimum_filter1d(g, size=2 * la + 1)
    out = np.empty_like(g)
    a = np.exp(-1 / (rel * SR))
    cur = 1.0
    for i in range(len(g)):
        cur = g[i] if g[i] < cur else a * cur + (1 - a) * g[i]
        out[i] = cur
    out = np.convolve(out, np.ones(la) / la, mode='same')   # smooth; stays <= required gain
    return x * out


def main():
    cues = json.load(open(sys.argv[1] if len(sys.argv) > 1 else 'cues.json'))
    outp = sys.argv[2] if len(sys.argv) > 2 else 'audio/score.wav'
    kicks = groove()
    harmony(kicks)
    foley(cues)
    wet = reverb(verb) * 0.9 + pingpong(delay)
    x = mix + duck_bus + wet
    x = hp(x, 28)
    x = x[:, :N]
    fade = int(0.35 * SR)
    x[:, -fade:] *= np.linspace(1, 0, fade) ** 1.5
    # glue: gentle saturation, then loudness + true-peak limiting
    x = np.tanh(x * 1.1) / 1.1
    meter = pyln.Meter(SR)
    for _ in range(3):
        lufs = meter.integrated_loudness(x.T)
        x = x * db(-13.0 - lufs)
        x = limiter(x, -1.2)
    lufs = meter.integrated_loudness(x.T)
    tp = 20 * np.log10(np.abs(signal.resample_poly(x, 4, 1, axis=1)).max())
    print(f'integrated {lufs:.2f} LUFS, true peak {tp:.2f} dBTP')
    wavfile.write(outp, SR, x.T.astype(np.float32))


if __name__ == '__main__':
    main()
