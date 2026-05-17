package llm

import (
	"strings"
	"unicode"
)

type CompletionFilter func(text string, req Request) string

var defaultFilterPipeline = []CompletionFilter{
	stripFences,
	stripLeadingNewlines,
	stripTrailingNewlines,
	deduplicatePartialWord,
	stripSuffixLeak,
	stripRepeatedPrefix,
	truncateRepetition,
	truncateAtStatementBoundary,
}

func ApplyFilters(text string, req Request) string {
	for _, f := range defaultFilterPipeline {
		text = f(text, req)
	}
	return text
}

func stripFences(s string, _ Request) string {
	s = strings.TrimPrefix(s, "```")
	if idx := strings.Index(s, "\n"); idx == 0 {
		s = s[1:]
	}
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimRight(s, "`")
	return s
}

func stripLeadingNewlines(s string, req Request) string {
	if req.MidToken || req.MidLine {
		s = strings.TrimLeft(s, "\r\n")
	}
	return s
}

func stripTrailingNewlines(s string, _ Request) string {
	return strings.TrimRight(s, "\r\n")
}

func deduplicatePartialWord(s string, req Request) string {
	if !req.MidToken || req.PartialWord == "" {
		return s
	}
	lower := strings.ToLower(s)
	partialLower := strings.ToLower(req.PartialWord)
	if strings.HasPrefix(lower, partialLower) && len(s) < 2*len(req.PartialWord) {
		s = s[len(req.PartialWord):]
	}
	return s
}

func stripSuffixLeak(s string, req Request) string {
	if req.Suffix == "" || s == "" {
		return s
	}
	suffixLines := strings.Split(req.Suffix, "\n")
	if len(suffixLines) == 0 {
		return s
	}
	firstSuffixLine := strings.TrimSpace(suffixLines[0])
	if firstSuffixLine == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(firstSuffixLine, trimmed) && len(trimmed) > 3 {
			lines = lines[:i]
			break
		}
		if trimmed == firstSuffixLine {
			lines = lines[:i]
			break
		}
	}
	return strings.Join(lines, "\n")
}

func stripRepeatedPrefix(s string, req Request) string {
	if req.Prefix == "" || s == "" {
		return s
	}
	prefixLines := strings.Split(req.Prefix, "\n")
	if len(prefixLines) == 0 {
		return s
	}
	lastPrefixLine := prefixLines[len(prefixLines)-1]
	if lastPrefixLine == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return s
	}
	if strings.TrimSpace(lines[0]) == strings.TrimSpace(lastPrefixLine) {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func truncateRepetition(s string, _ Request) string {
	if len(s) < 20 {
		return s
	}

	runes := []rune(s)
	windowSizes := []int{6, 8, 10, 16}
	for _, ws := range windowSizes {
		if len(runes) < ws*3 {
			continue
		}
		for start := len(runes) - ws*3; start >= 0 && start < len(runes)-ws*3; start++ {
			pattern := runes[start : start+ws]
			matches := 0
			for pos := start; pos+ws <= len(runes); pos += ws {
				match := true
				for k := 0; k < ws; k++ {
					if runes[pos+k] != pattern[k] {
						match = false
						break
					}
				}
				if match {
					matches++
				} else {
					break
				}
			}
			if matches >= 4 {
				cutoff := start + ws*2
				if cutoff < len(runes) {
					return string(runes[:cutoff])
				}
			}
		}
	}
	return s
}

func truncateAtStatementBoundary(s string, _ Request) string {
	if len(s) < 10 {
		return s
	}

	runes := []rune(s)
	if len(runes) > 512 {
		lastSemicolon := -1
		for i := len(runes) - 1; i >= len(runes)/2; i-- {
			r := runes[i]
			if r == ';' || r == '}' || r == '\n' {
				if r == '\n' && i > 0 && (runes[i-1] == '{' || runes[i-1] == '}' || runes[i-1] == ';') {
					lastSemicolon = i
				} else if r != '\n' {
					lastSemicolon = i
				}
			}
		}
		if lastSemicolon > 0 {
			return string(runes[:lastSemicolon+1])
		}
	}
	return s
}

func looksLikeDiagnostic(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return (strings.Contains(lower, "expected ") && strings.Contains(lower, "found ")) ||
		strings.Contains(lower, "syntax error") ||
		strings.Contains(lower, "parser error") ||
		strings.Contains(lower, "compiler error") ||
		strings.Contains(lower, "type error") ||
		strings.Contains(lower, "undefined variable") ||
		(isAllProse(lower) && len(lower) > 50 && !strings.ContainsAny(lower, "{}()[];=<>"))
}

func isAllProse(s string) bool {
	words := 0
	total := 0
	for _, r := range s {
		if unicode.IsLetter(r) || r == ' ' || r == '.' || r == ',' || r == '!' || r == '?' || r == '\'' || r == '"' {
			total++
			if unicode.IsLetter(r) {
				words++
			}
		}
	}
	if total == 0 {
		return false
	}
	return float64(words)/float64(total) > 0.7
}

func ScoreCompletion(text string, req Request) float64 {
	if text == "" {
		return 0
	}

	score := 0.5

	lines := strings.Split(text, "\n")
	if len(lines) <= 3 {
		score += 0.2
	} else if len(lines) <= 8 {
		score += 0.1
	}

	if req.MidLine && !strings.HasPrefix(text, "\n") {
		score += 0.1
	}

	if looksLikeDiagnostic(text) {
		score -= 0.6
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}

	uniqueRunes := make(map[rune]bool)
	for _, r := range trimmed {
		uniqueRunes[r] = true
	}
	diversity := float64(len(uniqueRunes)) / float64(len([]rune(trimmed)))
	if diversity < 0.08 && len(trimmed) > 40 {
		score -= 0.3
	}

	if len(text) > 200 {
		half := len(text) / 2
		beginning := text[:half]
		end := text[half:]
		beginningLast := beginning[len(beginning)-30:]
		if len(beginning) < 30 {
			beginningLast = beginning
		}
		endLen := 30
		if len(end) < 30 {
			endLen = len(end)
		}
		endFirst := end[:endLen]
		if beginningLast == endFirst {
			score -= 0.4
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}
