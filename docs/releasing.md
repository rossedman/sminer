# Releasing

Releases are built by [GoReleaser](https://goreleaser.com) from a version tag. Each one publishes:

- archives for macOS, Linux and Windows on amd64 and arm64, each with the binary, shell completions (bash, zsh, fish) and a man page,
- `checksums.txt`, signed keylessly with Sigstore cosign (`checksums.txt.sigstore.json`), plus a GitHub build provenance attestation for every archive,
- release notes grouped by Conventional Commit type (features, fixes, performance), and
- a Homebrew cask in [rossedman/homebrew-tap](https://github.com/rossedman/homebrew-tap), once the tap is set up (below).

## Cut a release

From an up-to-date `main` with green CI:

```bash
git tag v0.3.0
git push origin v0.3.0
```

The `release` workflow runs the race tests, then GoReleaser, which builds without the Go cache and signs `checksums.txt` with the workflow's GitHub identity; no signing keys or secrets are involved. Then it attests build provenance for the archives. [SECURITY.md](https://github.com/rossedman/sminer/blob/main/SECURITY.md#verifying-releases) has the commands to verify a download. The release appears on the [releases page](https://github.com/rossedman/sminer/releases) a few minutes later. Versions follow [semver](https://semver.org); while sminer is `0.x`, a minor bump may change behavior, and the notes say how.

To see what a release would contain without publishing anything:

```bash
make release-snapshot   # builds every archive into dist/, unsigned
```

## Set up the Homebrew tap

Until this is done, releases skip the cask and everything else still publishes.

1. Create a public repository named `homebrew-tap` under `rossedman`, with a README.
2. Create a [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new) with access to only that repository and **Contents: read and write**.
3. Add it to this repository as an Actions secret named `HOMEBREW_TAP_GITHUB_TOKEN` (Settings → Secrets and variables → Actions).

The next release commits `Casks/sminer.rb` to the tap, and `brew install rossedman/tap/sminer` works from then on. The cask installs the completions and man page too.

!!! note "macOS Gatekeeper"
    The binaries aren't notarized. The cask clears the quarantine flag after installing, so Homebrew users see no prompt. If you download an archive by hand, run `xattr -d com.apple.quarantine sminer` once, or allow it in System Settings → Privacy & Security.
