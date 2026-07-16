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

The tap ships the normal **auto-updating** build (per the channel map in #10). A
tap-installed binary self-updates in place: `*/Cellar/*` is user-writable, so
when a newer release exists the updater verifies and replaces the binary
directly. Homebrew's receipt goes momentarily stale, but the tap re-renders the
formula hourly, so `brew upgrade kubectl-add` converges on the same version — no
downgrade in practice.

The from-source, no-self-update **homebrew-core** formula is a separate, later
endgame (#23); it opts out at compile time with `-tags noselfupdate`, since
runtime `*/Cellar/*` detection can't tell a core install from a tap one.
