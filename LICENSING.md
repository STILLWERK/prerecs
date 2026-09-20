# Licensing notes

The Go source, build scripts, tests, and project documentation in this repository are original PreRecs release material and are covered by the MIT License in [LICENSE](LICENSE).

The project has no vendored Go dependencies; `go.mod` uses only the standard library. FFmpeg, Xvid, MagicYUV, Ut Video, Windows VfW components, and other runtime tools are external software. They are not included here and remain under their own licenses.

The source archive also contained a `legacy/` directory with an older batch implementation and an original README that point to `https://github.com/gmzorz/prerecs`. The referenced upstream repository does not publish a license for those files. Because their redistribution and relicensing status is ambiguous, they are intentionally excluded from this repository rather than being placed under MIT by assumption.
