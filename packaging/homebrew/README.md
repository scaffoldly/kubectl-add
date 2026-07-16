# Homebrew tap

The tap channel installs the prebuilt release binary (the normal auto-updating
build). Users:

```sh
brew tap scaffoldly/tap
brew install kubectl-add
```

## How it works

- [`formula.rb.tmpl`](./formula.rb.tmpl) is the formula with `@@…@@`
  placeholders for the version and per-arch sha256s.
- [`render.sh`](./render.sh) fills the template for a release tag by fetching
  that release's archives and hashing them: `render.sh v0.2.0 > kubectl-add.rb`.
- The tap **updates itself**: `scaffoldly/homebrew-tap` runs a scheduled
  workflow (`update-formula.yml`) that reads this repo's latest public release,
  re-renders the formula with copies of `render.sh` + `formula.rb.tmpl` it
  keeps, and commits with its own `GITHUB_TOKEN`. No cross-repo token or secret
  lives here; this repo's `render.sh`/`formula.rb.tmpl` are the canonical
  source those copies track.

## Setup (done)

The tap repo (`scaffoldly/homebrew-tap`) exists and self-updates hourly. To
change the formula shape, edit `formula.rb.tmpl` here and copy it into the tap
(or let the next render pick it up if you keep them in sync).

## Self-update interaction

A tap-installed binary lives under `*/Cellar/*`, which the self-updater treats
as a managed install: it never swaps the binary in place (that would leave
Homebrew's receipt stale), and instead nudges `brew upgrade kubectl-add` when a
newer release exists. Keeping this tap bumped promptly (the publish job does
so automatically) keeps that nudge accurate.

The from-source, no-self-update **homebrew-core** formula is a separate, later
endgame tracked in #23.
