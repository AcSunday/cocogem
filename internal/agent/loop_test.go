package agent

import (
	"context"
	"encoding/json"
	"github.com/AcSunday/cocogem/internal/model"
	"strings"
	"testing"
)

type scripted struct {
	calls     int
	duplicate bool
}

func (s *scripted) Complete(_ context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	s.calls++
	if s.calls == 1 || s.duplicate {
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "one", Name: "read_file", Input: json.RawMessage(`{}`)}}}, nil
	}
	if r.Messages[len(r.Messages)-1].Content[0].ToolUseID != "one" {
		panic("lost tool association")
	}
	return model.Response{StopReason: "end_turn"}, emit("done")
}

func TestLoopPermissionsAssociationAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name             string
		allow, duplicate bool
		wantErr          string
	}{
		{"allowed", true, false, ""}, {"denied", false, false, ""}, {"duplicate", true, true, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &scripted{duplicate: tc.duplicate}
			request := model.Request{}
			if tc.allow {
				request.Tools = []model.Tool{{Name: "read_file"}}
			}
			count := 0
			events := []string{}
			err := (Loop{Model: m}).Run(context.Background(), request, func(context.Context, string, json.RawMessage) (string, error) { count++; return "source", nil }, func(kind string, data map[string]any) error { events = append(events, kind); return nil })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !tc.allow && count != 0 {
				t.Fatal("denied tool executed")
			}
			if len(events) == 0 {
				t.Fatal("no observation")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Loop{Model: &scripted{}}).Run(ctx, model.Request{}, nil, nil); err == nil {
		t.Fatal("cancel ignored")
	}
}
