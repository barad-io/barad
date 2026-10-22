package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Barad's two themes, from the web app's colour system:
// night (Night terrain) and paper. Ink on paper, a night rail, and vermilion
// as the only signal. Surfaces sit a step further apart than on the web, and
// the quiet text roles are lifted so every role reads at 4.5:1 or better on
// every surface it is drawn on. The app sets the terminal's own background and
// foreground to Canvas and Text while it runs.
var (
	Rail, Subtle, Canvas, Document, Raised, Muted                         color.Color
	BorderSubtle, BorderDefault                                           color.Color
	Text, TextSec, TextMuted, TextFaint                                   color.Color
	Accent, AccentText, AccentSurface, OnAccent                           color.Color
	Selection                                                             color.Color // text you drag over
	Agent, AgentSurface, Success, SuccessSurf, Danger, DangerSurf, Violet color.Color

	// Code, tuned to each theme's code well.
	CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct color.Color
	// Provider marks.
	MarkClaude, MarkChatGPT, MarkGrok color.Color

	SText, SSec, SMuted, SFaint, SAccent, SAgent, SOk, SDanger lipgloss.Style
	SCursor                                                    lipgloss.Style
)

// Themes are the names applyTheme accepts.
var Themes = []string{"night", "paper"}

func Fg(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// applyTheme sets every token. It may run again at any time; styles are
// derived here.
func applyTheme(name string) {
	c := lipgloss.Color
	switch name {
	case "paper":
		Rail, Subtle, Canvas, Document, Raised, Muted = c("#e5e0d6"), c("#e9e4da"), c("#f3efe8"), c("#faf8f3"), c("#fffdf9"), c("#e2dccf")
		BorderSubtle, BorderDefault = c("#dbd7cf"), c("#c2bdb4")
		Text, TextSec, TextMuted, TextFaint = c("#121921"), c("#353e49"), c("#565b62"), c("#625e57")
		Accent, AccentText, AccentSurface, OnAccent = c("#ff5b35"), c("#aa3f11"), c("#ffe3d7"), c("#21100c")
		Agent, AgentSurface, Success, SuccessSurf, Danger, DangerSurf, Violet = c("#27639c"), c("#e2effa"), c("#267147"), c("#e0f2e6"), c("#a22456"), c("#fce7eb"), c("#5b4a9e")
		CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct = c("#27639c"), c("#267147"), c("#7a5000"), c("#1f6f68"), c("#66625a"), c("#565b62")
		MarkClaude, MarkChatGPT, MarkGrok = c("#c4623f"), c("#121921"), c("#353e49")
		SCursor = lipgloss.NewStyle().Foreground(c("#fffdf9")).Background(Accent)
		Selection = c("#c9dcef")
	default: // night
		Rail, Subtle, Canvas, Document, Raised, Muted = c("#05080e"), c("#0b1016"), c("#11161d"), c("#182029"), c("#222b36"), c("#2c3643")
		BorderSubtle, BorderDefault = c("#272e38"), c("#353e49")
		Text, TextSec, TextMuted, TextFaint = c("#efebe2"), c("#cac7c0"), c("#a3a9b0"), c("#8b95a0")
		Accent, AccentText, AccentSurface, OnAccent = c("#ff5b35"), c("#fd9a73"), c("#3e2015"), c("#24110c")
		Agent, AgentSurface, Success, SuccessSurf, Danger, DangerSurf, Violet = c("#88bfe6"), c("#132b3d"), c("#8bcca1"), c("#152c1e"), c("#f28eab"), c("#3a1b24"), c("#b9a2e8")
		CodeKeyword, CodeString, CodeNumber, CodeType, CodeComment, CodePunct = c("#88bfe6"), c("#8bcca1"), c("#e0b25c"), c("#7cc7bb"), c("#8691a0"), c("#a3a9b0")
		MarkClaude, MarkChatGPT, MarkGrok = c("#d97757"), c("#e9e4da"), c("#cac7c0")
		SCursor = lipgloss.NewStyle().Foreground(Canvas).Background(Accent)
		Selection = c("#2f4b68")
	}
	SText, SSec, SMuted, SFaint = Fg(Text), Fg(TextSec), Fg(TextMuted), Fg(TextFaint)
	SAccent, SAgent, SOk, SDanger = Fg(Accent), Fg(Agent), Fg(Success), Fg(Danger)
}

// providerName is how a provider is named to people.
func providerName(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "openai":
		return "OpenAI"
	case "codex":
		return "ChatGPT"
	case "xai":
		return "xAI"
	case "grok":
		return "Grok"
	}
	return "Agent"
}

// providerMark is a provider's glyph and colour.
func providerMark(provider string) (string, color.Color) {
	switch provider {
	case "claude":
		return "✻", MarkClaude
	case "openai", "codex":
		return "◍", MarkChatGPT
	case "xai", "grok":
		return "◇", MarkGrok
	}
	return "•", TextMuted
}

// Spinner is the frames of work in progress.
var Spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
