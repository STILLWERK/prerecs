# Contributing to PreRecs

PreRecs is a codec-tuned utility. Changes to encoder arguments, timing filters, output wrapping, frame counting, or verification need focused regression evidence and must preserve the documented SHARE/Efficient distinction.

Before opening a change:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o PreRecs.exe .
```

Do not commit generated executables, media, archives, local FFmpeg/Xvid/MagicYUV installations, credentials, or machine-specific paths. The release workflow builds Windows artifacts from tags beginning with `v`.
