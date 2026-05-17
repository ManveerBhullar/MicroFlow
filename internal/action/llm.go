package action

import (
	"context"

	"github.com/micro-editor/micro/v2/internal/buffer"
	"github.com/micro-editor/micro/v2/internal/config"
	"github.com/micro-editor/micro/v2/internal/llm"
	"github.com/micro-editor/micro/v2/internal/screen"
)

type LLMResponse struct {
	Buf       *buffer.Buffer
	Text      string
	Loc       buffer.Loc
	Start     buffer.Loc
	End       buffer.Loc
	RequestID int64
	Err       error
	Mode      string
	Debug     string
	Score     float64
	StartedAt int64
}

var LLMRespChan = make(chan LLMResponse, 4)
var LLMStartChan = make(chan func(), 16)
var LLMContext context.Context
var llmCancel context.CancelFunc

func InitLLM() {
	LLMContext, llmCancel = context.WithCancel(context.Background())
	llm.InitMetrics()
	llm.InitTokenCounter(config.GetGlobalOption("llm.baseurl").(string))
}

func CancelLLM() {
	if llmCancel != nil {
		llmCancel()
	}
}

func SendLLMResponse(resp LLMResponse) {
	select {
	case LLMRespChan <- resp:
		screen.Redraw()
	default:
	}
}
