# Performance baseline

This document records the starting point for the performance and maintainability
cleanup items in `TODO.md`. The benchmark is intentionally kept in the test
suite so later changes can be compared with the same workload.

## Public-state snapshot and diff

Command:

```sh
go test -run '^$' -bench '^BenchmarkPublicStateSnapshotAndDiff$' -benchmem ./internal/manager
```

Recorded on 2026-09-24, before item 1 changes, on an AMD Ryzen 7 8845HS,
linux/amd64:

| Resources | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|
| 16 | 55,140 | 81,344 | 380 |
| 128 | 420,567 | 663,169 | 2,638 |
| 512 | 2,031,307 | 2,564,965 | 10,336 |

These numbers measure the current full public-state capture and comparison path,
not a complete reconciliation or a real device workload. They are comparison
baselines rather than release performance requirements.

After item 1 (reuse the previous published state and avoid the duplicate
capture), the same benchmark produced:

| Resources | ns/op | B/op | allocs/op |
|---:|---:|---:|---:|
| 16 | 37,172 | 48,288 | 255 |
| 128 | 319,906 | 391,211 | 1,832 |
| 512 | 1,729,204 | 2,199,512 | 7,220 |

Compared with the baseline, this reduced allocations by approximately 33–41%
and elapsed time by approximately 15–33% in this run. Benchmark results are
hardware- and workload-dependent; rerun the command for a fresh comparison.

## Retained event append

Command:

```sh
go test -run '^$' -bench '^BenchmarkRetainedEventAppend$' -benchmem ./internal/manager
```

Before item 2, appending after the 1,024-event retention limit measured
71,070 ns/op, 286,711 B/op, and 2 allocs/op. With the ring buffer it measured
70.41 ns/op, 0 B/op, and 0 allocs/op on the same host and run conditions.
