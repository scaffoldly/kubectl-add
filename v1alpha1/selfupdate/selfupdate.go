//go:build !noselfupdate

// Package selfupdate keeps a kubectl-add binary current by fetching newer
// releases, verifying them (checksum + cosign signature), and swapping the
// symlink — never overwriting the running binary. It only acts on installs it
// owns (the versioned-binary + symlink layout); managed installs (Homebrew
// core, krew, Nix) are deferred to their own package managers, and a
// noselfupdate build tag compiles the whole mechanism out.
package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	repo      = "scaffoldly/kubectl-add"
	envOptOut = "KUBECTL_ADD_NO_AUTO_UPDATE"
	// retain caps how many versioned binaries are kept for rollback; older
	// ones are pruned after a successful swap.
	retain = 3
	// checkEvery throttles the auto-update network check.
	checkEvery = 24 * time.Hour
)

// AutoUpdate applies a newer release when one exists, at most once per day, but
// only on installs kubectl-add owns. It is fail-open: any problem (opt-out,
// managed install, network, verification) is logged at debug and returns
// without disrupting the caller's command. Disabled by KUBECTL_ADD_NO_AUTO_UPDATE.
func AutoUpdate(ctx context.Context, current string, client *http.Client) {
	if os.Getenv(envOptOut) != "" {
		slog.Debug("auto-update disabled", "env", envOptOut)
		return
	}
	if err := update(ctx, current, client, false); err != nil {
		slog.Debug("auto-update skipped", "err", err)
	}
}

// Update forces an immediate check and apply, bypassing the once-per-day
// throttle and the env opt-out (but not the hard guards on managed or
// non-writable installs). It reports why it could not update, or nil on
// success or already-current.
func Update(ctx context.Context, current string, client *http.Client) error {
	return update(ctx, current, client, true)
}

func update(ctx context.Context, current string, client *http.Client, force bool) error {
	cur, err := semver.NewVersion(current)
	if err != nil {
		return fmt.Errorf("this is not a release build (version %q); install a release to enable updates", current)
	}

	exe, err := resolveExe()
	if err != nil {
		return err
	}

	// A package manager owns upgrades for its installs: never swap the binary
	// (a stale brew/krew/nix receipt is worse than being a release behind),
	// but still do the version check so we can nudge the right command.
	manager, managed := managedInstall(exe)

	// For an install we own, confirm the versioned+symlink layout before any
	// network call; a bare binary bails here.
	var inst *install
	if !managed {
		if inst, err = locate(exe); err != nil {
			return err
		}
	}

	if !force {
		if recentlyChecked() {
			return nil
		}
		// Stamp before the network call so a run of failures doesn't retry
		// every invocation.
		markChecked()
	}

	tag, err := latestTag(ctx, client)
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}
	latest, err := semver.NewVersion(strings.TrimPrefix(tag, "v"))
	if err != nil {
		return fmt.Errorf("unparseable release tag %q: %w", tag, err)
	}
	upToDate := !latest.GreaterThan(cur)

	if managed {
		if upToDate {
			if force {
				fmt.Fprintf(os.Stderr, "kubectl-add is up to date (%s)\n", current)
			}
			return nil
		}
		fmt.Fprintf(os.Stderr, "kubectl-add %s is available (installed v%s); update with: %s\n",
			tag, cur.String(), upgradeCommand(manager))
		return nil
	}

	if upToDate {
		slog.Debug("already up to date", "current", current, "latest", tag)
		if force {
			fmt.Fprintf(os.Stderr, "kubectl-add is up to date (%s)\n", current)
		}
		return nil
	}

	if err := apply(ctx, client, inst, latest, tag); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "kubectl-add self-updated v%s → %s\n", cur.String(), tag)
	return nil
}

// upgradeCommand is the command that updates a package-manager-owned install.
func upgradeCommand(manager string) string {
	switch manager {
	case "Homebrew":
		return "brew upgrade kubectl-add"
	case "krew":
		return "kubectl krew upgrade add"
	case "Nix":
		return "nix profile upgrade kubectl-add"
	default:
		return "your package manager"
	}
}

// install describes an install kubectl-add owns: a versioned binary reached via
// a kubectl-add symlink in a writable directory.
type install struct {
	dir  string // directory holding the versioned binaries and the symlink
	link string // the kubectl-add symlink kubectl discovers
}

// resolveExe returns the path of the running binary with symlinks resolved, so
// the versioned file (not the kubectl-add symlink) is what we inspect.
func resolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// locate confirms the running binary is one we own — the versioned file in a
// writable directory beside its kubectl-add symlink — and returns its layout.
func locate(exe string) (*install, error) {
	if !strings.HasPrefix(filepath.Base(exe), "kubectl_add_") {
		return nil, fmt.Errorf("not a versioned install; self-update needs the symlink layout")
	}

	dir := filepath.Dir(exe)
	link := filepath.Join(dir, "kubectl-add")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("no kubectl-add symlink at %s", link)
	}
	if !writable(dir) {
		return nil, fmt.Errorf("install dir %s is not writable", dir)
	}
	return &install{dir: dir, link: link}, nil
}

