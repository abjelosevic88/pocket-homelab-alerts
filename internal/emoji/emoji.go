// Package emoji turns ntfy-style tags into emoji: a tag that is an emoji
// short code ("warning", "white_check_mark") shows as the emoji, every other
// tag stays a plain label — what the ntfy apps do. The short codes are
// GitHub's gemoji list (MIT), as ntfy ships it.
package emoji

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed emojis.json
var raw []byte

var (
	once  sync.Once
	table map[string]string
)

func load() {
	table = map[string]string{}
	_ = json.Unmarshal(raw, &table)
}

// Of returns the emoji for a short code, or "".
func Of(tag string) string {
	once.Do(load)
	return table[strings.ToLower(strings.Trim(strings.TrimSpace(tag), ":"))]
}

// Split separates tags into the emoji they stand for and plain labels.
func Split(tags []string) (string, []string) {
	var b strings.Builder
	labels := []string{}
	for _, t := range tags {
		if e := Of(t); e != "" {
			b.WriteString(e)
		} else if t = strings.TrimSpace(t); t != "" {
			labels = append(labels, t)
		}
	}
	return b.String(), labels
}
