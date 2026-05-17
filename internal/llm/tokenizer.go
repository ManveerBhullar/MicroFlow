package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type TokenCounter interface {
	Count(text string) int
}

type CharTokenCounter struct{}

func (c *CharTokenCounter) Count(text string) int {
	return utf8.RuneCountInString(text)
}

type OllamaTokenCounter struct {
	BaseURL string
	Client  *http.Client
	mu      sync.Mutex
	cache   map[string]int
}

func NewOllamaTokenCounter(baseURL string) *OllamaTokenCounter {
	return &OllamaTokenCounter{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     60 * time.Second,
			},
		},
		cache: make(map[string]int),
	}
}

func (o *OllamaTokenCounter) Count(text string) int {
	if text == "" {
		return 0
	}

	const maxCacheSize = 256
	o.mu.Lock()
	if v, ok := o.cache[text]; ok {
		o.mu.Unlock()
		return v
	}
	if len(o.cache) >= maxCacheSize {
		o.cache = make(map[string]int, maxCacheSize)
	}
	o.mu.Unlock()

	url := o.BaseURL + "/api/tokenize"
	body := map[string]any{"model": "qwen2.5-coder", "prompt": text}
	data, _ := json.Marshal(body)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return utf8.RuneCountInString(text)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return utf8.RuneCountInString(text)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return utf8.RuneCountInString(text)
	}

	var result struct {
		Tokens []any `json:"tokens"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return utf8.RuneCountInString(text)
	}

	count := len(result.Tokens)
	if count == 0 {
		return utf8.RuneCountInString(text)
	}

	o.mu.Lock()
	o.cache[text] = count
	o.mu.Unlock()

	return count
}

var defaultTokenCounter TokenCounter = &CharTokenCounter{}

func SetTokenCounter(tc TokenCounter) {
	defaultTokenCounter = tc
}

func TokenCount(text string) int {
	return defaultTokenCounter.Count(text)
}

func InitTokenCounter(baseURL string) {
	if strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1") {
		SetTokenCounter(NewOllamaTokenCounter(baseURL))
	} else {
		SetTokenCounter(&CharTokenCounter{})
	}
}

func TrimContextTokens(prefix, suffix string, maxTokens int, maxSuffixPct float64) (string, string) {
	if maxTokens <= 0 {
		return TrimContext(prefix, suffix, 0, maxSuffixPct)
	}

	maxSuffixTokens := int(float64(maxTokens) * maxSuffixPct)
	suffix = TrimToTokenBudget(suffix, maxSuffixTokens, false)
	maxPrefixTokens := maxTokens - TokenCount(suffix)
	prefix = TrimToTokenBudget(prefix, maxPrefixTokens, true)
	return prefix, suffix
}

func TrimToTokenBudget(text string, budget int, keepEnd bool) string {
	if budget <= 0 || text == "" {
		return ""
	}
	if TokenCount(text) <= budget {
		return text
	}

	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		var sample string
		if keepEnd {
			sample = string(runes[len(runes)-mid:])
		} else {
			sample = string(runes[:mid])
		}
		if TokenCount(sample) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	if keepEnd {
		return string(runes[len(runes)-lo:])
	}
	return string(runes[:lo])
}

func FormatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
