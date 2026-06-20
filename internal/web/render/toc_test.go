package render

import "testing"

func TestExtractTOC(t *testing.T) {
	html := `<h1 id="title">Title</h1>` +
		`<p>intro</p>` +
		`<h2 id="alpha">Alpha <code>x</code></h2>` +
		`<h3 id="beta">Beta</h3>` +
		`<h2>No id here</h2>` +
		`<h2 id="gamma">Gamma</h2>`
	toc := ExtractTOC(html)
	if len(toc) != 3 {
		t.Fatalf("len(toc) = %d, want 3 (h1 and id-less h2 excluded); got %+v", len(toc), toc)
	}
	want := []TOCEntry{
		{Level: 2, ID: "alpha", Text: "Alpha x"},
		{Level: 3, ID: "beta", Text: "Beta"},
		{Level: 2, ID: "gamma", Text: "Gamma"},
	}
	for i, w := range want {
		if toc[i] != w {
			t.Errorf("toc[%d] = %+v, want %+v", i, toc[i], w)
		}
	}
}

func TestExtractTOC_Empty(t *testing.T) {
	if toc := ExtractTOC(""); toc != nil {
		t.Errorf("ExtractTOC(\"\") = %v, want nil", toc)
	}
	if toc := ExtractTOC("<p>no headings</p>"); toc != nil {
		t.Errorf("ExtractTOC(no headings) = %v, want nil", toc)
	}
}
