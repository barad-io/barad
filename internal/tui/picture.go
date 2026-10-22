package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi/kitty"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Graphics is how the terminal draws pictures.
//
//   - GraphicsKitty: the kitty graphics protocol with Unicode placeholders
//     (kitty, Ghostty). Pictures are real pixels in ordinary cells, so they
//     scroll, clip and diff with the rest of the screen.
//   - GraphicsBlocks: truecolor half blocks (▀, two pixels a cell), which
//     every modern terminal draws.
type Graphics int

const (
	GraphicsBlocks Graphics = iota
	GraphicsKitty
)

// DetectGraphics reads the environment; BARAD_GRAPHICS=kitty|blocks decides.
func DetectGraphics() Graphics {
	switch os.Getenv("BARAD_GRAPHICS") {
	case "kitty":
		return GraphicsKitty
	case "blocks":
		return GraphicsBlocks
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("TERM") == "xterm-kitty" || os.Getenv("TERM") == "xterm-ghostty" ||
		os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("TERM_PROGRAM") == "rex" {
		return GraphicsKitty
	}
	return GraphicsBlocks
}

// Picture is an image the app shows: a screenshot the agent took, an image
// you attached, or the cover it drew.
type Picture struct {
	Name  string
	Bytes int // its size as sent or received
	Img   image.Image
	png   []byte // kitty is sent PNG

	mu    sync.Mutex
	lines map[[2]int][]Line // by cols and Graphics
	ids   map[int]uint32    // the kitty image placed at each width
}

// NewPicture decodes PNG, JPEG, WebP or GIF data.
func NewPicture(name string, data []byte) (*Picture, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a PNG, JPEG, WebP or GIF image", name)
	}
	p := &Picture{Name: name, Bytes: len(data), Img: img}
	if format == "png" {
		p.png = data
	}
	return p, nil
}

// DrawnPicture wraps an image the app drew itself.
func DrawnPicture(name string, img image.Image) *Picture {
	return &Picture{Name: name, Img: img}
}

// Rows is how many cell rows the picture takes at cols wide (cells are ~1:2).
func (p *Picture) Rows(cols int) int {
	b := p.Img.Bounds()
	return max(1, int(float64(cols)*float64(b.Dy())/float64(b.Dx())/2+0.5))
}

// Size is the picture's pixels and bytes, for captions.
func (p *Picture) Size() string {
	b := p.Img.Bounds()
	kb := float64(p.Bytes) / 1024
	switch {
	case p.Bytes == 0:
		return fmt.Sprintf("%d×%d", b.Dx(), b.Dy())
	case kb > 1024:
		return fmt.Sprintf("%d×%d · %.1f MB", b.Dx(), b.Dy(), kb/1024)
	}
	return fmt.Sprintf("%d×%d · %.0f KB", b.Dx(), b.Dy(), kb)
}

// Kitty images are uploaded once per width they are shown at, outside the
// frame. Lines queues an upload the first time a width is drawn; the app
// sends the queue (Transmissions) on its next tick, so a picture appears a
// frame after its placeholder.
var (
	kittyMu      sync.Mutex
	kittyNext    uint32 = 1
	kittyPending []string
)

// Transmissions takes the queued kitty uploads.
func Transmissions() string {
	kittyMu.Lock()
	defer kittyMu.Unlock()
	out := strings.Join(kittyPending, "")
	kittyPending = nil
	return out
}

// Encoded is the picture as PNG.
func (p *Picture) Encoded() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.encoded()
}

func (p *Picture) encoded() []byte {
	if p.png == nil {
		var buf bytes.Buffer
		if err := png.Encode(&buf, p.Img); err == nil {
			p.png = buf.Bytes()
		}
	}
	return p.png
}

func (p *Picture) kittyID(cols int) uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.ids[cols]; ok {
		return id
	}
	if p.ids == nil {
		p.ids = map[int]uint32{}
	}
	p.encoded()
	kittyMu.Lock()
	id := kittyNext
	kittyNext = kittyNext%0xfffffe + 1 // ids are 24-bit colours; 0 is none
	kittyPending = append(kittyPending, transmit(p.png, id, cols, p.Rows(cols)))
	kittyMu.Unlock()
	p.ids[cols] = id
	return id
}

