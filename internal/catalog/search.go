package catalog

import (
	"sort"
	"strings"
	"unicode"
)

// Split identifier casing and punctuation, keep alphanumerics and fold common
// plurals. No network calls, embeddings, or nondeterministic tie-breaking.
func tokens(s string) []string {
	var b strings.Builder
	var prev rune
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			b.WriteByte(' ')
		} else {
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
		}
		prev = r
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Fields(b.String()) {
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			t = strings.TrimSuffix(t, "s")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func (c *Catalog) indexOperation(op *Operation) {
	add := func(value string, weight int) {
		for _, term := range tokens(value) {
			if c.index[term] == nil {
				c.index[term] = map[string]int{}
			}
			c.index[term][op.ID] += weight
		}
	}
	add(op.ID, 8)
	add(op.Path, 6)
	add(op.Method, 2)
	add(strings.Join(op.Tags, " "), 5)
	add(op.Summary, 4)
	add(op.Description, 1)
	var names []string
	for _, p := range op.Parameters {
		names = append(names, p.Name)
	}
	add(strings.Join(names, " "), 3)
}

func (c *Catalog) Search(query string) []Operation {
	score := map[string]int{}
	if _, ok := c.Lookup(query); ok {
		score[query] = 1000
	}
	for _, term := range tokens(clip(query, 1024)) {
		for id, w := range c.index[term] {
			if _, ok := c.Lookup(id); ok {
				score[id] += w
			}
		}
	}
	ids := make([]string, 0, len(score))
	for id := range score {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if score[ids[i]] != score[ids[j]] {
			return score[ids[i]] > score[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > 8 {
		ids = ids[:8]
	}
	result := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op := *c.ops[id]
		op.Description = clip(op.Description, 768)
		op.Summary = clip(op.Summary, 384)
		op.Tags = nil
		for i, t := range c.ops[id].Tags {
			if i == 12 {
				break
			}
			op.Tags = append(op.Tags, clip(t, 96))
		}
		result = append(result, op)
	}
	return result
}
