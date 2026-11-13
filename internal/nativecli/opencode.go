package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

func openCodeProbeConfig(endpoint string) map[string]any {
	return map[string]any{
		"model": "cocogem-probe/cocogem-probe",
		"providers": map[string]any{"cocogem-probe": map[string]any{
			"name": "Cocogem probe", "package": "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": endpoint + "/v1", "apiKey": "cocogem-probe-token"},
			"models": map[string]any{"cocogem-probe": map[string]any{
				"name": "Cocogem probe", "limit": map[string]any{"context": 128000, "output": 1024},
				"capabilities": map[string]any{"tools": true},
			}},
		}},
	}
}

func (p Provider) prepareOpenCode(ctx context.Context, request model.Request, dir string, gate *bridge, probeURL string, launch *connectionPlan) ([]string, map[string]string, error) {
	var plan connectionPlan
	if probeURL != "" {
		plan = connectionPlan{openCodeConfig: openCodeProbeConfig(probeURL), openCodeModel: "cocogem-probe/cocogem-probe", openCodeEnv: []string{}}
	} else if launch != nil {
		plan = *launch
	} else {
		return nil, nil, errors.New("OpenCode connection was not resolved before launch")
	}
	// Clone before adding per-step permissions to the cached connection plan.
	raw, _ := json.Marshal(plan.openCodeConfig)
	config := map[string]any{}
	if json.Unmarshal(raw, &config) != nil {
		return nil, nil, errors.New("invalid OpenCode connection snapshot")
	}
	permissions := []any{map[string]any{"action": "*", "resource": "*", "effect": "deny"}}
	for _, tool := range request.Tools {
		permissions = append(permissions, map[string]any{"action": "cocogem_" + tool.Name, "resource": "*", "effect": "allow"})
	}
	if gate.approver != nil {
		// The private execute.before plugin is the single gate; it asks the host
		// before every tool and runs native commands through the host's
		// per-call sandbox. Do not also emit permission.asked for the same call.
		for _, action := range []string{"bash", "shell", "edit", "write", "read", "webfetch", "websearch", "glob", "grep", "list", "patch", "apply_patch", "view_image"} {
			permissions = append(permissions, map[string]any{"action": action, "resource": "*", "effect": "allow"})
		}
	} else if (request.ExecutionMode == "project_autonomous" || request.ExecutionMode == "full_access") && !gate.trustedNative {
		for _, action := range []string{"bash", "shell", "edit", "write", "read", "webfetch", "websearch", "glob", "grep", "list", "patch", "apply_patch", "view_image"} {
			permissions = append(permissions, map[string]any{"action": action, "resource": "*", "effect": "ask"})
		}
	}
	if gate.trustedNative {
		permissions = []any{map[string]any{"action": "*", "resource": "*", "effect": "allow"}}
	}
	config["permissions"] = permissions
	policies := []any{
		map[string]any{"action": "tool.use", "resource": "*", "effect": "deny"},
		map[string]any{"action": "skill.use", "resource": "*", "effect": "deny"},
		map[string]any{"action": "plugin.use", "resource": "*", "effect": "deny"},
	}
	for _, tool := range request.Tools {
		policies = append(policies, map[string]any{"action": "tool.use", "resource": "cocogem_" + tool.Name + ":*", "effect": "allow"})
	}
	if gate.trustedNative || request.ExecutionMode == "project_autonomous" || request.ExecutionMode == "full_access" && gate.approver != nil {
		policies = []any{map[string]any{"action": "tool.use", "resource": "*", "effect": "allow"}}
	}
	config["policies"] = policies
	config["agents"] = map[string]any{"cocogem": map[string]any{"mode": "primary", "system": "Complete only the supplied step using the authorized Cocogem tools.", "permissions": permissions}}
	if gate.trustedNative {
		config["agents"] = map[string]any{"cocogem": map[string]any{"mode": "primary", "system": "Complete the authorized task with native tools. Use Cocogem tools for member handoff and final file delivery; preserve input files.", "permissions": permissions}}
	}
	config["default_agent"], config["share"], config["update"] = "cocogem", "disabled", "disable"
	config["snapshots"], config["warming"] = false, false
	config["skills"], config["instructions"] = []string{}, []string{}
	plugins := []string{"-opencode.tool.skill", "-opencode.skill", "-opencode.config.skill", "-opencode.config.compatibility"}
	if gate.approver != nil {
		pluginDir := filepath.Join(filepath.Dir(dir), "cocogem-plugin")
		if err := writeOpenCodePlugin(pluginDir); err != nil {
			return nil, nil, err
		}
		plugins = append([]string{pluginDir}, plugins...)
	}
	config["plugins"] = plugins
	if request.ExecutionMode == "project_autonomous" && gate.sandbox != nil && gate.modelGateway == nil {
		gateway, err := openCodeModelGateway(config, plan.openCodeModel)
		if err != nil {
			return nil, nil, err
		}
		gate.modelGateway = gateway
		gate.sandbox.setAllowedAddrs([]string{gateway.addr(), gate.addr})
		gate.sandbox.allowInbound = true
	}
	config["mcp"] = map[string]any{"servers": map[string]any{"cocogem": map[string]any{
		"type": "remote", "url": gate.url(), "oauth": false, "codemode": false,
		"headers": map[string]string{"Authorization": "Bearer " + gate.token},
	}}}
	if err := p.writeOpenCodeConfig(ctx, dir, plan, config); err != nil {
		return nil, nil, err
	}
	args := []string{"run", "--standalone", "--format", "json", "--thinking", "--agent", "cocogem", "--title", "Cocogem step"}
	if plan.openCodeModel != "" {
		selected, err := openCodeModelVariant(plan, request.ThinkingLevel)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--model", selected)
	}
	return args, openCodeScopedEnv(filepath.Clean(dir)), nil
}

