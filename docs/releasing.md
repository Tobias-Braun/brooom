# Releasing Brooom

Releases are built by [GoReleaser](https://goreleaser.com) from
`.goreleaser.yaml` and published by `.github/workflows/release.yml`. The
GitHub release and the Homebrew cask are published today; every other package
manager integration is prepared but has `skip_upload: true`.

## Cutting a release

1. Make sure `main` is green and contains everything for the release.
2. Tag `main` with a semantic version and push the tag:

   ```sh
   git switch main && git pull --ff-only
   git tag -a v1.0.0 -m "v1.0.0"
   git push origin v1.0.0
   ```

3. The `Release` workflow runs on the tag (`v*`). It builds linux, darwin and
   windows binaries for amd64 and arm64 with `CGO_ENABLED=0`, stamps
   `buildinfo.Version`, `Commit` and `Date`, creates the archives,
   `checksums.txt` (sha256), the deb, rpm and apk packages, and publishes a
   GitHub release with the generated changelog. Tags with a pre-release suffix
   (for example `v0.2.0-rc.1`) are marked as pre-releases. For stable tags it
   then commits `Casks/brooom.rb` to `Tobias-Braun/homebrew-tap`.
4. Check the release page and try the install script:
   `curl -fsSL https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.sh | sh`.
5. Check that the tap received the cask commit, then run
   `brew update && brew upgrade --cask brooom` (or
   `brew install --cask Tobias-Braun/tap/brooom`) and `brooom version`.

Archive names are `brooom_<version>_<os>_<arch>.tar.gz` (`.zip` on windows),
without the leading `v`. `scripts/install.sh`, `scripts/install.ps1` and the
`archives.name_template` in `.goreleaser.yaml` depend on each other and must
change together.

## Changelog

The changelog is generated from commit subjects (`changelog.use: github`, so
entries link to the author and PR). Conventional prefixes decide the group,
with an optional scope and a `!` for breaking changes. Squash merge titles such
as `feat(cli): add sweep (#12)` are matched too, because the trailing PR
number is part of the subject.

| Prefix | Group |
| --- | --- |
| `type!:` (feat, fix, perf, refactor) | Breaking changes |
| `feat` | Features |
| `fix` | Bug fixes |
| `perf` | Performance |
| `refactor` | Refactoring |
| `docs` | Documentation |
| anything else | Other changes (last) |
| `chore`, `ci`, `test`, `build`, `style`, merge commits | excluded |

Write PR titles in this form, since squash merges turn them into commit
subjects.

## Testing locally

```sh
go run github.com/goreleaser/goreleaser/v2@latest check
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish
./dist/brooom_linux_amd64_v1/brooom version   # pick the folder of your platform
```

The snapshot writes archives, `checksums.txt`, packages and the rendered
Homebrew, winget and AUR manifests to `dist/` (git-ignored) and uploads
nothing. `scripts/test-install.sh` tests the install script against a local
fake release. Pull requests touching the release files run the same checks in
`.github/workflows/release-check.yml`.

The `Release` workflow first verifies that the tagged commit is reachable from
`origin/main` and fails before building anything otherwise. Pull requests that
touch the release files (or `internal/trash/**`) additionally smoke-test the
snapshot: the darwin arm64 and Intel binaries run `scripts/smoke-darwin.sh`
(version, a dry run and a sweep into the macOS Trash), and `scripts/test-install.ps1`
installs the Windows snapshot under Windows PowerShell and PowerShell 7. The
CI `Test purego` jobs run the trash tests with `CGO_ENABLED=0` on both macOS
architectures, as the shipped binary is built.

`scripts/install.ps1` resolves the latest tag from the GitHub releases API;
its `BROOOM_LATEST_URL` override must answer with that JSON (only `tag_name`
is read), unlike `install.sh` where it is a redirecting URL.

## Homebrew cask and Gatekeeper

The darwin binaries are ad-hoc signed but not notarized, and Homebrew
downloads casks with `com.apple.quarantine`, so Gatekeeper blocks the first
run. The cask therefore has a `hooks.post.install` that removes the attribute
from the staged binary. Notarization (`notarize.macos`, needs Apple developer
credentials) makes the hook unnecessary; drop it then.

The cask installs `brooom` only. `install.sh` adds the short command `br` only
when the name is free, which a cask cannot check, so the cask's `caveats`
explain how to add the symlink instead.

## Enabling package managers

Homebrew is enabled (`skip_upload: auto`); every other integration is
configured but disabled. To enable one, create the repository and secret listed
below, then change its `skip_upload: true` to `auto` (which skips
pre-releases) or `false` in `.goreleaser.yaml`. The workflow already passes the
secrets as environment variables; an unset secret is harmless while uploads
are skipped, but a stable release fails at the Homebrew step without
`HOMEBREW_TAP_GITHUB_TOKEN`.

| Channel | Repository | Secret (repository secret in Tobias-Braun/brooom) | Token / key scope |
| --- | --- | --- | --- |
| Homebrew (enabled) | `Tobias-Braun/homebrew-tap` (public, exists) | `HOMEBREW_TAP_GITHUB_TOKEN` | fine-grained PAT, contents: write on the tap |
| winget | fork of `microsoft/winget-pkgs` as `Tobias-Braun/winget-pkgs` | `WINGET_GITHUB_TOKEN` | PAT that can push to the fork and open pull requests against `microsoft/winget-pkgs` |
| AUR | package `brooom-bin` registered on aur.archlinux.org | `AUR_SSH_PRIVATE_KEY` | private key whose public half is on the AUR account |

The deb, rpm and apk packages are already attached to the GitHub release; they
are not pushed to a package repository.

winget and AUR stay `skip_upload: true` for now: they need separate accounts and manual review (the winget PR is
reviewed by Microsoft, the first AUR push needs the package registered).

## Site releases

The landing page (`site/`) is released independently of the CLI by
`.github/workflows/site.yml`. It is not hosted on GitHub Pages: the
maintainer's infrastructure runs the site image and deploys new versions with
FluxCD.

- Every push to `main` that touches `site/**` (or the workflow) runs the checks
  and then releases the next version: the patch part of the highest `site-v*`
  tag is increased, and the first release is `1.0.0`. A manual run
  (`workflow_dispatch`) can increase the minor or major part instead.
- The image is `ghcr.io/tobias-braun/brooom-site`, built for `linux/amd64` from
  `site/Dockerfile`: an unprivileged nginx serving the static build on port
  8080, with `/healthz` for probes. Each release pushes the tags `X.Y.Z`,
  `sha-<commit>` and `latest`, and then creates the git tag `site-vX.Y.Z`.
- Site releases create no GitHub Release, so `/releases/latest` (read by the
  install scripts) always points at the CLI, and the
  `site-v*` tags never trigger the CLI release workflow (`v*`).
- The public origin is read at container start from the env var `SITE_URL`
  (e.g. `https://brooom.dev`), so the same image serves any domain. Without it,
  canonical and Open Graph URLs point to `http://localhost:8080`. The container
  only writes to `/tmp`, so it runs with a read-only root filesystem.
- Analytics are opt-in the same way: when both `UMAMI_SCRIPT_URL` (e.g.
  `https://analytics.tobi-braun.com/script.js`) and `UMAMI_WEBSITE_ID` are set,
  every page loads the Umami tracker; otherwise the image ships no tracker.

A FluxCD image policy that follows the releases and ignores `latest` and the
`sha-*` tags:

```yaml
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImageRepository
metadata:
  name: brooom-site
spec:
  image: ghcr.io/tobias-braun/brooom-site
  interval: 5m
---
apiVersion: image.toolkit.fluxcd.io/v1beta2
kind: ImagePolicy
metadata:
  name: brooom-site
spec:
  imageRepositoryRef:
    name: brooom-site
  filterTags:
    pattern: '^\d+\.\d+\.\d+$'
  policy:
    semver:
      range: '>=1.0.0'
```

One-time setup: after the first release, make the `brooom-site` package public
(package settings on GitHub) or give Flux an image pull secret, set `SITE_URL`
(and optionally the Umami variables) in the deployment's container env, and disable GitHub Pages in the repository settings.

## Identity

All commits for this project use `Tobias Braun <mail@tobi-braun.com>`: the
maintainer's own commits, the commits and pull request of the release pipeline
issue, and every commit the release pipeline creates in other repositories
(Homebrew tap, winget fork, AUR). Each publisher sets it as
`commit_author`, and the `commit_msg_template` values add no trailers. No bot
or AI identity and no `Co-Authored-By` trailer is used.

## Future work

- Signing of checksums and artifacts (cosign or GPG).
- SBOMs attached to releases.
- Publishing the deb, rpm and apk packages to a package repository.
