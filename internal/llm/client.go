package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20

type Request struct {
	Prefix       string
	Suffix       string
	Instruction  string
	Selection    string
	Task         string
	FileName     string
	FileType     string
	PromptMode   string
	Model        string
	BaseURL      string
	APIKey       string
	MaxToks      int
	Timeout      int
	MidLine      bool
	MidToken     bool
	PartialWord  string
	RequestStart time.Time
}

var defaultClient *http.Client

func init() {
	defaultClient = &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        4,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

func Complete(ctx context.Context, req Request) (string, error) {
	return completeWithRetry(ctx, req, 2)
}

func completeWithRetry(ctx context.Context, req Request, maxRetries int) (string, error) {
	userContent := buildPrompt(req)
	systemPrompt := buildSystemPrompt(req)

	body := map[string]any{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"max_tokens":  req.MaxToks,
		"temperature": 0.1,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	client := *defaultClient
	client.Timeout = time.Duration(timeout) * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(req.BaseURL, "/")+"/chat/completions",
			bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if req.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			if isTransientError(err) && attempt < maxRetries {
				continue
			}
			return "", err
		}

		result, err := parseResponse(resp, req)
		resp.Body.Close()
		if err != nil {
			if resp.StatusCode >= 500 && attempt < maxRetries {
				lastErr = err
				continue
			}
			return "", err
		}
		return result, nil
	}
	return "", lastErr
}

func buildSystemPrompt(req Request) string {
	if strings.TrimSpace(req.Instruction) != "" {
		if strings.EqualFold(req.Task, "edit") {
			return "You are a code editor inside a terminal editor chat mode. Apply the user's instruction to the provided code. Return only the complete replacement text for the requested scope. Do not explain. Do not use markdown fences. Do not include diagnostics."
		}
		return "You are an inline code assistant inside a programmer's editor. Follow the user's instruction and produce code to insert at the cursor. Return only the text to insert. Do not explain. Do not use markdown fences."
	}

	base := `You are an inline text completion engine inside a programmer's editor.
The user is typing and you must continue from exactly where the cursor is.
Return ONLY the text to insert at the <CURSOR> position.
Do not explain. Do not use markdown fences. Do not return diagnostics.
Do not repeat text before or after the cursor.`

	if req.MidToken {
		base += fmt.Sprintf("\n\nCRITICAL: The cursor is in the middle of the word \"%s\". You MUST continue that word on the same line. Do NOT start a new line. Do NOT repeat the partial word.", req.PartialWord)
	} else if req.MidLine {
		base += "\n\nThe cursor is in the middle of a line. Continue on the same line. Do not start a new line unless the current line clearly ends with a complete statement."
	}

	return base
}

func parseResponse(resp *http.Response, req Request) (string, error) {
	respData, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return "", err
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := result.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(respData))
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("llm: http %d: %s", resp.StatusCode, msg)
	}
	if result.Error.Message != "" {
		return "", fmt.Errorf("llm: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices returned")
	}
	raw := result.Choices[0].Message.Content
	if raw == "" {
		raw = result.Choices[0].Text
	}
	if raw == "" {
		raw = result.Choices[0].Message.ReasoningContent
	}

	completion := ApplyFilters(raw, req)

	if looksLikeDiagnostic(completion) {
		return "", fmt.Errorf("llm: model returned a diagnostic instead of completion: %s", completion)
	}
	return completion, nil
}

func isTransientError(err error) bool {
	if netErr, ok := err.(net.Error); ok {
		return netErr.Timeout() || netErr.Temporary()
	}
	return strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "EOF") ||
		strings.Contains(err.Error(), "reset")
}

func buildPrompt(req Request) string {
	metadata := fmt.Sprintf("File: %s (%s)", req.FileName, req.FileType)

	if strings.EqualFold(req.PromptMode, "fim") {
		return fmt.Sprintf("%s\n<|fim_prefix|>%s<|fim_suffix|>%s<|fim_middle|>", metadata, req.Prefix, req.Suffix)
	}

	if strings.TrimSpace(req.Instruction) != "" {
		return buildInstructionPrompt(req)
	}

	var cursorHint string
	if req.MidToken {
		cursorHint = fmt.Sprintf("\n[CURSOR IS MID-WORD: \"%s\" — continue this word, do not start a new line]", req.PartialWord)
	} else if req.MidLine {
		cursorHint = "\n[CURSOR IS MID-LINE — continue on the same line]"
	}

	prefix := req.Prefix
	suffix := req.Suffix
	if suffix == "" {
		suffix = "(empty)"
	}

	return fmt.Sprintf(`Complete the text at the cursor position.%s

%s

`+"```"+`
%s
`+"```"+`<CURSOR>
`+"```"+`
%s
`+"```"+`

## Completion:`, cursorHint, metadata, prefix, suffix)
}

func buildInstructionPrompt(req Request) string {
	metadata := fmt.Sprintf("File: %s (%s)", req.FileName, req.FileType)
	return fmt.Sprintf("Follow the user instruction and write code to insert at the cursor. Return inserted text only.\n\n%s\n\nInstruction:\n%s\n\nBefore cursor:\n%s\n\nAfter cursor:\n%s\n\nInserted text only:", metadata, req.Instruction, req.Prefix, req.Suffix)
}

func buildEditPrompt(req Request) string {
	metadata := fmt.Sprintf("File: %s (%s)", req.FileName, req.FileType)
	if req.Selection != "" {
		return fmt.Sprintf("Apply the instruction to the selected code. Return the replacement for the selection only.\n\n%s\n\nInstruction:\n%s\n\nBefore selection:\n%s\n\nSelected code:\n%s\n\nAfter selection:\n%s\n\nReplacement selection only:", metadata, req.Instruction, req.Prefix, req.Selection, req.Suffix)
	}
	return fmt.Sprintf("Apply the instruction to the full file. Return the complete replacement file only.\n\n%s\n\nInstruction:\n%s\n\nFull file:\n%s\n\nComplete replacement file only:", metadata, req.Instruction, req.Prefix)
}

var fencePrefixRe = regexp.MustCompile("(?s)^```[a-zA-Z0-9+-]*\n?")

func cleanCompletion(s string, req Request) string {
	return ApplyFilters(s, req)
}
