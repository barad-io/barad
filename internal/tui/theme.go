package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/barad-io/barad/wire"
)

// Terminal-native palette aligned with Barad's Anodized Carbon Dark and Ceramic
// Light themes. Barad never overrides the host terminal's background colour so
// it integrates seamlessly into Rex, Ghostty, tmux, and any terminal theme.
var (
	BorderSubtle, BorderDefault         color.Color
	Text, TextSec, TextMuted, TextFaint color.Color
	Accent, AccentText, Warn            color.Color
	Agent, Success, Danger, Violet      color.Color
	SelBg                               color.Color

	CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct color.Color
	MarkClaude, MarkCodex, MarkGrok, MarkAgy                              color.Color

	SText, SSec, SMuted, SFaint, SAccent, SAgent, SOk, SDanger, SWarn lipgloss.Style
	SCursor, SSel                                                     lipgloss.Style

	activeTheme = "night"
)

// Themes lists the accepted theme preference names.
var Themes = []string{"auto", "night", "paper"}

func Fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// resolveTheme chooses "night" or "paper" from preference, env override, and
// terminal light/dark hint.
func resolveTheme(pref string, terminalLight *bool) string {
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("BARAD_THEME"))); env == "night" || env == "paper" {
		return env
	}
	if pref == "night" || pref == "paper" {
		return pref
	}
	if terminalLight != nil {
		if *terminalLight {
			return "paper"
		}
		return "night"
	}
	if fgbg := os.Getenv("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil && bg >= 7 && bg != 8 {
			return "paper"
		}
	}
	return "night"
}

func applyTheme(name string) {
	c := lipgloss.Color
	if name != "paper" {
		name = "night"
	}
	activeTheme = name
	switch name {
	case "paper": // Ceramic Light
		BorderSubtle, BorderDefault = c("#d4cec4"), c("#b7afa3")
		Text, TextSec, TextMuted, TextFaint = c("#14191f"), c("#353e49"), c("#5a6169"), c("#757068")
		Accent, AccentText, Warn = c("#d83a17"), c("#b13112"), c("#9a5b00")
		Agent, Success, Danger, Violet = c("#1d5fa7"), c("#1f6f43"), c("#b4234e"), c("#5b4a9e")
		SelBg = c("#e8e2d7")
		CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct = c("#1d5fa7"), c("#1f6f43"), c("#855200"), c("#1c6b64"), c("#6e6a62"), c("#5a6169")
		MarkClaude, MarkCodex, MarkGrok, MarkAgy = c("#c45a33"), c("#1f6f43"), c("#353e49"), c("#1d5fa7")
		SCursor = lipgloss.NewStyle().Foreground(c("#ffffff")).Background(Accent)
		SSel = lipgloss.NewStyle().Foreground(Text).Background(SelBg).Bold(true)
	default: // Anodized Carbon Dark
		BorderSubtle, BorderDefault = c("#2a313b"), c("#3b4452")
		Text, TextSec, TextMuted, TextFaint = c("#ece8e0"), c("#c9c5be"), c("#9aa1ab"), c("#76808d")
		Accent, AccentText, Warn = c("#ff5d38"), c("#ff8a6b"), c("#f0b865")
		Agent, Success, Danger, Violet = c("#79b8ff"), c("#7ee787"), c("#ff7b93"), c("#c2a8ff")
		SelBg = c("#232b36")
		CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct = c("#79b8ff"), c("#7ee787"), c("#f0b865"), c("#76d7c4"), c("#7b8594"), c("#9aa1ab")
		MarkClaude, MarkCodex, MarkGrok, MarkAgy = c("#e07c5a"), c("#7ee787"), c("#c9c5be"), c("#79b8ff")
		SCursor = lipgloss.NewStyle().Foreground(c("#11161d")).Background(Accent)
		SSel = lipgloss.NewStyle().Foreground(Text).Background(SelBg).Bold(true)
	}
	SText, SSec, SMuted, SFaint = Fg(Text), Fg(TextSec), Fg(TextMuted), Fg(TextFaint)
	SAccent, SAgent, SOk, SDanger, SWarn = Fg(Accent), Fg(Agent), Fg(Success), Fg(Danger), Fg(Warn)
}

// harnessLabel returns the human-readable name for a Barad v2 harness.
func harnessLabel(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "claude":
		return "claude"
	case "codex", "openai", "chatgpt":
		return "codex"
	case "grok", "xai":
		return "grok"
	case "agy", "antigravity":
		return "agy"
	default:
		if id == "" {
			return "agent"
		}
		return id
	}
}

// harnessMark returns a subtle glyph and colour for a harness.
func harnessMark(id string) (string, color.Color) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "claude":
		return "✻", MarkClaude
	case "codex", "openai", "chatgpt":
		return "◍", MarkCodex
	case "grok", "xai":
		return "◇", MarkGrok
	case "agy", "antigravity":
		return "✦", MarkAgy
	default:
		return "●", TextMuted
	}
}

// Spinner frames for active work.
var Spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// An attachment is an image or text file queued with the next prompt.
type attachment struct {
	name      string
	data      []byte
	mediaType string
}

func isImage(mediaType string) bool { return strings.HasPrefix(mediaType, "image/") }

func attachMark(mediaType string) string {
	if isImage(mediaType) {
		return "▣ "
	}
	return "▤ "
}

// readAttachment reads a local file path (drag-dropped or referenced) as an
// image or UTF-8 text attachment respecting Barad's upload limits.
func readAttachment(p string) (attachment, error) {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	p = strings.ReplaceAll(p, `\ `, " ")
	if strings.HasPrefix(p, "file://") {
		p = strings.TrimPrefix(p, "file://")
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return attachment{}, errors.New("not a file")
	}
	if info.Size() > wire.PromptMediaMaximumBytes {
		return attachment{}, fmt.Errorf("%s exceeds %d MB limit", filepath.Base(p), wire.PromptMediaMaximumBytes>>20)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return attachment{}, err
	}
	mt := http.DetectContentType(data)
	if !isImage(mt) {
		if ext := mime.TypeByExtension(filepath.Ext(p)); isImage(ext) {
			mt = ext
		} else if utf8.Valid(data) {
			if len(data) > wire.PromptTextMaximumBytes {
				return attachment{}, fmt.Errorf("%s exceeds text limit", filepath.Base(p))
			}
			mt = wire.PromptTextType
		} else {
			return attachment{}, fmt.Errorf("%s is not an image or UTF-8 text file", filepath.Base(p))
		}
	}
	return attachment{name: filepath.Base(p), data: data, mediaType: mt}, nil
}

// clipboardImage reads a PNG image from the host clipboard without hanging.
func clipboardImage() ([]byte, error) {
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
			return nil, errors.New("clipboard has no image")
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
		return nil, errors.New("clipboard has no image")
	}
	return out, nil
}
