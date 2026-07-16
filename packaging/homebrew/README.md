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
- On every published release, the `homebrew` job in
  [`.github/workflows/publish.yaml`](../../.github/workflows/publish.yaml) runs
  `render.sh` and pushes the result to `scaffoldly/homebrew-tap` as
  `Formula/kubectl-add.rb`.

## One-time setup (maintainers)

1. Create the public repo **`scaffoldly/homebrew-tap`** (the name must start
   with `homebrew-` for `brew tap scaffoldly/tap` to resolve).
2. Add a repository secret **`HOMEBREW_TAP_TOKEN`** to this repo: a PAT (or
   fine-grained token) with `contents: write` on `scaffoldly/homebrew-tap`. The
   publish job skips cleanly until this secret exists.
3. Publish (or re-publish) a release; the job populates the tap.

## Self-update interaction

A tap-installed binary lives under `*/Cellar/*`, which the self-updater treats
as a managed install: it never swaps the binary in place (that would leave
Homebrew's receipt stale), and instead nudges `brew upgrade kubectl-add` when a
newer release exists. Keeping this tap bumped promptly (the publish job does
so automatically) keeps that nudge accurate.

The from-source, no-self-update **homebrew-core** formula is a separate, later
endgame tracked in #23.
