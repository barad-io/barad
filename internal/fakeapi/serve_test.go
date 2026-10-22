package fakeapi

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"
)

// TestServeFakeAPI serves the fake API until interrupted, signed in with its
// files in $BARAD_FAKE_API_DIR, for running barad against it by hand:
//
//	XDG_CONFIG_HOME=$BARAD_FAKE_API_DIR BARAD_API_URL=$(cat $BARAD_FAKE_API_DIR/url) barad

func TestServeFakeAPI(t *testing.T) {
	dir := os.Getenv("BARAD_FAKE_API_DIR")
	if dir == "" {
		t.Skip("set BARAD_FAKE_API_DIR to serve the fake API")
	}
	f := New(1200 * time.Millisecond)
	defer f.Srv.Close()
	f.SignIn(t, dir)
	_ = os.WriteFile(filepath.Join(dir, "url"), []byte(f.Srv.URL), 0o600)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
}
