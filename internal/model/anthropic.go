package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Anthropic invokes Messages API with a bounded, validated SSE response.
type Anthropic struct {
	APIKey   string
	Client   *http.Client
	Endpoint string
}

// Complete preserves tool identities and rejects truncated or ambiguous streams.
func (a *Anthropic) Complete(ctx context.Context, input Request, emit func(string) error) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	input.Stream = true
	b, err := json.Marshal(input)
	if err != nil {
		return Response{}, err
	}
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1/messages"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("model request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("model returned HTTP %d", resp.StatusCode)
	}
	return parseStream(resp.Body, emit)
}

func parseStream(reader io.Reader, emit func(string) error) (Response, error) {
	var result Response
	blocks := map[int]*Block{}
	closed := map[int]bool{}
	partials := map[int]string{}
	finished := false
	started := false
	total := 0
	scanner := bufio.NewScanner(io.LimitReader(reader, 16*1024*1024+1))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var data []string
	process := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		var e struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock Block  `json:"content_block"`
			Message      struct {
				Usage struct {
					Input int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Partial string `json:"partial_json"`
				Stop    string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return fmt.Errorf("invalid model event: %w", err)
		}
		if finished && e.Type != "ping" {
			return errors.New("event after message termination")
		}
		switch e.Type {
		case "ping":
			return nil
		case "error":
			return errors.New("model stream returned an error")
		case "message_start":
			if started {
				return errors.New("duplicate message start")
			}
			started = true
			result.InputTokens = e.Message.Usage.Input
			result.UsageKnown = true
		case "content_block_start":
			if !started {
				return errors.New("content before message start")
			}
			if _, exists := blocks[e.Index]; exists {
				return errors.New("duplicate content block")
			}
			block := e.ContentBlock
			blocks[e.Index] = &block
			if block.Type == "text" && block.Text != "" {
				return emit(block.Text)
			}
		case "content_block_delta":
			block := blocks[e.Index]
			if block == nil || closed[e.Index] {
				return errors.New("delta without content block")
			}
			switch e.Delta.Type {
			case "text_delta":
				block.Text += e.Delta.Text
				return emit(e.Delta.Text)
			case "input_json_delta":
				partials[e.Index] += e.Delta.Partial
			}
		case "content_block_stop":
			block := blocks[e.Index]
			if block == nil || closed[e.Index] {
				return errors.New("unknown block stop")
			}
			closed[e.Index] = true
			if block.Type == "tool_use" {
				if p := partials[e.Index]; p != "" {
					block.Input = json.RawMessage(p)
				}
				if len(block.Input) == 0 {
					block.Input = json.RawMessage(`{}`)
				}
				if !json.Valid(block.Input) || len(bytes.TrimSpace(block.Input)) == 0 || bytes.TrimSpace(block.Input)[0] != '{' || block.ID == "" || block.Name == "" {
					return errors.New("invalid tool request")
				}
			}
		case "message_delta":
			result.StopReason = e.Delta.Stop
			result.OutputTokens = e.Usage.Output
		case "message_stop":
			finished = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > 16*1024*1024 {
			return result, errors.New("model stream exceeds limit")
		}
		if line == "" {
			if err := process(); err != nil {
				return result, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if err := process(); err != nil {
		return result, err
	}
	if !started || !finished || !slices.Contains([]string{"end_turn", "tool_use", "stop_sequence"}, result.StopReason) {
		return result, errors.New("model response did not confirm successful completion")
	}
	indexes := []int{}
	for index := range blocks {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	for _, index := range indexes {
		if !closed[index] {
			return result, errors.New("unterminated content block")
		}
		block := *blocks[index]
		if block.Type == "tool_use" && (len(block.Input) == 0 || !json.Valid(block.Input)) {
			return result, errors.New("incomplete tool input")
		}
		if block.Type == "text" || block.Type == "tool_use" {
			result.Blocks = append(result.Blocks, block)
		}
	}
	if result.StopReason == "tool_use" && !slices.ContainsFunc(result.Blocks, func(b Block) bool { return b.Type == "tool_use" }) {
		return result, errors.New("tool stop without tool calls")
	}
	return result, nil
}
