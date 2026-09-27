package web

import "testing"

func TestFormatHTML(t *testing.T) {
	in := `<ul class="rows"><li class="row-split"><span><strong>mum</strong><br><small>Owner</small></span><a href="#">Change…</a></li></ul><pre class="output">a
  b</pre>`
	want := `<ul class="rows">
  <li class="row-split"><span><strong>mum</strong><br><small>Owner</small></span><a href="#">Change…</a></li>
</ul>
<pre class="output">a
  b</pre>`
	if got := formatHTML(in); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
