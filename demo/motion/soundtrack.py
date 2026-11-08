#!/usr/bin/env python3
"""Original soundtrack for the Tiimi motion graphic, synthesised from scratch
(no samples, nothing licensed). Timed to demo/motion/index.html:

  0-3.1 s   hook            soft A-minor pad
  3.3-8.9   bars            F -> C pad, 120 bpm pulse, a tick as each bar starts
  9.3-11.6  punchline       G pad, brighter
  12-14.5   end card        resolve on C, bell on the logo
  whooshes at the scene changes (3.1, 9.1, 11.8 s)

  soundtrack.py <out.wav>
"""
import math, random, struct, sys, wave

SR, DUR = 44100, 14.5
N = int(SR * DUR)
random.seed(3)

def note(name):
    names = {"C": -9, "D": -7, "E": -5, "F": -4, "G": -2, "A": 0, "B": 2}
    return 440.0 * 2 ** ((names[name[0]] + 12 * (int(name[-1]) - 4)) / 12)

CHORDS = [  # (start, end, notes)
    (0.0, 3.2, ["A2", "C3", "E3", "A3"]),
    (3.2, 6.1, ["F2", "A2", "C3", "F3"]),
    (6.1, 9.2, ["C3", "E3", "G3", "C4"]),
    (9.2, 11.9, ["G2", "B2", "D3", "G3"]),
    (11.9, 14.5, ["C3", "E3", "G3", "C4", "E4"]),
]
TICKS = [4.0 + i * 0.45 for i in range(5)]          # bars start
WHOOSH = [3.05, 9.1, 11.8]
BEAT_FROM, BEAT_TO, BPM = 3.3, 9.0, 120

def env(t, a, b, att=0.35, rel=0.6):
    if t < a or t > b + rel:
        return 0.0
    up = min(1.0, (t - a) / att)
    down = 1.0 if t <= b else max(0.0, 1 - (t - b) / rel)
    return up * down

lp = 0.0  # one-pole low-pass state for the whoosh noise
out = []
for i in range(N):
    t = i / SR
    s = 0.0
    # pad: detuned sines with slow tremolo
    for a, b, notes in CHORDS:
        e = env(t, a, b)
        if e:
            bright = 0.35 if a >= 9.2 else 0.22
            for k, nm in enumerate(notes):
                f = note(nm)
                s += e * 0.055 * (math.sin(2 * math.pi * f * t) + 0.5 * math.sin(2 * math.pi * f * 1.003 * t + k))
                s += e * 0.02 * bright * math.sin(2 * math.pi * 2 * f * t)
            s *= 1 + 0.06 * math.sin(2 * math.pi * 0.5 * t)
    # pulse: soft kick on each beat while the bars run
    if BEAT_FROM <= t <= BEAT_TO:
        bt = (t - BEAT_FROM) % (60 / BPM)
        s += 0.32 * math.sin(2 * math.pi * (50 + 60 * math.exp(-bt * 30)) * bt) * math.exp(-bt * 9)
    # ticks: short bright blip as each bar starts
    for tk in TICKS:
        dt = t - tk
        if 0 <= dt < 0.25:
            s += 0.12 * math.sin(2 * math.pi * 1760 * dt) * math.exp(-dt * 28)
    # whoosh: filtered noise swell into each scene change
    w = 0.0
    for c in WHOOSH:
        dt = t - c
        if -0.45 < dt < 0.35:
            w += math.exp(-((dt + 0.05) / 0.18) ** 2)
    lp += (random.uniform(-1, 1) - lp) * 0.08
    s += 0.18 * w * lp
    # bell on the logo
    dt = t - 12.1
    if 0 <= dt < 2.4:
        s += 0.1 * (math.sin(2 * math.pi * note("C5") * dt) + 0.4 * math.sin(2 * math.pi * note("G5") * dt)) * math.exp(-dt * 2.2)
    # master fade in / out
    s *= min(1.0, t / 0.4) * min(1.0, (DUR - t) / 1.2)
    out.append(s)

peak = max(abs(x) for x in out) or 1
gain = 0.8 / peak
with wave.open(sys.argv[1], "wb") as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes(b"".join(struct.pack("<h", int(x * gain * 32767)) for x in out))
print("wrote", sys.argv[1])
