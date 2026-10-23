# Security policy

## Supported versions

sminer is pre-1.0. Security fixes go into the latest release only, so upgrade to the newest version on the [releases page](https://github.com/rossedman/sminer/releases) before reporting.

| Version | Supported |
| --- | --- |
| Latest 0.x release | Yes |
| Older releases | No |

## Reporting a vulnerability

Please report privately, not in a public issue:

- through GitHub's private vulnerability reporting: <https://github.com/rossedman/sminer/security/advisories/new>, or
- by email to ross.edman@gmail.com.

Include the sminer version (`sminer version`), your OS, what you found and how to reproduce it. sminer reads Claude Code transcripts, which can contain secrets, so never attach real transcripts; a minimal synthetic line is enough.

What to expect:

- an acknowledgement within 3 business days,
- a fix, or a plan with a timeline, within 30 days,
- credit in the published advisory, unless you'd rather stay anonymous.

Of particular interest: secrets surviving redaction (`internal/redact`) or reaching the Anthropic API, anything that sends data off the machine under `--offline`, and patches that write outside the patch directory.

## Verifying releases

Every release's `checksums.txt` is signed keylessly with [Sigstore cosign](https://docs.sigstore.dev/) by the release workflow, and every archive has a GitHub build provenance attestation. To verify a download, put it next to `checksums.txt` and `checksums.txt.sigstore.json` from the same release, then:

```bash
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/rossedman/sminer/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt
gh attestation verify sminer_*_linux_amd64.tar.gz --repo rossedman/sminer
```

The first command proves `checksums.txt` came from this repository's release workflow; the second proves your archive matches it; the third shows where and how the archive was built. On macOS, use `shasum -a 256 --ignore-missing -c checksums.txt`. Releases up to and including v0.2.3 are not signed.
