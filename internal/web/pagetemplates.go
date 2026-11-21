package web

import (
	"html/template"
	"text/template/parse"
)

// Every page shares layout.html, components.html and parts.html, but each
// uses only a few of their parts. Parsed whole for every page, the shared
// parts took about 40 MB of Roostack's memory (html/template's Clone
// copies them anyway), so a page gets only the parts it calls, and those
// they call in turn, plus any that Go renders from it by name (partsFor).

// partsFor are the shared parts Go renders from a page by name
// (renderPart, renderLive) that the page itself doesn't call.
var partsFor = map[string][]string{
	"settings":        {"activity-status", "activity-log"},
	"settings-cloud":  {"cloud-name-check"},
	"settings-public": {"protection", "relay-stats"},
}

// sharedParts are components.html's and parts.html's templates, parsed
// once and never run: each page gets its own copies of those it uses.
func sharedParts() (map[string]*parse.Tree, error) {
	t, err := template.New("shared").Funcs(templateFuncs()).ParseFS(templateFS, "templates/components.html", "templates/parts.html")
	if err != nil {
		return nil, err
	}
	trees := map[string]*parse.Tree{}
	for _, x := range t.Templates() {
		if x.Tree != nil && x.Name() != "shared" {
			trees[x.Name()] = x.Tree
		}
	}
	return trees, nil
}

// parsePage parses layout.html and a page's own file, then adds the shared
// parts the page reaches.
func parsePage(page string, shared map[string]*parse.Tree) (*template.Template, error) {
	t, err := template.New("layout.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
	if err != nil {
		return nil, err
	}
	var want []string
	for _, x := range t.Templates() {
		if x.Tree != nil {
			want = calls(x.Tree.Root, want)
		}
	}
	want = append(want, partsFor[page]...)
	for len(want) > 0 {
		name := want[len(want)-1]
		want = want[:len(want)-1]
		tree, ok := shared[name]
		if !ok || t.Lookup(name) != nil {
			continue
		}
		if _, err := t.AddParseTree(name, tree.Copy()); err != nil {
			return nil, err
		}
		want = calls(tree.Root, want)
	}
	return t, nil
}

// calls adds the names of the templates a node calls ({{template "x"}}).
func calls(n parse.Node, names []string) []string {
	switch n := n.(type) {
	case *parse.ListNode:
		if n != nil {
			for _, c := range n.Nodes {
				names = calls(c, names)
			}
		}
	case *parse.TemplateNode:
		names = append(names, n.Name)
	case *parse.IfNode:
		names = calls(n.List, calls(n.ElseList, names))
	case *parse.RangeNode:
		names = calls(n.List, calls(n.ElseList, names))
	case *parse.WithNode:
		names = calls(n.List, calls(n.ElseList, names))
	}
	return names
}
