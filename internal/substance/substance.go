// Package substance scores how substantive a prompt is, ported from Tally's text features.
//
// Relevance is the share (per mille) of a prompt's distinct tokens that already appear in the
// project's own files: "fix the retry loop in syncer.Flush" scores high, "continue" scores 0.
// Everything is computed locally; only the number is recorded, never the text.
package substance

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// TrivialChars and TrivialRelevance match Tally's ruleset: a shorter or less relevant
	// prompt is "try again"-grade and earns only a small attention allowance.
	TrivialChars     = 20
	TrivialRelevance = 150
	// SubstantiveRelevance is the bar for think-time credit before a prompt.
	SubstantiveRelevance = 500

	vocabMax    = 50000
	maxFiles    = 300
	maxFileSize = 64 << 10
)

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("the and for that this with you are not but have was can all any its from they will would there their what about which when make like just into than then them these some could other only also been") {
		stop[w] = true
	}
}

// Tokens lowercases text and returns runs of [a-z0-9_] of length >= 3 that do not start with
// a digit, minus stopwords. Non-ASCII characters are separators.
func Tokens(text string) []string {
	var out []string
	for _, run := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	}) {
		run = strings.TrimLeft(run, "0123456789")
		if len(run) >= 3 && !stop[run] {
			out = append(out, run)
		}
	}
	return out
}

// Vocab is a set of tokens.
type Vocab map[string]struct{}

// Add inserts the tokens of text, stopping at the size cap.
func (v Vocab) Add(text string) {
	for _, t := range Tokens(text) {
		if len(v) >= vocabMax {
			return
		}
		v[t] = struct{}{}
	}
}

// Relevance returns 0..1000: the per-mille of distinct prompt tokens found in v.
func Relevance(prompt string, v Vocab) int {
	seen := map[string]bool{}
	hit := 0
	for _, t := range Tokens(prompt) {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, ok := v[t]; ok {
			hit++
		}
	}
	if len(seen) == 0 {
		return 0
	}
	return 1000 * hit / len(seen)
}

// Trivial reports whether a prompt of n characters with the given relevance is "try again"-grade.
// rel < 0 means relevance is unknown, so only length decides.
func Trivial(n, rel int) bool {
	return n < TrivialChars || (rel >= 0 && rel < TrivialRelevance)
}

// ProjectVocab builds a vocabulary from the most recently modified tracked text files under
// cwd, within budget. It returns nil when cwd is not a git repo or nothing could be read.
func ProjectVocab(cwd string, budget time.Duration) Vocab {
	if cwd == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		full := filepath.Join(cwd, string(p))
		if st, err := os.Stat(full); err == nil && st.Mode().IsRegular() && st.Size() <= maxFileSize {
			files = append(files, f{full, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	v := Vocab{}
	for i, fl := range files {
		if i >= maxFiles || ctx.Err() != nil || len(v) >= vocabMax {
			break
		}
		b, err := os.ReadFile(fl.path)
		if err != nil || bytes.IndexByte(b, 0) >= 0 {
			continue // unreadable or binary
		}
		v.Add(string(b))
		v.Add(filepath.ToSlash(strings.TrimPrefix(fl.path, cwd))) // file names count as vocabulary
	}
	if len(v) == 0 {
		return nil
	}
	return v
}