// transmit is the escape sequence that uploads a PNG for a cols×rows virtual
// placement.
func transmit(data []byte, id uint32, cols, rows int) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(enc); i += 4096 {
		chunk := enc[i:min(len(enc), i+4096)]
		more := 0
		if i+4096 < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,U=1,f=100,q=2,i=%d,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
	return b.String()
}

// Lines draws the picture cols wide in g's way.
func (p *Picture) Lines(cols int, g Graphics) []Line {
	key := [2]int{cols, int(g)}
	p.mu.Lock()
	cached, ok := p.lines[key]
	p.mu.Unlock()
	if ok {
		return cached
	}
	rows := p.Rows(cols)
	var out []Line
	if g == GraphicsKitty {
		id := p.kittyID(cols)
		st := lipgloss.NewStyle().Foreground(color.RGBA{uint8(id >> 16), uint8(id >> 8), uint8(id), 255})
		for r := 0; r < rows; r++ {
			var b strings.Builder
			for c := 0; c < cols; c++ {
				b.WriteRune(kitty.Placeholder)
				b.WriteRune(kitty.Diacritic(r))
				b.WriteRune(kitty.Diacritic(c))
			}
			out = append(out, Line{S(b.String(), st)})
		}
	} else {
		dst := image.NewRGBA(image.Rect(0, 0, cols, rows*2))
		draw.CatmullRom.Scale(dst, dst.Bounds(), p.Img, p.Img.Bounds(), draw.Src, nil)
		for r := 0; r < rows; r++ {
			var b strings.Builder
			for c := 0; c < cols; c++ {
				t, u := dst.RGBAAt(c, r*2), dst.RGBAAt(c, r*2+1)
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", t.R, t.G, t.B, u.R, u.G, u.B)
			}
			b.WriteString("\x1b[m")
			out = append(out, Line{S(b.String(), lipgloss.NewStyle())})
		}
	}
	p.mu.Lock()
	if p.lines == nil {
		p.lines = map[[2]int][]Line{}
	}
	p.lines[key] = out
	p.mu.Unlock()
	return out
}

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif"}

// pastedPath recognises a paste that is only a path to an image — what a
// terminal sends when a file is dragged onto it.
func pastedPath(text string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(text), `"'`)
	if strings.HasPrefix(p, "file://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	p = strings.ReplaceAll(p, `\ `, " ")
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	if strings.ContainsAny(p, "\n") || imageTypes[strings.ToLower(filepath.Ext(p))] == "" {
		return "", false
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

// clipboardImage reads an image from the system clipboard with the
// platform's own tool: a terminal cannot paste pictures, so Ctrl+V reaches
// the app as a key and it fetches the image itself.
func clipboardImage() ([]byte, error) {
	// The tools can wait (wl-paste does, with nothing to offer); never hang.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		f := filepath.Join(os.TempDir(), "barad-clipboard.png")
		script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
set eof f to 0
write (the clipboard as «class PNGf») to f
close access f`, f)
		if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err != nil {
			return nil, errors.New("the clipboard has no image")
		}
		return os.ReadFile(f)
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			`Add-Type -AssemblyName System.Windows.Forms; $i=[Windows.Forms.Clipboard]::GetImage(); if($i){$m=New-Object IO.MemoryStream; $i.Save($m,[Drawing.Imaging.ImageFormat]::Png); [Console]::OpenStandardOutput().Write($m.ToArray(),0,$m.Length)}`)
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd = exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "image/png")
		} else {
			cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		}
	}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, errors.New("the clipboard has no image")
	}
	return out, nil
}

// openExternally writes data to a temporary file and hands it to the
// system's viewer.
func openExternally(name string, data []byte) error {
	f := filepath.Join(os.TempDir(), "barad-"+filepath.Base(name))
	if err := os.WriteFile(f, data, 0o600); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", f).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", f).Start()
	}
	return exec.Command("xdg-open", f).Start()
}
