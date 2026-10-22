package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.5.0", "v0.4.0", true}, {"v0.10.0", "v0.9.9", true}, {"v1.0.0", "v0.99.0", true},
		{"v0.4.0", "v0.4.0", false}, {"v0.3.9", "v0.4.0", false},
		{"v0.5.0", "dev", false}, {"dev", "v0.4.0", false}, {"v0.5", "v0.4.0", false}, {"0.5.0", "v0.4.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// release serves a release the way GitHub does, signed with a key of its
// own that the test makes barad trust.
type release struct {
	tag          string
	binary, sums []byte
	sig          string
}

func serve(t *testing.T, r *release) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		base := "/download/" + r.tag + "/"
		name := "barad-" + runtime.GOOS + "-" + runtime.GOARCH
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		switch req.URL.Path {
		case "/latest":
			w.Header().Set("Location", "https://github.com/barad-io/barad/releases/tag/"+r.tag)
			w.WriteHeader(http.StatusFound)
		case base + "SHA256SUMS":
			w.Write(r.sums)
		case base + "SHA256SUMS.sig":
			w.Write([]byte(r.sig))
		case base + name:
			w.Write(r.binary)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	prev := Releases
	Releases = srv.URL
	t.Cleanup(func() { Releases = prev })
}

func signed(t *testing.T, binary []byte) *release {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	prev := signingKey
	signingKey = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	t.Cleanup(func() { signingKey = prev })
	sum := sha256.Sum256(binary)
	name := "barad-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sums := []byte(fmt.Sprintf("%s  install.sh\n%s  %s\n", strings.Repeat("0", 64), hex.EncodeToString(sum[:]), name))
	return &release{tag: "v9.9.9", binary: binary, sums: sums, sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n"}
}

func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "barad")
	if err := os.WriteFile(exe, []byte("old barad"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestInstallReplacesBaradWithTheSignedRelease(t *testing.T) {
	r := signed(t, []byte("new barad"))
	serve(t, r)
	tag, err := Latest(context.Background())
	if err != nil || tag != "v9.9.9" {
		t.Fatalf("latest = %q, %v", tag, err)
	}
	exe := installed(t)
	if err := Install(context.Background(), tag, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(got) != "new barad" || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed %q, mode %v", got, st.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".barad-update-*")); len(left) > 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestInstallRefusesWhatTheSignatureDoesNotVouchFor(t *testing.T) {
	for name, spoil := range map[string]func(r *release){
		"a changed binary":  func(r *release) { r.binary = []byte("evil barad") },
		"changed checksums": func(r *release) { r.sums = []byte(strings.Replace(string(r.sums), "0", "1", 1)) },
		"another key's signature": func(r *release) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			r.sig = base64.StdEncoding.EncodeToString(ed25519.Sign(other, r.sums))
		},
		"no signature": func(r *release) { r.sig = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := signed(t, []byte("new barad"))
			spoil(r)
			serve(t, r)
			exe := installed(t)
			if err := Install(context.Background(), r.tag, exe); err == nil {
				t.Fatal("installed it")
			}
			if got, _ := os.ReadFile(exe); string(got) != "old barad" {
				t.Fatalf("barad is now %q", got)
			}
		})
	}
}

// The key barad ships with parses, so a real release can be checked.
func TestTheShippedKeyIsAnEd25519Key(t *testing.T) {
	if err := verify([]byte("x"), []byte(base64.StdEncoding.EncodeToString(make([]byte, 64)))); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("verify with the shipped key: %v", err)
	}
}
