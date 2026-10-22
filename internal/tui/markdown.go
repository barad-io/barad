package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Markdown lays out agent markdown in width cells: paragraphs, headings as
// small uppercase kickers, lists, quotes, rules, tables, links, emphasis,
// inline code on a quiet chip, and fenced code in the code well.
func Markdown(src string, width int) []Line {
	source := []byte(src)
	doc := mdParser.Parser().Parse(text.NewReader(source))
	r := mdRenderer{src: source}
	return r.blocks(doc, width)
}

var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

type mdRenderer struct{ src []byte }

// blocks lays out the block children of n, a blank line between each.
func (r *mdRenderer) blocks(n ast.Node, width int) []Line {
	var out []Line
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		block := r.block(c, width)
		if len(block) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, block...)
	}
	return out
}

func (r *mdRenderer) block(n ast.Node, width int) []Line {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return Wrap(r.inline(n, SText), width)
	case *ast.Heading:
		segs := r.inline(n, SText.Bold(true))
		if n.Level == 1 {
			return Wrap(segs, width)
		}
		// a kicker: small, uppercase, quiet
		for i := range segs {
			segs[i].Text = strings.ToUpper(segs[i].Text)
			segs[i].Style = Fg(TextMuted).Bold(true)
		}
		return Wrap(segs, width)
	case *ast.ThematicBreak:
		return []Line{{S(strings.Repeat("─", width), Fg(BorderDefault))}}
	case *ast.FencedCodeBlock:
		return CodeBlock(r.lines(n), string(n.Language(r.src)), width, Subtle)
	case *ast.CodeBlock:
		return CodeBlock(r.lines(n), "", width, Subtle)
	case *ast.Blockquote:
		var out []Line
		for _, l := range r.blocks(n, width-2) {
			out = append(out, append(Line{S("▎ ", Fg(BorderDefault))}, recolor(l, TextSec)...))
		}
		return out
	case *ast.List:
		return r.list(n, width)
	case *east.Table:
		return r.table(n, width)
	case *ast.HTMLBlock:
		return Wrap([]Seg{S(r.lines(n), SMuted)}, width)
	}
	return nil
}

// recolor gives a line's default-coloured text another colour.
func recolor(l Line, c color.Color) Line {
	out := make(Line, len(l))
	for i, s := range l {
		fg := s.Style.GetForeground()
		if _, none := fg.(lipgloss.NoColor); none || fg == Text {
			s.Style = s.Style.Foreground(c)
		}
		out[i] = s
	}
	return out
}

func (r *mdRenderer) lines(n ast.Node) string {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(r.src))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *mdRenderer) list(n *ast.List, width int) []Line {
	var out []Line
	number := n.Start
	depth := 0
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*ast.List); ok {
			depth++
		}
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		marker := []string{"•", "◦", "▪"}[depth%3]
		if n.IsOrdered() {
			marker = fmt.Sprintf("%d.", number)
			number++
		}
		indent := ansi.StringWidth(marker) + 1
		// A task list item starts with its check box.
		var body []Line
		for c := item.FirstChild(); c != nil; c = c.NextSibling() {
			block := r.block(c, width-indent)
			if len(block) == 0 {
				continue
			}
			if len(body) > 0 && !n.IsTight {
				body = append(body, Line{})
			}
			body = append(body, block...)
		}
		for i, l := range body {
			lead := gap(indent)
			if i == 0 {
				lead = S(marker+" ", SMuted)
			}
			out = append(out, append(Line{lead}, l...))
		}
		if !n.IsTight && item.NextSibling() != nil {
			out = append(out, Line{})
		}
	}
	return out
}

// table lays out a GFM table: columns as wide as their widest cell, cut to
// share the width when they don't fit.
func (r *mdRenderer) table(n *east.Table, width int) []Line {
	var rows [][][]Seg
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells [][]Seg
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			st := SText
			if _, header := row.(*east.TableHeader); header {
				st = SText.Bold(true)
			}
			cells = append(cells, r.inline(cell, st))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return nil
	}
	cols := 0
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], Line(cell).Width())
		}
	}
	// Narrow the widest column until the table fits, three cells apart.
	for total := sumWidths(widths) + 3*(cols-1); total > width; total = sumWidths(widths) + 3*(cols-1) {
		widest := 0
		for i := range widths {
			if widths[i] > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 4 {
			break
		}
		widths[widest]--
	}
	var out []Line
	for ri, row := range rows {
		var l Line
		for i := 0; i < cols; i++ {
			if i > 0 {
				l = append(l, S(" │ ", Fg(BorderDefault)))
			}
			var cell Line
			if i < len(row) {
				cell = row[i]
			}
			l = append(l, S(Line(cell).Paint(nil, widths[i]), lipgloss.NewStyle()))
		}
		out = append(out, l)
		if ri == 0 {
			var rule []string
			for _, w := range widths {
				rule = append(rule, strings.Repeat("─", w))
			}
			out = append(out, Line{S(strings.Join(rule, "─┼─"), Fg(BorderDefault))})
		}
	}
	return out
}

