package basispoints

import (
	"fmt"
	"sort"
	"strings"
)

// Selection is separate from the catalog so a forced choice never removes the
// schemas needed to replay other tools from earlier turns. BPS receives only its
// supported wire fields; the bridge enforces this policy before tool dispatch.
type clientToolChoice struct {
	required bool
	single   bool
	allowed  map[string]bool // nil permits the entire declared client catalog
}

type toolChoiceViolation struct{ message string }

func (e toolChoiceViolation) Error() string { return e.message }

func (b *Bridge) prepareToolChoice(value any) error {
	if value == nil || value == "auto" {
		return nil
	}
	if mode, ok := value.(string); ok {
		switch mode {
		case "none":
			b.toolChoice.allowed = map[string]bool{}
			return nil
		case "required":
			if len(b.tools) == 0 {
				return fmt.Errorf("basispoints tool_choice required needs at least one declared client tool")
			}
			b.toolChoice.required = true
			return nil
		default:
			return fmt.Errorf("basispoints tool_choice must be auto, none, required, or a client tool selection")
		}
	}
	selection, ok := value.(object)
	if !ok || selection == nil {
		return fmt.Errorf("basispoints tool_choice must be a string or selection object")
	}
	policy := clientToolChoice{required: true, allowed: make(map[string]bool)}
	if text(selection["type"]) == "allowed_tools" {
		if err := validateChoiceFields(selection, "type", "mode", "tools"); err != nil {
			return err
		}
		switch selection["mode"] {
		case "auto":
			policy.required = false
		case "required":
		default:
			return fmt.Errorf("basispoints tool_choice allowed_tools mode must be auto or required")
		}
		items, ok := selection["tools"].([]any)
		if !ok {
			return fmt.Errorf("basispoints tool_choice allowed_tools tools must be an array")
		}
		for _, raw := range items {
			item, ok := raw.(object)
			if !ok || item == nil {
				return fmt.Errorf("basispoints tool_choice allowed_tools entries must be client tool selections")
			}
			if err := b.selectClientTools(item, policy.allowed); err != nil {
				return err
			}
		}
	} else {
		if err := b.selectClientTools(selection, policy.allowed); err != nil {
			return err
		}
		policy.single = text(selection["type"]) != "namespace"
	}
	if policy.required && len(policy.allowed) == 0 {
		return fmt.Errorf("basispoints tool_choice required needs at least one selected client tool")
	}
	b.toolChoice = policy
	return nil
}

func validateChoiceFields(value object, fields ...string) error {
	for key := range value {
		known := false
		for _, field := range fields {
			known = known || key == field
		}
		if !known {
			return fmt.Errorf("basispoints tool_choice contains an unsupported selection field")
		}
	}
	return nil
}

func (b *Bridge) selectClientTools(selection object, allowed map[string]bool) error {
	kind := text(selection["type"])
	if kind != "function" && kind != "custom" && kind != "namespace" {
		return fmt.Errorf("basispoints tool_choice cannot select hosted or unsupported tools; select a declared client function, custom tool, or namespace")
	}
	if err := validateChoiceFields(selection, "type", "name", "namespace"); err != nil {
		return err
	}
	name, ok := selection["name"].(string)
	if !ok || name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("basispoints tool_choice requires a nonempty client tool name")
	}
	namespace := ""
	if value, exists := selection["namespace"]; exists {
		namespace, ok = value.(string)
		if !ok || strings.TrimSpace(namespace) != namespace {
			return fmt.Errorf("basispoints tool_choice namespace must be a string")
		}
	}
	key := name
	if namespace != "" {
		key = namespace + "." + name
	}
	if kind == "namespace" {
		matches := 0
		for candidate, info := range b.tools {
			if info.Namespace == key || strings.HasPrefix(info.Namespace, key+".") {
				allowed[candidate] = true
				matches++
			}
		}
		if matches == 0 {
			return fmt.Errorf("basispoints tool_choice namespace has no declared client tools")
		}
		return nil
	}
	info, exists := b.tools[key]
	if !exists || info.Kind != kind || (namespace != "" && (info.Namespace != namespace || info.Name != name)) {
		return fmt.Errorf("basispoints tool_choice must match a declared client tool name, namespace, and type")
	}
	allowed[key] = true
	return nil
}

func (p clientToolChoice) instructions() string {
	var instructions string
	if p.allowed != nil {
		names := make([]string, 0, len(p.allowed))
		for name := range p.allowed {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "\nClient tool_choice forbids all tool calls in this response. Return assistant text only."
		}
		instructions = "\nClient tool_choice restricts this response to these exact catalog names: " + quoted(names) + ". All other catalog entries are available only to interpret previous history."
	}
	if p.required {
		if p.single {
			instructions += "\nClient tool_choice requires exactly one call to the selected client tool in this response using its declared run_officejs transport."
		} else {
			instructions += "\nClient tool_choice requires at least one allowed client tool call in this response using its declared run_officejs transport."
		}
		instructions += " A text-only answer does not satisfy this request. Previous tool calls do not satisfy this turn's requirement."
	}
	return instructions
}

func (p clientToolChoice) validateCall(item object) error {
	key := text(item["name"])
	if namespace := text(item["namespace"]); namespace != "" {
		key = namespace + "." + key
	}
	if p.allowed != nil && !p.allowed[key] {
		return toolChoiceViolation{"basispoints response violates tool_choice: selected client tool is not allowed; no tool was executed"}
	}
	return nil
}

func (p clientToolChoice) validateCount(count int) error {
	if p.required && count == 0 {
		return toolChoiceViolation{"basispoints response violates tool_choice: a client tool call is required"}
	}
	if p.single && count != 1 {
		return toolChoiceViolation{"basispoints response violates tool_choice: exactly one selected client tool call is required"}
	}
	return nil
}
