#!/bin/sh
set -eu
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "Run gofmt on:" >&2
  echo "$unformatted" >&2
  exit 1
fi
go vet ./...
go test -race -timeout 300s ./...
go build -o bin/ ./cmd/...
python3 scripts/artifact-smoke.py "bin/emberdeck$(go env GOEXE)" --expected-version dev
python3 scripts/check-notices.py
python3 scripts/build-docs.py
