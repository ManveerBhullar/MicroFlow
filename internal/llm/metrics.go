package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/micro-editor/micro/v2/internal/config"
)

type CompletionEvent struct {
	Timestamp    string  `json:"ts"`
	Mode         string  `json:"mode"`
	Model        string  `json:"model"`
	FileType     string  `json:"filetype"`
	LatencyMs    int64   `json:"latency_ms"`
	InputChars   int     `json:"input_chars"`
	OutputChars  int     `json:"output_chars"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	Accepted     bool    `json:"accepted"`
	Score        float64 `json:"score,omitempty"`
	Status       string  `json:"status"`
}

type MetricsStore struct {
	mu         sync.Mutex
	file       *os.File
	totalReqs  int64
	totalAccepted int64
	totalLatencyNs int64
	lastLatencyMs  int64
	sessionStart  time.Time
}

var globalMetrics *MetricsStore
var metricsOnce sync.Once

func InitMetrics() {
	metricsOnce.Do(func() {
		globalMetrics = &MetricsStore{
			sessionStart: time.Now(),
		}
		path := metricsPath()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			globalMetrics.file = f
		}
	})
}

func metricsPath() string {
	dir := filepath.Join(config.ConfigDir, "llm")
	os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "metrics.jsonl")
}

func RecordCompletion(ev CompletionEvent) {
	if globalMetrics == nil {
		return
	}

	atomic.AddInt64(&globalMetrics.totalReqs, 1)
	if ev.Accepted {
		atomic.AddInt64(&globalMetrics.totalAccepted, 1)
	}
	atomic.StoreInt64(&globalMetrics.lastLatencyMs, ev.LatencyMs)
	if ev.LatencyMs > 0 {
		atomic.AddInt64(&globalMetrics.totalLatencyNs, ev.LatencyMs*1e6)
	}

	if globalMetrics.file != nil {
		data, _ := json.Marshal(ev)
		data = append(data, '\n')

		globalMetrics.mu.Lock()
		globalMetrics.file.Write(data)
		globalMetrics.mu.Unlock()
	}
}

func GetLastLatencyMs() int64 {
	return atomic.LoadInt64(&globalMetrics.lastLatencyMs)
}

type MetricsSummary struct {
	SessionDuration  string
	TotalRequests    int64
	TotalAccepted    int64
	AcceptRate       float64
	AvgLatencyMs     int64
	LastLatencyMs    int64
}

func GetMetricsSummary() MetricsSummary {
	if globalMetrics == nil {
		return MetricsSummary{}
	}

	total := atomic.LoadInt64(&globalMetrics.totalReqs)
	accepted := atomic.LoadInt64(&globalMetrics.totalAccepted)
	totalLatNs := atomic.LoadInt64(&globalMetrics.totalLatencyNs)

	var avgLat int64
	if total > 0 {
		avgLat = totalLatNs / total / 1e6
	}

	var acceptRate float64
	if total > 0 {
		acceptRate = float64(accepted) / float64(total) * 100
	}

	return MetricsSummary{
		SessionDuration: time.Since(globalMetrics.sessionStart).Round(time.Minute).String(),
		TotalRequests:   total,
		TotalAccepted:   accepted,
		AcceptRate:      acceptRate,
		AvgLatencyMs:    avgLat,
		LastLatencyMs:   atomic.LoadInt64(&globalMetrics.lastLatencyMs),
	}
}

func FormatMetricsSummary() string {
	s := GetMetricsSummary()
	if s.TotalRequests == 0 {
		return "No LLM completions this session"
	}
	return fmt.Sprintf(
		"Session: %s | Requests: %d | Accepted: %d (%.0f%%) | Avg latency: %dms | Last: %dms",
		s.SessionDuration, s.TotalRequests, s.TotalAccepted, s.AcceptRate, s.AvgLatencyMs, s.LastLatencyMs,
	)
}
