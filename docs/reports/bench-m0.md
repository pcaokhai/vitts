# Bench M0 — worker RTF and TTFA

Machine: Apple M4 Pro, 12 cores, Darwin 27.0.0
Corpus: 3 Vietnamese texts, 4 measured runs each (2 warm-up runs discarded), voice `maichi`, native 48 kHz.

| configuration | text | RTF mean | RTF max | TTFA mean | TTFA max |
|---|---|---|---|---|---|
| 1 process x 8 threads | short | 0.308x | 0.321x | 44.6 ms | 52.8 ms |
| 1 process x 8 threads | medium | 0.29x | 0.292x | 47.5 ms | 53.2 ms |
| 1 process x 8 threads | long | 0.306x | 0.312x | 54.9 ms | 55.8 ms |
| 2 processes x 4 threads | short | 0.349x | 0.367x | 49.9 ms | 67.8 ms |
| 2 processes x 4 threads | medium | 0.331x | 0.356x | 49.8 ms | 65.5 ms |
| 2 processes x 4 threads | long | 0.343x | 0.372x | 61.2 ms | 68.2 ms |

## PRD assumptions

- **A1** (RTF ≈ 0.5 with 8 threads): worst RTF 0.321x — holds on this machine.
- **A2** (two 4-thread processes still beat real time): worst RTF 0.372x — holds.
- **US-01 AC-2** (first frame ≤ 150 ms): worst TTFA 55.8 ms — holds.

Measured on the machine named above. A1 is stated for Hetzner CCX / AWS c7i;
re-run `make bench` on the target host before treating it as settled there.
