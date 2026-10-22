// Package update replaces barad with its newest release.
//
// The release workflow signs each release's SHA256SUMS with an Ed25519 key
// that only it holds (SHA256SUMS.sig). barad carries the public half
// (release-signing.pem) and installs a binary only when the signed checksums
// vouch for it, so a release changed by anyone but the workflow is refused.
package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

//go:embed release-signing.pem
var signingKey []byte

// Releases is where barad's releases are published.
var Releases = "https://github.com/barad-io/barad/releases"

// Latest is the newest release's version, such as "v0.5.0": where
// releases/latest redirects, which costs no API call.
func Latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, Releases+"/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if _, ok := parse(tag); !ok {
		return "", fmt.Errorf("could not find the latest release (%s)", resp.Status)
	}
	return tag, nil
}

// Newer reports whether version a is a later release than b. A version that
// is not vMAJOR.MINOR.PATCH, such as a development build's, is never newer
// and nothing is newer than it.
func Newer(a, b string) bool {
	va, oka := parse(a)
	vb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

// Released reports whether version is a release (not a development build).
func Released(version string) bool {
	_, ok := parse(version)
	return ok
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Install downloads release tag for this platform, checks it against the
// release's signed checksums, and puts it in place of the file at exe.
func Install(ctx context.Context, tag, exe string) error {
	base := Releases + "/download/" + tag + "/"
	sums, err := fetch(ctx, base+"SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	sig, err := fetch(ctx, base+"SHA256SUMS.sig", 1<<10)
	if err != nil {
		return err
	}
	if err := verify(sums, sig); err != nil {
		return err
	}
	name := "barad-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if sum, file, ok := strings.Cut(line, "  "); ok && file == name {
			want = sum
		}
	}
	if want == "" {
		return fmt.Errorf("release %s has no build for %s/%s", tag, runtime.GOOS, runtime.GOARCH)
	}
	bin, err := fetch(ctx, base+name, 256<<20)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(bin); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("the download of %s does not match its signed checksum", name)
	}
	return replace(exe, bin)
}

// verify checks the release workflow's signature on the checksums.
func verify(sums, sig []byte) error {
	block, _ := pem.Decode(signingKey)
	if block == nil {
		return errors.New("barad's release key is missing")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return err
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return errors.New("barad's release key is not an Ed25519 key")
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || !ed25519.Verify(pub, sums, raw) {
		return errors.New("the release's checksums are not signed by barad's release workflow; not installing it")
	}
	return nil
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", path.Base(url), resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("download %s: larger than expected", path.Base(url))
	}
	return data, err
}

// replace writes data beside exe and renames it over exe, so the file is
// never half written. Windows will not replace a running program, so the
// old one is moved aside first (and removed at the next update).
func replace(exe string, data []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".barad-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (%w); reinstall with the install command instead", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), exe)
}
