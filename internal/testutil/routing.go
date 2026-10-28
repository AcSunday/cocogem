package testutil

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func route(request model.Request) (model.Response, error) {
	if len(request.Messages) == 0 || len(request.Messages[0].Content) == 0 {
		return model.Response{}, errors.New("missing routing test input")
	}
	catalog, input, found := strings.Cut(request.Messages[0].Content[0].Text, "\nCurrent user input:\n")
	var candidates []w.Agent
	if !found || json.Unmarshal([]byte(strings.TrimPrefix(catalog, "Candidate members: ")), &candidates) != nil {
		return model.Response{}, errors.New("invalid routing test input")
	}
	routing := w.Routing{Decision: "discuss", Workflow: "free", Explanation: "测试路由结果"}
	assign := true
	for _, word := range []string{"不要", "别让", "do not", "如果", "假如", "我觉得", "怎么样", "会有什么"} {
		if strings.Contains(input, word) {
			assign = false
		}
	}
	if assign {
		routing.Decision = "assign"
		for _, candidate := range candidates {
			routing.Targets = append(routing.Targets, candidate.ID)
			routing.Evidence = append(routing.Evidence, w.RoutingEvidence{AgentID: candidate.ID, Quote: input})
		}
	}
	payload, err := json.Marshal(routing)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "route", Name: "routing_decision", Input: payload}}}, nil
}
