package llm

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/micro-editor/micro/v2/internal/buffer"
)

type CursorContext struct {
	Prefix      string
	Suffix      string
	MidLine     bool
	MidToken    bool
	PartialWord string
}

func BuildFIMContext(b *buffer.Buffer, nLines int) CursorContext {
	if nLines < 0 {
		nLines = 0
	}

	c := b.GetActiveCursor()
	curY := c.Y
	curX := c.X

	startY := curY - nLines
	if startY < 0 {
		startY = 0
	}

	var prefixParts []string
	for y := startY; y < curY; y++ {
		prefixParts = append(prefixParts, string(b.LineBytes(y)))
	}

	curLine := []rune(string(b.LineBytes(curY)))
	if curX > len(curLine) {
		curX = len(curLine)
	}
	beforeCursor := string(curLine[:curX])
	afterCursor := string(curLine[curX:])
	prefixParts = append(prefixParts, beforeCursor)
	prefix := strings.Join(prefixParts, "\n")

	suffix := afterCursor
	endY := curY + nLines/2
	if endY >= b.LinesNum() {
		endY = b.LinesNum() - 1
	}
	for y := curY + 1; y <= endY; y++ {
		suffix += "\n" + string(b.LineBytes(y))
	}

	midLine := len(beforeCursor) > 0
	midToken := false
	partialWord := ""
	if midLine {
		runes := []rune(beforeCursor)
		lastR := runes[len(runes)-1]
		if unicode.IsLetter(lastR) || unicode.IsDigit(lastR) || lastR == '_' {
			midToken = true
			end := len(runes)
			start := end
			for start > 0 {
				r := runes[start-1]
				if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
					break
				}
				start--
			}
			partialWord = string(runes[start:end])
		}
	}

	return CursorContext{
		Prefix:      prefix,
		Suffix:      suffix,
		MidLine:     midLine,
		MidToken:    midToken,
		PartialWord: partialWord,
	}
}

func TrimContext(prefix, suffix string, maxChars int, maxSuffixPercentage float64) (string, string) {
	if maxChars <= 0 {
		return prefix, suffix
	}
	if maxSuffixPercentage < 0 {
		maxSuffixPercentage = 0
	}
	if maxSuffixPercentage > 1 {
		maxSuffixPercentage = 1
	}

	maxSuffix := int(float64(maxChars) * maxSuffixPercentage)
	suffix = firstRunes(suffix, maxSuffix)

	maxPrefix := maxChars - utf8.RuneCountInString(suffix)
	if maxPrefix < 0 {
		maxPrefix = 0
	}
	prefix = lastRunes(prefix, maxPrefix)
	return prefix, suffix
}

func firstRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func lastRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
