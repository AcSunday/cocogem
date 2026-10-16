// Package agent executes bounded model/tool loops independently of workflow scheduling.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// Observe persists visible execution facts. Returning an error stops the loop.
type Observe func(kind string, data map[string]any) error

// Execute is a permission-checked host tool handler.
type Execute func(context.Context, string, json.RawMessage) (string, error)

// Loop drives one member's step; it does not delegate work or create runs.
type Loop struct {
	Model     model.Model
	MaxRounds int
	MaxTools  int
}

// Run executes tools with exact identities and never treats missing termination as success.
func (l Loop) Run(ctx context.Context, request model.Request, execute Execute, observe Observe) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	rounds := l.MaxRounds
	if rounds == 0 {
		rounds = 12
	}
	toolLimit := l.MaxTools
	if toolLimit == 0 {
		toolLimit = 30
	}
	calls := 0
	seen := map[string]bool{}
	for round := 0; round < rounds; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A conservative byte ceiling avoids silently dropping required upstream input.
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		if len(encoded) > 100000 {
			return errors.New("required context exceeds the 100000-byte host input budget")
		}
		response, err := l.Model.Complete(ctx, request, func(text string) error { return observe("text", map[string]any{"text": text}) })
		if err != nil {
			return err
		}
		if err := observe("usage", map[string]any{"input_tokens": response.InputTokens, "output_tokens": response.OutputTokens, "known": response.UsageKnown}); err != nil {
			return err
		}
		if response.StopReason != "tool_use" {
			if response.StopReason == "end_turn" || response.StopReason == "stop_sequence" {
				return nil
			}
			return errors.New("model completion is unconfirmed")
		}
		request.Messages = append(request.Messages, model.Message{Role: "assistant", Content: response.Blocks})
		results := []model.Block{}
		for _, block := range response.Blocks {
			if block.Type != "tool_use" {
				continue
			}
			calls++
			if calls > toolLimit {
				return errors.New("tool-call budget exceeded")
			}
			if block.ID == "" || seen[block.ID] {
				return errors.New("duplicate or missing tool-call identity")
			}
			seen[block.ID] = true
			if err := observe("tool_started", map[string]any{"tool_call_id": block.ID, "name": block.Name, "input": block.Input}); err != nil {
				return err
			}
			started := time.Now()
			var output string
			var callErr error
			if !slices.ContainsFunc(request.Tools, func(t model.Tool) bool { return t.Name == block.Name }) {
				callErr = errors.New("tool is not allowed for this member")
			} else {
				output, callErr = execute(ctx, block.Name, block.Input)
			}
			status := "ok"
			if callErr != nil {
				status = "error"
				output = "Tool error: " + callErr.Error()
			}
			if len(output) > 65536 {
				return errors.New("tool observation exceeds 64 KiB")
			}
			if err := observe("tool_finished", map[string]any{"tool_call_id": block.ID, "name": block.Name, "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			results = append(results, model.Block{Type: "tool_result", ToolUseID: block.ID, Content: output, IsError: callErr != nil})
		}
		if len(results) == 0 {
			return errors.New("tool continuation contains no tool calls")
		}
		request.Messages = append(request.Messages, model.Message{Role: "user", Content: results})
	}
	return fmt.Errorf("model-turn budget exceeded (%d)", rounds)
}
