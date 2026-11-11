package nativecli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
)

// piGateExtensionSource is the host's private Pi extension. It is loaded through
// an explicit `-e` path, which `--no-extensions` does not disable, so a project
// extension can never replace the guard.
//
// Verified with the installed 1.5.0 binary (`native-protocol-probes/
// pi_tool_gate_probe.py`): `tool_call` runs before the effect, returning
// `{block:true}` leaves the file unwritten, and mutating `event.input` changes
// what actually executes. `tool_result` runs after the effect.
//
// The extension reuses Pi's own built-in tools (no reimplementation), so native
// tool names, result shapes, events and session identity stay Pi's. The host
// only answers "deny", "run your original input", or "run this narrowed input":
// a per-call seatbelt-wrapped command, or the host-staged copy of an approved
// outside path.
func piGateExtensionSource(brokerURL, token string) string {
	return `import { request } from "node:http";

const BROKER = ` + jsonString(brokerURL) + `;
const TOKEN = ` + jsonString(token) + `;

// A failed host round-trip must never become an allow.
function gate(path, body) {
  return new Promise((resolve) => {
    const payload = JSON.stringify(body);
    const target = new URL(BROKER + path);
    const req = request({
      hostname: target.hostname,
      port: target.port,
      path: target.pathname,
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Content-Length": Buffer.byteLength(payload),
        Authorization: "Bearer " + TOKEN,
      },
    }, (res) => {
      let data = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => { data += chunk; });
      res.on("end", () => {
        if (res.statusCode !== 200) { resolve(null); return; }
        try { resolve(JSON.parse(data)); } catch { resolve(null); }
      });
    });
    req.on("error", () => resolve(null));
    req.setTimeout(3600000, () => { req.destroy(); resolve(null); });
    req.write(payload);
    req.end();
  });
}

async function authorize(callID, name, input) {
  const decision = await gate("/gate/pi/tool_call", { call_id: callID, name, input });
  if (!decision) {
    return { allow: false, reason: "the Cocogem approval gate is unavailable; the tool was not executed" };
  }
  return decision;
}

export default function (pi) {
  pi.on("tool_call", async (event) => {
    const decision = await authorize(event.toolCallId, event.toolName, event.input ?? {});
    if (!decision.allow) {
      return { block: true, reason: decision.reason || "the operation was not approved" };
    }
    // Only the exact fields the host narrowed are replaced; everything else the
    // model asked for stays identical, so Pi's own validation and result shape
    // remain the CLI's.
    if (decision.input && typeof decision.input === "object" && event.input && typeof event.input === "object") {
      for (const [key, value] of Object.entries(decision.input)) {
        if (JSON.stringify(event.input[key]) !== JSON.stringify(value)) {
          event.input[key] = value;
        }
      }
    }
    return undefined;
  });

  pi.on("tool_result", async (event) => {
    await gate("/gate/pi/finalize", { call_id: event.toolCallId, is_error: Boolean(event.isError) });
    return undefined;
  });
}
`
}

// jsonString encodes one string literal for the generated extension source.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// preparePiNative writes the scoped Pi agent directory plus the private gate
// extension and returns the RPC invocation.
func preparePiNative(request model.Request, workdir string, gate *bridge, launch *connectionPlan, broker *gateBroker, probeURL string) ([]string, map[string]string, error) {
	var plan connectionPlan
	switch {
	case probeURL != "":
		plan = connectionPlan{piAuth: []byte("{}"), piModels: piProbeModels(probeURL), piSettings: []byte(piProbeSettings)}
	case launch != nil:
		plan = *launch
	default:
		return nil, nil, errors.New("Pi connection was not resolved before launch")
	}
	dir, err := writePiConfig(workdir, plan)
	if err != nil {
		return nil, nil, err
	}
	if err := writePiMCP(dir, gate); err != nil {
		return nil, nil, err
	}
	extension := filepath.Join(dir, "cocogem-gate.mjs")
	if err := os.WriteFile(extension, []byte(piGateExtensionSource(broker.url(), broker.token)), 0600); err != nil {
		return nil, nil, errors.New("write scoped Pi gate extension")
	}
	names := make([]string, 0, len(request.Tools)+4)
	for _, tool := range request.Tools {
		names = append(names, "mcp__cocogem__"+tool.Name)
	}
	names = append(names, "read", "bash", "edit", "write")
	// RPC mode is long-lived: adding --print would make Pi exit after the
	// prompt instead of emitting agent_settled, so the gate would hang.
	args := []string{
		"--mode", "rpc", "--no-session", "--no-skills", "--no-prompt-templates",
		"--no-themes", "--no-context-files", "--no-approve", "--no-extensions",
		"-e", "builtin:mcp", "-e", extension, "--tools", strings.Join(names, ","),
	}
	if plan.piModel != "" {
		args = append(args, "--model", plan.piModel, "--thinking", plan.piThinking)
	}
	return args, piScopedEnv(dir), nil
}