func sumWidths(ws []int) int {
	t := 0
	for _, w := range ws {
		t += w
	}
	return t
}

// inline flattens n's inline children into styled segments.
func (r *mdRenderer) inline(n ast.Node, base lipgloss.Style) []Seg {
	var segs []Seg
	var walk func(n ast.Node, st lipgloss.Style)
	walk = func(n ast.Node, st lipgloss.Style) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				t := string(c.Value(r.src))
				if c.SoftLineBreak() {
					t += " "
				}
				if c.HardLineBreak() {
					t += "\n"
				}
				segs = append(segs, S(t, st))
			case *ast.String:
				segs = append(segs, S(string(c.Value), st))
			case *ast.CodeSpan:
				var b strings.Builder
				for g := c.FirstChild(); g != nil; g = g.NextSibling() {
					if t, ok := g.(*ast.Text); ok {
						b.Write(t.Value(r.src))
					}
				}
				segs = append(segs, S(b.String(), Fg(TextSec).Background(Muted)))
			case *ast.Emphasis:
				if c.Level >= 2 {
					walk(c, st.Bold(true))
				} else {
					walk(c, st.Italic(true))
				}
			case *east.Strikethrough:
				walk(c, st.Strikethrough(true))
			case *ast.Link:
				before := len(segs)
				walk(c, st.Foreground(Agent).Underline(true))
				label := ""
				for _, s := range segs[before:] {
					label += s.Text
				}
				if url := string(c.Destination); url != "" && url != label {
					segs = append(segs, S(" ("+url+")", SFaint))
				}
			case *ast.AutoLink:
				segs = append(segs, S(string(c.URL(r.src)), st.Foreground(Agent).Underline(true)))
			case *ast.Image:
				segs = append(segs, S("▣ "+string(c.Destination), SMuted))
			case *ast.RawHTML:
				for i := 0; i < c.Segments.Len(); i++ {
					seg := c.Segments.At(i)
					segs = append(segs, S(string(seg.Value(r.src)), SMuted))
				}
			case *east.TaskCheckBox:
				box := "☐ "
				if c.IsChecked {
					box = "☑ "
				}
				segs = append(segs, S(box, SMuted))
			default:
				walk(c, st)
			}
		}
	}
	walk(n, base)
	return segs
}

// CodeBlock highlights code on a surface w cells wide: half a row of padding
// above and below (half blocks), and the language at the top right.
func CodeBlock(code, lang string, width int, bg color.Color) []Line {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	var rows []Line
	cur := Line{}
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		rows = Wrap([]Seg{S(code, SSec)}, width-4)
	} else {
		for tok := it(); tok != chroma.EOF; tok = it() {
			for pi, p := range strings.Split(tok.Value, "\n") {
				if pi > 0 {
					rows = append(rows, cur)
					cur = Line{}
				}
				if p != "" {
					cur = append(cur, S(strings.ReplaceAll(p, "\t", "    "), tokenStyle(tok.Type)))
				}
			}
		}
		if len(cur) > 0 {
			rows = append(rows, cur)
		}
	}
	pad := lipgloss.NewStyle().Background(bg)
	out := []Line{{S(strings.Repeat("▄", width), Fg(bg))}}
	for i, r := range rows {
		l := Line{S("  ", pad)}
		for _, s := range r {
			l = append(l, S(s.Text, s.Style.Background(bg)))
		}
		if i == 0 && lang != "" && l.Width()+len(lang)+4 <= width {
			l = append(l, S(strings.Repeat(" ", width-l.Width()-len(lang)-2), pad), S(lang, Fg(TextFaint).Background(bg)))
		}
		if w := l.Width(); w < width {
			l = append(l, S(strings.Repeat(" ", width-w), pad))
		} else {
			l = Line{S(ansi.Truncate(l.Paint(bg, 0), width-1, "…"), lipgloss.NewStyle()), S(" ", pad)}
		}
		out = append(out, l)
	}
	return append(out, Line{S(strings.Repeat("▀", width), Fg(bg))})
}

func tokenStyle(t chroma.TokenType) lipgloss.Style {
	switch {
	case t == chroma.Comment || t.InCategory(chroma.Comment):
		return Fg(CodeComment).Italic(true)
	case t.InSubCategory(chroma.LiteralString):
		return Fg(CodeString)
	case t.InSubCategory(chroma.LiteralNumber) || t == chroma.KeywordConstant || t == chroma.NameConstant:
		return Fg(CodeNumber)
	case t == chroma.KeywordType || t == chroma.NameBuiltin || t == chroma.NameClass:
		return Fg(CodeType)
	case t.InCategory(chroma.Keyword):
		return Fg(CodeKeyword)
	case t == chroma.NameFunction:
		return Fg(Text).Bold(true)
	case t.InCategory(chroma.Operator) || t == chroma.Punctuation:
		return Fg(CodePunct)
	case t == chroma.GenericInserted:
		return Fg(Success)
	case t == chroma.GenericDeleted:
		return Fg(Danger)
	}
	return Fg(Text)
}
