package rag

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var blankLine = regexp.MustCompile(`\n[ \t\r]*\n`)

// unit is one piece of text to pack into chunks: a paragraph when it fits,
// otherwise one of its sentences. para marks the first unit of a paragraph
// so the paragraph break survives in the chunk.
type unit struct {
	text string
	para bool
}

// Chunk splits text into pieces of about size runes for embedding. It
// splits on blank lines first, then on sentence ends for paragraphs longer
// than size, then packs the pieces into chunks that do not exceed size.
// Consecutive chunks share up to overlap runes of trailing context so a
// fact straddling a boundary is still found. A sentence longer than size
// is cut at the last space before size, or at size when there is none.
//
// The result never contains an empty string; empty or whitespace input
// yields nil. size <= 0 or input shorter than size returns the trimmed
// text as a single chunk.
func Chunk(text string, size, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if size <= 0 || utf8.RuneCountInString(text) <= size {
		return []string{text}
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size / 4
	}

	units := splitUnits(text, size)
	var chunks []string
	var cur []unit
	curLen := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		if s := joinUnits(cur); s != "" {
			chunks = append(chunks, s)
		}
	}
	for _, u := range units {
		ul := utf8.RuneCountInString(u.text)
		if curLen > 0 && curLen+1+ul > size {
			flush()
			// Carry trailing units into the next chunk as overlap, but only
			// while the new chunk (overlap plus this unit) still fits.
			var keep []unit
			keepLen := 0
			for i := len(cur) - 1; i >= 0; i-- {
				cand := utf8.RuneCountInString(cur[i].text)
				if keepLen > 0 {
					cand += keepLen + 1
				}
				if cand > overlap || cand+1+ul > size {
					break
				}
				keep = append([]unit{cur[i]}, keep...)
				keepLen = cand
			}
			cur = keep
			curLen = keepLen
		}
		if curLen == 0 {
			u.para = false
			curLen = ul
		} else {
			curLen += 1 + ul
		}
		cur = append(cur, u)
	}
	flush()
	return chunks
}

// joinUnits renders a chunk: paragraph starts are separated by a blank
// line, sentences by a space.
func joinUnits(units []unit) string {
	var b strings.Builder
	for i, u := range units {
		if i > 0 {
			if u.para {
				b.WriteString("\n\n")
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(u.text)
	}
	return strings.TrimSpace(b.String())
}

// splitUnits turns text into units no longer than size runes.
func splitUnits(text string, size int) []unit {
	var units []unit
	for _, para := range blankLine.Split(text, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		first := true
		for _, s := range splitSentences(para) {
			for _, piece := range hardSplit(s, size) {
				units = append(units, unit{text: piece, para: first})
				first = false
			}
		}
	}
	return units
}

// splitSentences splits a paragraph at sentence ends: a '.', '!', or '?'
// (optionally followed by closing quotes or brackets) that is followed by
// whitespace or the end of the text, and at every line break.
func splitSentences(para string) []string {
	var out []string
	runes := []rune(para)
	start := 0
	emit := func(end int) {
		if s := strings.TrimSpace(string(runes[start:end])); s != "" {
			out = append(out, s)
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' {
			emit(i)
			start = i + 1
			continue
		}
		if !isSentenceEnd(r) {
			continue
		}
		j := i + 1
		for j < len(runes) && isCloser(runes[j]) {
			j++
		}
		if j >= len(runes) || unicode.IsSpace(runes[j]) {
			emit(j)
			i = j - 1
		}
	}
	emit(len(runes))
	return out
}

func isCloser(r rune) bool {
	return r == '"' || r == '\'' || r == ')' || r == ']'
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// hardSplit cuts s into pieces of at most size runes, preferring the last
// space in the window.
func hardSplit(s string, size int) []string {
	if utf8.RuneCountInString(s) <= size {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for len(runes) > size {
		cut := size
		for i := size; i > size/2; i-- {
			if unicode.IsSpace(runes[i]) {
				cut = i
				break
			}
		}
		piece := strings.TrimSpace(string(runes[:cut]))
		if piece != "" {
			out = append(out, piece)
		}
		runes = []rune(strings.TrimLeftFunc(string(runes[cut:]), unicode.IsSpace))
	}
	if rest := strings.TrimSpace(string(runes)); rest != "" {
		out = append(out, rest)
	}
	return out
}
