// Package negotiate implements pillar 1: content negotiation. It decides,
// from an Accept header, whether a requester wants the stripped markdown
// view instead of the full HTML page, and converts HTML into that stripped
// view.
package negotiate

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// MarkdownType is the media type served for the stripped view.
const MarkdownType = "text/markdown"

// WantsMarkdown reports whether the Accept header requests the markdown
// view at least as strongly as it requests HTML. Ties (equal or absent q
// values) favor markdown, since an explicit "text/markdown" entry is a
// stronger signal of intent than the implicit default HTML preference.
func WantsMarkdown(accept string) bool {
	if accept == "" {
		return false
	}
	mdQ, htmlQ := -1.0, -1.0
	for _, part := range strings.Split(accept, ",") {
		mt, q := parseAcceptEntry(part)
		switch mt {
		case MarkdownType:
			if q > mdQ {
				mdQ = q
			}
		case "text/html", "*/*":
			if q > htmlQ {
				htmlQ = q
			}
		}
	}
	if mdQ < 0 {
		return false
	}
	if mdQ == 0 {
		return false
	}
	return mdQ >= htmlQ
}

// parseAcceptEntry splits one comma-separated Accept segment into its media
// type (lowercased, parameters other than q dropped) and quality value.
func parseAcceptEntry(part string) (mediaType string, q float64) {
	q = 1.0
	segs := strings.Split(part, ";")
	mediaType = strings.ToLower(strings.TrimSpace(segs[0]))
	for _, p := range segs[1:] {
		p = strings.TrimSpace(p)
		if v, ok := strings.CutPrefix(p, "q="); ok {
			if parsed, err := strconv.ParseFloat(v, 64); err == nil {
				q = parsed
			}
		}
	}
	return mediaType, q
}

// dropTags are removed wholesale (with their subtrees) before rendering —
// chrome and non-content nodes that a "stripped" view has no use for.
var dropTags = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Nav:      true,
	atom.Header:   true,
	atom.Footer:   true,
	atom.Aside:    true,
	atom.Noscript: true,
	atom.Svg:      true,
	atom.Iframe:   true,
	atom.Form:     true,
	atom.Button:   true,
}

// Strip converts an HTML page into its markdown-ish stripped view: chrome
// removed, headings/paragraphs/lists/links/emphasis kept, everything else
// flattened to text. It is a deliberately small converter rather than a
// full CommonMark renderer — good enough for agent consumption, not for
// round-tripping arbitrary HTML.
func Strip(htmlSrc string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	renderNode(&b, doc)
	return collapseBlankLines(b.String()), nil
}

func renderNode(b *strings.Builder, n *html.Node) {
	if n.Type == html.ElementNode && dropTags[n.DataAtom] {
		return
	}
	if n.Type == html.TextNode {
		trimmed := strings.TrimSpace(n.Data)
		if trimmed == "" {
			// Whitespace-only text node (e.g. the newline between two
			// inline siblings). Emit it as a single separating space so
			// e.g. "<strong>a</strong> <em>b</em>" doesn't glue into
			// "**a**_b_" — collapseBlankLines mops up any excess later.
			if n.Data != "" {
				b.WriteString(" ")
			}
			return
		}
		// A leading space in the source has to survive, or it glues onto
		// a preceding inline element's closing delimiter (renderInline
		// trims that element's own trailing space away) — e.g. without
		// this, "<strong>demo</strong> page" renders as "**demo**page".
		if len(strings.TrimLeft(n.Data, " \t\n\r")) != len(n.Data) {
			b.WriteString(" ")
		}
		b.WriteString(trimmed)
		b.WriteString(" ")
		return
	}
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			level := int(n.Data[1] - '0')
			b.WriteString("\n\n" + strings.Repeat("#", level) + " ")
			renderChildren(b, n)
			b.WriteString("\n\n")
			return
		case atom.P, atom.Div, atom.Section, atom.Article, atom.Main:
			b.WriteString("\n\n")
			renderChildren(b, n)
			b.WriteString("\n\n")
			return
		case atom.Br:
			b.WriteString("\n")
			return
		case atom.Li:
			b.WriteString("\n- ")
			renderChildren(b, n)
			return
		case atom.A:
			href := attr(n, "href")
			b.WriteString("[" + renderInline(n) + "](" + href + ")")
			return
		case atom.Strong, atom.B:
			b.WriteString("**" + renderInline(n) + "**")
			return
		case atom.Em, atom.I:
			b.WriteString("_" + renderInline(n) + "_")
			return
		case atom.Code:
			b.WriteString("`" + renderInline(n) + "`")
			return
		case atom.Pre:
			b.WriteString("\n\n```\n")
			renderChildren(b, n)
			b.WriteString("\n```\n\n")
			return
		}
	}
	renderChildren(b, n)
}

func renderChildren(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(b, c)
	}
}

// renderInline renders n's children into their own buffer and trims the
// result, so a wrapping delimiter (**, _, `, [...]) sits directly against
// the content — a space just inside the delimiter stops most markdown
// renderers from recognizing it (e.g. "**world **" isn't parsed as bold).
func renderInline(n *html.Node) string {
	var b strings.Builder
	renderChildren(&b, n)
	return strings.TrimSpace(b.String())
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// collapseBlankLines trims trailing whitespace per line and squashes runs
// of 3+ blank lines (an artifact of nested block elements) down to one.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n") + "\n"
}