// openCodeModelGateway points the sandboxed CLI at a loopback reverse proxy so
// the model connection stays outside the sandbox while tool processes see only
// the private listener.
func openCodeModelGateway(config map[string]any, selectedModel string) (*modelGateway, error) {
	providerID, modelID, valid := strings.Cut(selectedModel, "/")
	if valid && providerID != "" && modelID != "" {
		// OpenCode gives an explicit providers entry precedence over the
		// compatibility provider entry with the same ID.
		for _, schema := range []struct{ group, settings string }{
			{"providers", "settings"},
			{"provider", "options"},
		} {
			providers, _ := config[schema.group].(map[string]any)
			raw, exists := providers[providerID]
			if !exists {
				continue
			}
			provider, _ := raw.(map[string]any)
			settings, _ := provider[schema.settings].(map[string]any)
			base, _ := settings["baseURL"].(string)
			if base == "" {
				break
			}
			gateway, err := newModelGateway(base)
			if err != nil {
				return nil, err
			}
			settings["baseURL"] = gateway.url()
			return gateway, nil
		}
	}
	return nil, errors.New("OpenCode model endpoint is not available for the loopback gateway")
}

var openCodeSessionPattern = regexp.MustCompile(`^ses_[a-zA-Z0-9]{20,64}$`)

// Thinking choices come from the resolved model, including custom variants.
func (p Provider) ModelOptions(ctx context.Context, executor, selected string) (agent.ModelOptions, error) {
	if executor != "opencode" {
		return agent.ModelOptions{}, errors.New("model options are only available for OpenCode")
	}
	plan, err := p.openCodePlan(ctx, selected)
	if err != nil {
		return agent.ModelOptions{}, err
	}
	return agent.ModelOptions{ResolvedModel: plan.openCodeModel, ThinkingLevels: append([]string{}, plan.openCodeVariants...)}, nil
}

func openCodeModelVariant(plan connectionPlan, thinking string) (string, error) {
	if thinking == "" {
		return plan.openCodeModel, nil
	}
	if !slices.Contains(plan.openCodeVariants, thinking) {
		return "", fmt.Errorf("OpenCode model %s does not support thinking variant %q", strings.SplitN(plan.openCodeModel, "#", 2)[0], thinking)
	}
	return strings.SplitN(plan.openCodeModel, "#", 2)[0] + "#" + thinking, nil
}
