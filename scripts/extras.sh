#!/bin/sh
# Generate the shell completions and man page that release archives ship.
# Run by goreleaser before it builds (see .goreleaser.yaml).
set -eu
rm -rf completions manpages
mkdir -p completions manpages
for sh in bash zsh fish; do
  go run ./cmd/sminer completion "$sh" > "completions/sminer.$sh"
done
go run ./cmd/sminer man | gzip -c -9 > manpages/sminer.1.gz