// managedInstall reports whether the executable path belongs to a package
// manager that owns upgrades (Homebrew, Nix, krew), so self-update defers.
func managedInstall(exe string) (string, bool) {
	// Normalize backslashes to forward slashes so the markers match on Windows
	// too (krew installs there as ...\.krew\...), regardless of which OS
	// produced the path.
	slash := strings.ReplaceAll(exe, `\`, "/")
	for _, m := range []struct{ marker, name string }{
		{"/Cellar/", "Homebrew"},
		{"/nix/store/", "Nix"},
		{"/.krew/", "krew"},
	} {
		if strings.Contains(slash, m.marker) {
			return m.name, true
		}
	}
	return "", false
}

// apply downloads the release archive for this platform, verifies it, writes
// the new versioned binary beside the current one, and repoints the symlink.
func apply(ctx context.Context, client *http.Client, inst *install, latest *semver.Version, tag string) error {
	asset := fmt.Sprintf("kubectl-add_%s_%s.zip", runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)

	archive, err := download(ctx, client, base)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", asset, err)
	}

	// Checksum: a cheap integrity pre-check against the published .sha256.
	if sum, err := download(ctx, client, base+".sha256"); err != nil {
		return fmt.Errorf("downloading checksum: %w", err)
	} else if err := verifyChecksum(archive, sum); err != nil {
		return err
	}

	// Signature: the trust anchor. Never swap an archive this can't prove.
	sig, err := download(ctx, client, base+".sigstore")
	if err != nil {
		return fmt.Errorf("downloading signature bundle: %w", err)
	}
	if err := verifySignature(archive, sig); err != nil {
		return err
	}

	binName := "kubectl-add"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binary, err := extractBinary(archive, binName)
	if err != nil {
		return err
	}

	// Write the new versioned binary atomically (temp + rename within dir),
	// mirroring `make link`'s naming: kubectl_add_<version>, no leading v.
	newName := "kubectl_add_" + latest.String()
	newPath := filepath.Join(inst.dir, newName)
	if err := writeExecutable(inst.dir, newPath, binary); err != nil {
		return err
	}

	// Repoint the symlink onto the new binary with a relative target so the
	// directory stays relocatable. The running process keeps its own inode.
	tmpLink := inst.link + ".new"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(newName, tmpLink); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	if err := os.Rename(tmpLink, inst.link); err != nil {
		return fmt.Errorf("repointing symlink: %w", err)
	}

	prune(inst.dir, newName, retain)
	return nil
}

func verifyChecksum(archive, sumFile []byte) error {
	// .sha256 files are "<hexdigest>  <filename>"; take the first field.
	fields := strings.Fields(string(sumFile))
	if len(fields) == 0 {
		return fmt.Errorf("empty checksum file")
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, fields[0]) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, fields[0])
	}
	return nil
}

// extractBinary pulls the named binary out of the release zip.
func extractBinary(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("reading archive: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s in archive: %w", name, err)
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("archive has no %s", name)
}

// writeExecutable writes data to path via a temp file in dir + rename, so a
// partial download never lands as a runnable binary.
func writeExecutable(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".kubectl_add_*")
	if err != nil {
		return fmt.Errorf("creating temp binary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installing binary: %w", err)
	}
	return nil
}

// prune keeps the newest `keep` versioned binaries (always keeping `current`)
// and removes the rest.
func prune(dir, current string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var versioned []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "kubectl_add_") {
			versioned = append(versioned, e.Name())
		}
	}
	if len(versioned) <= keep {
		return
	}
	// Sort newest-first by semver; unparseable names sort last.
	sort.SliceStable(versioned, func(i, j int) bool {
		vi, ei := semver.NewVersion(strings.TrimPrefix(versioned[i], "kubectl_add_"))
		vj, ej := semver.NewVersion(strings.TrimPrefix(versioned[j], "kubectl_add_"))
		if ei != nil || ej != nil {
			return ei == nil
		}
		return vi.GreaterThan(vj)
	})
	for _, name := range versioned[keep:] {
		if name == current {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// latestTag returns the newest release's tag from the GitHub API.
func latestTag(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	var out struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release")
	}
	return out.TagName, nil
}

// download fetches a URL into memory, capping the read so a hostile response
// can't exhaust RAM (release archives are ~50 MB; 200 MB is generous headroom).
func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".kubectl-add-writable-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// recentlyChecked reports whether an auto-update check ran within checkEvery.
func recentlyChecked() bool {
	p := stampPath()
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < checkEvery
}

func markChecked() {
	p := stampPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	now := time.Now()
	if f, err := os.Create(p); err == nil {
		f.Close()
		_ = os.Chtimes(p, now, now)
	}
}

func stampPath() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "kubectl-add", "last-update-check")
}
