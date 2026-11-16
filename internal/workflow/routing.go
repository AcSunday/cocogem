package workflow

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

var fenced = regexp.MustCompile("(?s)```.*?```|`[^`]*`|“[^”\\n]*”|「[^」\\n]*」|\"[^\"\\n]*\"|(?m)^\\s*>.*$")

// mention matches an @recipient token anywhere in prose. Handles are ASCII, but
// a display name or alias may be CJK, so the token accepts any non-space,
// non-punctuation run. The negative lookbehind equivalent (a preceding letter or
// digit means an email address) is handled by the shotPrefix check below.
var mention = regexp.MustCompile(`@([\p{L}\p{N}][\p{L}\p{N}_-]{0,31})`)

// Routing is a proposed exact dispatch, not authority to execute host tools.
type Routing = w.Routing

// ResolveRouting is deterministic and model-free: explicit @mentions and the
// members selected in the Composer choose the recipients, and an unaddressed
// message goes straight to the conversation coordinator. No preset workflow or
// slow semantic pre-routing is consulted.
func ResolveRouting(input w.RunInput, thread w.Thread, agents []w.Agent) (Routing, error) {
	byHandle := map[string]w.Agent{}
	byID := map[string]w.Agent{}
	for _, a := range agents {
		if a.Disabled {
			continue
		}
		byHandle[a.Handle] = a
		byHandle[strings.ToLower(a.DisplayName)] = a
		for _, alias := range a.Aliases {
			byHandle[strings.ToLower(alias)] = a
		}
		byID[a.ID] = a
	}
	clean := fenced.ReplaceAllString(input.Message, "")
	explicit := []string{}
	for _, match := range mention.FindAllStringSubmatchIndex(clean, -1) {
		// An '@' preceded by a letter, digit or dot is part of an email address,
		// not a recipient mention.
		if match[0] > 0 {
			previous := clean[match[0]-1]
			if previous >= 'a' && previous <= 'z' || previous >= 'A' && previous <= 'Z' || previous >= '0' && previous <= '9' || previous == '.' || previous == '_' {
				continue
			}
		}
		name := clean[match[2]:match[3]]
		a, ok := byHandle[name]
		if !ok {
			a, ok = byHandle[strings.ToLower(name)]
		}
		if !ok {
			return Routing{Decision: "clarify", Clarification: "成员 @" + name + " 不可用，请选择有效成员。"}, nil
		}
		if !slices.Contains(explicit, a.ID) {
			explicit = append(explicit, a.ID)
		}
	}
	if len(explicit) > 0 {
		if hasNamedNegation(clean, agents) {
			return Routing{Decision: "clarify", Clarification: "正文包含对成员的否定，同时存在明确提及。请说明本次实际收件人。"}, nil
		}
		if hasNamedCondition(clean, agents) {
			return Routing{Decision: "clarify", Clarification: "正文包含针对成员的条件，同时存在明确提及。请直接选择实际收件人，或说明现在需要执行的任务。"}, nil
		}
		if len(input.Targets) > 0 && !sameIDs(explicit, input.Targets) {
			return Routing{}, errors.New("explicit mentions conflict with selected recipients")
		}
		return Routing{Decision: "assign", Targets: explicit, Explanation: "按用户明确指定的成员执行"}, nil
	}
	if len(input.Targets) > 0 {
		if len(input.Targets) > maxTargets {
			return Routing{}, errors.New("select at most four recipients")
		}
		seen := map[string]bool{}
		for _, id := range input.Targets {
			if _, ok := byID[id]; !ok {
				return Routing{}, errors.New("selected member is unavailable")
			}
			if seen[id] {
				return Routing{}, errors.New("duplicate recipient")
			}
			seen[id] = true
		}
		return Routing{Decision: "assign", Targets: slices.Clone(input.Targets), Explanation: "按用户选择的成员执行"}, nil
	}
	if thread.CoordinatorID == "" {
		return Routing{}, errors.New("conversation has no coordinator to receive the message")
	}
	return Routing{Decision: "discuss", Targets: []string{thread.CoordinatorID}, Explanation: "没有明确收件人，交给会话协调者"}, nil
}

// A negation or condition only guards routing when it shares a clause with the
// member (or with a pronoun bound to a mentioned member). Unrelated constraints
// elsewhere in the message are not about the member.
func hasNamedNegation(text string, agents []w.Agent) bool {
	return hasMemberConstraint(text, agents, []string{"不要", "别让", "不用", "不让", "不需要", "don't", "do not", "must not"})
}

func hasNamedCondition(text string, agents []w.Agent) bool {
	return hasMemberConstraint(text, agents, []string{"如果", "假如", "假设", "是否让", "what if"})
}

func hasMemberConstraint(text string, agents []w.Agent, markers []string) bool {
	mentioned, _ := namedCandidates(text, agents)
	clauses := strings.FieldsFunc(text, func(r rune) bool {
		return strings.ContainsRune("\n，。；,;.!?？！", r)
	})
	for _, clause := range clauses {
		lower := strings.ToLower(clause)
		marked := false
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				marked = true
				break
			}
		}
		if !marked {
			continue
		}
		if names, _ := namedCandidates(clause, agents); len(names) != 0 {
			return true
		}
		if len(mentioned) == 0 {
			continue
		}
		// 其他/其它 contain 他/它 but never refer to a member.
		pronounced := strings.NewReplacer("其他", "", "其它", "").Replace(lower)
		for _, reference := range []string{"他", "她", "让其", "由其", "其参与", "其继续", "该成员", "这个成员", "上述成员", " him", " her", " them", " their"} {
			if strings.Contains(pronounced, reference) {
				return true
			}
		}
	}
	return false
}

func sameIDs(a, b []string) bool {
	x := slices.Clone(a)
	y := slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// namedCandidates matches normalized names at the longest non-overlapping span.
func namedCandidates(input string, agents []w.Agent) ([]w.Agent, bool) {
	text := strings.ToLower(input)
	owners := map[string][]w.Agent{}
	for _, agent := range agents {
		if agent.Disabled {
			continue
		}
		for _, name := range append([]string{agent.DisplayName, agent.Handle}, agent.Aliases...) {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if !slices.ContainsFunc(owners[key], func(a w.Agent) bool { return a.ID == agent.ID }) {
				owners[key] = append(owners[key], agent)
			}
		}
	}
	candidates := []w.Agent{}
	ambiguous := false
	asciiWord := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-' }
	for offset := 0; offset < len(text); {
		longest := ""
		for name := range owners {
			if len(name) <= len(longest) || !strings.HasPrefix(text[offset:], name) {
				continue
			}
			end := offset + len(name)
			if asciiWord(name[0]) && offset > 0 && asciiWord(text[offset-1]) {
				continue
			}
			if asciiWord(name[len(name)-1]) && end < len(text) && asciiWord(text[end]) {
				continue
			}
			longest = name
		}
		if longest == "" {
			offset++
			continue
		}
		matches := owners[longest]
		if len(matches) > 1 {
			ambiguous = true
		}
		for _, agent := range matches {
			if !slices.ContainsFunc(candidates, func(a w.Agent) bool { return a.ID == agent.ID }) {
				candidates = append(candidates, agent)
			}
		}
		offset += len(longest)
	}
	return candidates, ambiguous
}
