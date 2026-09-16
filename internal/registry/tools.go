package registry

import (
	"fmt"
	"sort"

	"avatars/internal/tools"
)

type ToolRegistry struct {
	tools map[string]tools.Definition
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]tools.Definition)}
}

func (r *ToolRegistry) Register(tool tools.Definition) error {
	if tool == nil {
		return fmt.Errorf("tool cannot be nil")
	}
	name := tool.Name()
	if name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}
	r.tools[name] = tool
	return nil
}

func (r *ToolRegistry) Get(name string) (tools.Definition, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) List() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
