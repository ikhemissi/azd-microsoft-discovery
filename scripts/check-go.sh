#!/usr/bin/env bash
set -euo pipefail

mapfile -d '' -t go_files < <(git ls-files -z -- '*.go')
test -z "$(gofmt -l "${go_files[@]}")"
go build ./...
go vet ./...
mkdir -p coverage
go test -race -count=1 -coverprofile=coverage/offline.out ./...
go tool cover -func=coverage/offline.out | tee coverage/summary.txt
go fix -diff ./... > coverage/go-fix.diff
test ! -s coverage/go-fix.diff
