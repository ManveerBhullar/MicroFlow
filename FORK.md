# MicroFlow Fork Notes

MicroFlow is a fork of [micro](https://github.com/micro-editor/micro) focused on
local-first AI-assisted terminal editing.

## Current Fork Goals

- Keep micro's terminal-native editing model and keybinding system.
- Add automatic inline code suggestions with local models.
- Keep inline suggestions separate from natural-language chat edits.
- Default to Ollama's OpenAI-compatible endpoint at `http://localhost:11434/v1`.
- Prefer coder/autocomplete models such as `qwen2.5-coder:7b`.
- Keep project context small and predictable until same-file suggestions are stable.

## LLM Defaults

```text
llm.enabled true
llm.autosuggest true
llm.baseurl http://localhost:11434/v1
llm.model qwen2.5-coder:7b
llm.promptmode chat
llm.contextlines 120
llm.maxinputchars 4096
llm.maxsuffixpercentage 0.35
llm.maxtokens 128
llm.edittokens 4096
llm.debounce 500
llm.timeout 120
llm.latencybudget 5000
```

Useful commands:

```text
show llm.model
set llm.model qwen2.5-coder:7b
set llm.debug true
set llm.autosuggest false
llm-stats
```

## AI UX Model

MicroFlow has two AI modes:

- Inline completion: automatic ghost text appears while typing. `Tab` accepts all
  ghost text. `Ctrl-Right` accepts one word at a time (partial accept). `Esc`
  dismisses. `Alt-Tab` or `Ctrl-L` explicitly requests one completion.
- Chat edit: `CtrlSpace` or `CtrlI` opens a natural-language edit prompt. With a
  selection, the selected text is replaced. Without a selection, MicroFlow asks
  before applying the edit to the whole file. The command bar equivalent is
  `llm`; use `llm file <instruction>` to intentionally edit the whole file.

The older insert-at-cursor behavior remains available as `llm-insert`.

Quality gate: auto-suggestions scoring below 0.2 (on a 0-1 scale) are silently
discarded. Latency budget: auto-suggestions arriving after `llm.latencybudget` ms
(default 5000) are discarded. Use `:llm-stats` to see session metrics.

---

## Prompt Engineering Design

This section documents exactly how prompts are built so the approach can be
compared against best practices and iterated on.

### Architecture Overview

```
                context.go         tokenizer.go       client.go           postprocess.go
              ┌────────────┐    ┌──────────────┐  ┌──────────────┐   ┌──────────────────┐
  Buffer +    │            │    │ TokenCounter │  │              │   │ CompletionFilter  │
  Cursor ────►│ BuildFIM   │───►│ TrimToToken  │─►│ buildSystem  │──►│ pipeline:         │
              │ Context    │    │ Budget       │  │ Prompt       │   │  stripFences      │
              │            │    │              │  │              │   │  stripNewlines    │
              └────────────┘    └──────────────┘  │ buildPrompt  │   │  dedupPartial     │
                                                  │ (code-fenced)│   │  stripSuffixLeak  │
                                                  │              │   │  stripRepeated    │
                                                  │ parseResponse│   │  truncateRepeat   │
                                                  │              │   │  truncateBoundary │
                                                  └──────────────┘   └──────────────────┘
                                                        │                     │
                                                        ▼                     ▼
                                                   ScoreCompletion()    metrics.go
                                                   (quality gate)      RecordCompletion()
```

### Step 1: Context Extraction (`internal/llm/context.go`)

`BuildFIMContext(buffer, nLines)` produces a `CursorContext`:

| Field | Description | Example (cursor at `func unimp|`) |
|---|---|---|
| `Prefix` | All lines above cursor + current line up to cursor | `"package main\n\nfunc unimp"` |
| `Suffix` | Rest of current line after cursor + lines below | `""` (empty in this case) |
| `MidLine` | True if cursor is not at start of an empty line | `true` |
| `MidToken` | True if char before cursor is a word char (letter, digit, `_`) | `true` |
| `PartialWord` | The incomplete word at cursor position | `"unimp"` |

**MidToken detection logic:**
1. Get the rune immediately before the cursor
2. If it's a letter, digit, or underscore → `MidToken = true`
3. Walk backwards from cursor to find the full partial word (contiguous
   word characters)
4. Example: `func unimp|` → last rune is `p` → walk back → `"unimp"`

### Step 2: Context Trimming (`llm.TrimContext`)

The raw prefix/suffix are trimmed to fit within a character budget:

- `maxChars` = `llm.maxinputchars` (default 4096)
- Suffix gets `maxSuffixPercentage * maxChars` chars (from the **start** of suffix)
- Prefix gets the remainder (from the **end** of prefix, keeping text nearest cursor)

### Step 3: System Prompt (`buildSystemPrompt`)

The system prompt varies by mode:

#### Ghost completion (no instruction)

Base:
```
You are an inline text completion engine inside a programmer's editor.
The user is typing and you must continue from exactly where the cursor is.
Return ONLY the text to insert at the cursor position.
Do not explain. Do not use markdown. Do not return diagnostics.
Do not repeat text before or after the cursor.
```

Plus cursor-position-specific hints:

| Condition | Appended |
|---|---|
| `MidToken = true` | `CRITICAL: The cursor is in the middle of the word "{PartialWord}". You MUST continue that word on the same line. Do NOT start a new line. Do NOT repeat the partial word.` |
| `MidLine = true` (not mid-token) | `The cursor is in the middle of a line. Continue on the same line. Do not start a new line unless the current line clearly ends with a complete statement.` |

#### Instruction mode (insert-at-cursor)

```
You are an inline code assistant inside a programmer's editor.
Follow the user's instruction and produce code to insert at the cursor.
Return only the text to insert. Do not explain. Do not use markdown fences.
```

#### Edit mode (selection/file replacement)

```
You are a code editor inside a terminal editor chat mode.
Apply the user's instruction to the provided code.
Return only the complete replacement text for the requested scope.
Do not explain. Do not use markdown fences. Do not include diagnostics.
```

### Step 4: User Prompt (`buildPrompt`)

#### FIM mode (`llm.promptmode = "fim"`)

Uses standard Fill-In-the-Middle tokens:
```
{metadata}
<|fim_prefix|>{Prefix}<|fim_suffix|>{Suffix}<|fim_middle|>
```

This is the preferred mode for models that natively support FIM (e.g. Qwen2.5-Coder,
CodeLlama, StarCoder). The model only needs to predict the `<|fim_middle|>` section.

#### Chat mode (`llm.promptmode = "chat"` — default)

Code-fenced structured prompt:
```
Complete the text at the cursor position.{cursorHint}

File: {path} ({filetype})

```{Prefix}```<CURSOR>
```{Suffix}```

## Completion:
```

Code fences give the model clear boundary markers for prefix/suffix,
reducing leakage. The `## Completion:` heading signals where output begins.

### Step 5: Post-Processing (`ApplyFilters` in `postprocess.go`)

Raw model output goes through a composable `CompletionFilter` pipeline:

| # | Filter | What | Why |
|---|---|---|---|
| 1 | `stripFences` | Remove markdown code fences from start/end | Models wrap code despite instructions |
| 2 | `stripLeadingNewlines` | Trim `\r\n` from start if mid-line/mid-token | Model starts with newline despite hints |
| 3 | `stripTrailingNewlines` | Trim `\r\n` from end | Avoids extra blank lines |
| 4 | `deduplicatePartialWord` | Strip repeated `PartialWord` prefix with length guard | Prefix=`"unimp"`, model=`"implemented()"` → `"lemented()"` |
| 5 | `stripSuffixLeak` | Remove trailing lines that match suffix content | Model sometimes echos suffix |
| 6 | `stripRepeatedPrefix` | Remove leading line that matches last prefix line | Model sometimes echos prefix |
| 7 | `truncateRepetition` | Cut output when a window of 6-16 runes repeats 4+ times | Catches runaway repetition |
| 8 | `truncateAtStatementBoundary` | For outputs >512 chars, cut at last `;` or `}` | Prevents overly long completions |

### API Request Format

```json
{
  "model": "qwen2.5-coder:7b",
  "messages": [
    {"role": "system", "content": "<system prompt>"},
    {"role": "user", "content": "<user prompt>"}
  ],
  "max_tokens": 128,
  "temperature": 0.1
}
```

Key parameters:
- `temperature: 0.1` — near-deterministic for completions
- `max_tokens: 128` for ghost, `4096` for edits (`llm.edittokens`)
- Timeout via `llm.timeout` (default 120s)
- Response body capped at 1MB
- Up to 2 retries with linear backoff for transient network errors

### Request Lifecycle

```
User types character
  │
  ▼
BufPane.HandleEvent()
  → DoRuneInsert() → buffer.insert()
    → ClearGhostState() (atomic GhostRequestID++)
  → ScheduleLLMAutosuggest()
    → ClearGhostState() (atomic GhostRequestID++)
    → captures requestID
    → adaptive debounce (max(llm.debounce, lastLatency/2))
        │
        ▼ (after debounce, on main goroutine via LLMStartChan)
      StartLLMGhostRequest(true, requestID)
        → BuildFIMContext() → CursorContext
        → TrimContext(prefix, suffix, maxChars, maxSuffix%)
        → buildSystemPrompt(req) — cursor-aware
        → buildPrompt(req) — code-fenced with ## Completion:
        → goroutine: Complete(ctx, req)
            → POST /v1/chat/completions
            → parseResponse() → ApplyFilters(raw, req) [8-filter pipeline]
            → ScoreCompletion(text, req) — quality gate (reject <0.2)
            → RecordCompletion() — metrics to JSONL
            → SendLLMResponse(LLMResponse{...})
        │
        ▼ (in DoEvent, via LLMRespChan)
      if requestID < GhostRequestID: discard (stale)
      latency budget check: if ghost-auto && elapsed > llm.latencybudget: discard
      if mode == "ghost" || "ghost-auto": buf.GhostText = text
      → Display() → drawInlineGhost() or drawGhostText()
        │
        ▼ (user presses Tab or Ctrl-Right)
      Tab  → AcceptGhostText() → buf.Insert(loc, ghostText)
      Ctrl-Right → AcceptGhostWord() → insert first word, keep rest as ghost
```

### Configuration Reference

| Setting | Type | Default | Purpose |
|---|---|---|---|
| `llm.enabled` | bool | `true` | Master on/off |
| `llm.autosuggest` | bool | `true` | Auto ghost text while typing |
| `llm.baseurl` | string | `http://localhost:11434/v1` | OpenAI-compatible endpoint |
| `llm.apikey` | string | `""` | Bearer token (for remote APIs) |
| `llm.model` | string | `qwen2.5-coder:7b` | Model name |
| `llm.promptmode` | string | `"chat"` | `"fim"` for FIM tokens, `"chat"` for structured prompt |
| `llm.disablefiletypes` | string | `"markdown,txt,help,unknown"` | Skip autosuggest for these file types |
| `llm.debug` | bool | `false` | Write dispatch/response logs |
| `llm.contextlines` | float | `120` | Lines above cursor sent as context |
| `llm.maxinputchars` | float | `4096` | Max chars for prefix + suffix |
| `llm.maxsuffixpercentage` | float | `0.35` | Fraction of budget for suffix |
| `llm.debounce` | float | `500` | ms to wait after typing |
| `llm.edittokens` | float | `4096` | Max tokens for edit/chat responses |
| `llm.maxtokens` | float | `128` | Max tokens for ghost completions |
| `llm.timeout` | float | `120` | HTTP timeout in seconds |
| `llm.latencybudget` | float | `5000` | Max ms for auto-suggestions before discard |

### Known Limitations

1. **Soft-wrap ghost text**: Multi-line ghost text rendering (`drawGhostText`)
   does not account for soft wrap. Single-line inline ghost (`drawInlineGhost`)
   works correctly.

2. **FIM mode with chat models**: Setting `llm.promptmode = "fim"` with a model
   that doesn't support FIM tokens will produce garbage.

3. **Local model quality**: Completion quality is entirely model-dependent.
   Larger coder-tuned models (Qwen2.5-Coder 14B+) produce significantly better
   results but require more RAM/VRAM.

4. **No project-wide context**: Only same-file context is sent.

5. **Token counting**: Ollama token counting uses `qwen2.5-coder` model name
   for the `/api/tokenize` endpoint. If a different model is used, the token
   counts may be approximate. Falls back to character counting for remote APIs.

### Prompt Tuning Guide

When iterating on prompts, enable debug mode and compare:

```text
set llm.debug true
log
```

This opens the log buffer showing the exact prompts sent and responses received.

**Things to tune:**

| Knob | Where | Effect |
|---|---|---|
| System prompt strictness | `buildSystemPrompt()` in `client.go` | More/less aggressive "same line" rules |
| Cursor hints | `buildPrompt()` in `client.go` | Mid-word/mid-line annotations |
| Post-processing | `defaultFilterPipeline` in `postprocess.go` | Add/remove/reorder CompletionFilter funcs |
| Quality threshold | `StartLLMGhostRequest` in `actions.go` | Change `score < 0.2` to adjust gate |
| `llm.contextlines` | settings | More context helps but costs tokens/time |
| `llm.maxtokens` | settings | Higher = longer completions but slower |
| `llm.promptmode` | settings | `"fim"` is better for FIM-native models |
| `llm.latencybudget` | settings | Increase to allow slower completions |
| `temperature` | hardcoded `0.1` in `client.go` | Lower = more deterministic |

**Comparing against industry approaches:**

- **Continue.dev / Copilot**: Use FIM natively with `<|fim_prefix|>` tokens.
  If the model supports FIM, prefer `llm.promptmode = "fim"`.
- **supermaven / Tabby**: Also detect mid-token state and bias the sampler.
  Our approach uses prompt hints instead of sampler biasing.
- **Ollama autocomplete**: Uses a simple suffix/prefix prompt similar to our
  chat mode. The `CRITICAL: cursor is mid-word` hint is MicroFlow-specific.

## Upstream Attribution

MicroFlow inherits the majority of its editor core, runtime files, terminal UI,
syntax support, and plugin infrastructure from micro. Keep upstream attribution
intact when changing docs, packaging, or release metadata.
