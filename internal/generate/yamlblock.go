package generate

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// A YAML block (SPEC.md §8.1, "A whole file, or a block at the end of the
// app's own"): YAML refuses a key given twice, so Roostack's lines can't
// follow the app's. Instead each key the block names is set in the app's
// file, marked with YAMLMark: a mapping in both is merged key by key,
// anything else replaced. Keys marked before that the block no longer
// names are removed, so turning a setting off (single sign-on) puts the
// app's own defaults back. The rest of the file, comments included, is
// left as it is, though written again in a standard layout when a key
// changes.

// YAMLMark is the comment on each key Roostack has set in an app's YAML
// file.
const YAMLMark = "set by Roostack"

// yamlBlockHeader starts a YAML block as the pages show it.
const yamlBlockHeader = "Roostack sets these keys in the app's own file, each marked \"# " + YAMLMark + "\", and will set them again if they're changed."

// setYAMLKeys is text with block's keys set in it. ok is false if text
// isn't a YAML mapping, which Roostack then leaves alone.
func setYAMLKeys(text, block string) (out string, ok bool) {
	doc, ok := yamlMapping(text)
	if !ok {
		return text, false
	}
	want, ok := yamlMapping(block)
	if !ok {
		return text, false
	}
	before, err := encodeYAML(doc)
	if err != nil {
		return text, false
	}
	m := doc.Content[0]
	mergeMarked(m, want.Content[0])
	stripMarked(m, want.Content[0])
	after, err := encodeYAML(doc)
	if err != nil {
		return text, false
	}
	if after == before {
		// Nothing to change: the file stays exactly as the app wrote it.
		return text, true
	}
	if len(m.Content) == 0 {
		return "", true
	}
	return after, true
}

// yamlMapping parses text as a document holding a mapping (an empty one
// for an empty text).
func yamlMapping(text string) (*yaml.Node, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, false
	}
	if doc.Kind == 0 {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}, true
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false
	}
	return &doc, true
}

func encodeYAML(doc *yaml.Node) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	return b.String(), enc.Close()
}

// marked says whether a key is one Roostack set: the mark is on the key,
// or on its value when that's on the same line (as YAML reads it back).
func marked(key, value *yaml.Node) bool {
	is := func(n *yaml.Node) bool { return strings.TrimSpace(strings.TrimPrefix(n.LineComment, "#")) == YAMLMark }
	return is(key) || value.Kind == yaml.ScalarNode && is(value)
}

// stripMarked removes the keys Roostack set from a mapping, and from the
// mappings in it, that want (nil: none) no longer names, so the ones it
// still sets keep their places.
func stripMarked(m, want *yaml.Node) {
	var kept []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		w := mappingValue(want, k.Value)
		if marked(k, v) && w == nil {
			continue
		}
		if v.Kind == yaml.MappingNode {
			if w != nil && w.Kind != yaml.MappingNode {
				w = nil
			}
			stripMarked(v, w)
		}
		kept = append(kept, k, v)
	}
	m.Content = kept
}

// mappingValue is key's value in mapping m (nil if m is nil or hasn't
// it).
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mergeMarked sets want's keys in m, marking each one it sets.
func mergeMarked(m, want *yaml.Node) {
	for i := 0; i+1 < len(want.Content); i += 2 {
		k, v := want.Content[i], want.Content[i+1]
		j := -1
		for n := 0; n+1 < len(m.Content); n += 2 {
			if m.Content[n].Value == k.Value {
				j = n
				break
			}
		}
		switch {
		case j >= 0 && !marked(m.Content[j], m.Content[j+1]) && m.Content[j+1].Kind == yaml.MappingNode && v.Kind == yaml.MappingNode:
			// The app's own mapping: Roostack sets keys inside it. One
			// Roostack added is replaced whole.
			mergeMarked(m.Content[j+1], v)
		case j >= 0:
			m.Content[j].LineComment = "# " + YAMLMark
			v.LineComment = ""
			m.Content[j+1] = v
		default:
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k.Value, LineComment: "# " + YAMLMark}
			m.Content = append(m.Content, key, v)
		}
	}
}
