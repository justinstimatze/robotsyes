package negotiate

import (
	"strings"
	"testing"
)

func TestWantsMarkdown(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   bool
	}{
		{"empty", "", false},
		{"markdown only", "text/markdown", true},
		{"html only", "text/html", false},
		{"markdown preferred over html", "text/markdown;q=0.9,text/html;q=0.5", true},
		{"html preferred over markdown", "text/markdown;q=0.3,text/html;q=0.9", false},
		{"equal q ties to markdown", "text/html,text/markdown", true},
		{"markdown explicitly zero", "text/markdown;q=0,text/html", false},
		{"wildcard only, no markdown", "*/*", false},
		{"markdown beats wildcard", "text/markdown,*/*;q=0.1", true},
		{"case insensitive", "TEXT/MARKDOWN", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WantsMarkdown(c.accept); got != c.want {
				t.Errorf("WantsMarkdown(%q) = %v, want %v", c.accept, got, c.want)
			}
		})
	}
}

func TestExpressesNoPreference(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		want   bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"bare wildcard", "*/*", true},
		{"wildcard with q", "*/*;q=0.8", true},
		{"repeated wildcard", "*/*, */*", true},
		{"explicit html", "text/html", false},
		{"explicit markdown", "text/markdown", false},
		{"non-html non-markdown", "application/json", false},
		{"wildcard plus explicit", "*/*,text/html;q=0.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExpressesNoPreference(c.accept); got != c.want {
				t.Errorf("ExpressesNoPreference(%q) = %v, want %v", c.accept, got, c.want)
			}
		})
	}
}

func TestStrip(t *testing.T) {
	html := `<!DOCTYPE html>
<html><head><style>body{color:red}</style><script>track()</script></head>
<body>
<nav>Home | About</nav>
<header>Site Header</header>
<main>
<h1>Title</h1>
<p>Hello <strong>world</strong>, this is <a href="/x">a link</a>.</p>
<ul><li>one</li><li>two</li></ul>
</main>
<footer>Site Footer</footer>
</body></html>`

	got, err := Strip(html)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}

	mustContain := []string{"# Title", "**world**", "[a link](/x)", "- one", "- two"}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("Strip output missing %q; got:\n%s", want, got)
		}
	}

	mustNotContain := []string{"track()", "color:red", "Home | About", "Site Header", "Site Footer"}
	for _, unwanted := range mustNotContain {
		if strings.Contains(got, unwanted) {
			t.Errorf("Strip output should not contain %q; got:\n%s", unwanted, got)
		}
	}
}

func TestStripPreservesSpacingAroundInlineElements(t *testing.T) {
	got, err := Strip(`<p>This is a <strong>demo</strong> page for the <a href="/about">about</a> section.</p>`)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	want := "This is a **demo** page for the [about](/about) section."
	got = strings.TrimSpace(got)
	if got != want {
		t.Errorf("Strip() = %q, want %q", got, want)
	}
}

func TestStripCollapsesBlankRuns(t *testing.T) {
	got, err := Strip(`<div><div><div><p>a</p></div></div><p>b</p></div>`)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("Strip output has a run of 3+ blank lines:\n%q", got)
	}
}
