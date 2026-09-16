package planner

import "strings"

// isCeremonialDAGRole reports roles whose real work already happens outside
// the scheduler: Planner at Build*/Assess time, Synthesizer at runOnce tail.
func isCeremonialDAGRole(role string) bool {
	switch strings.TrimSpace(role) {
	case "Planner", "Synthesizer":
		return true
	default:
		return false
	}
}

// WithoutCeremonialDAGNodes drops Planner/Synthesizer scheduler slots and
// rewires DependsOn through the removed nodes. Avatars are unchanged — callers
// still emit lifecycle events for those roles. CriticHub may add a Planner
// replan node later; that path does not go through this helper.
func WithoutCeremonialDAGNodes(nodes []WorkflowNode) []WorkflowNode {
	if len(nodes) == 0 {
		return nodes
	}
	byID := make(map[string]WorkflowNode, len(nodes))
	for _, node := range nodes {
		id := strings.TrimSpace(node.ID)
		if id == "" {
			continue
		}
		byID[id] = node
	}
	var resolve func(id string, walking map[string]bool) []string
	resolve = func(id string, walking map[string]bool) []string {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		node, ok := byID[id]
		if !ok || !isCeremonialDAGRole(node.AssignedRole) {
			return []string{id}
		}
		if walking[id] {
			return nil
		}
		walking[id] = true
		var out []string
		seen := map[string]bool{}
		for _, dep := range node.DependsOn {
			for _, resolved := range resolve(dep, walking) {
				if resolved == "" || seen[resolved] {
					continue
				}
				seen[resolved] = true
				out = append(out, resolved)
			}
		}
		delete(walking, id)
		return out
	}

	out := make([]WorkflowNode, 0, len(nodes))
	for _, node := range nodes {
		if isCeremonialDAGRole(node.AssignedRole) {
			continue
		}
		seen := map[string]bool{}
		deps := make([]string, 0, len(node.DependsOn))
		for _, dep := range node.DependsOn {
			for _, resolved := range resolve(dep, map[string]bool{}) {
				if resolved == "" || resolved == node.ID || seen[resolved] {
					continue
				}
				seen[resolved] = true
				deps = append(deps, resolved)
			}
		}
		node.DependsOn = deps
		out = append(out, node)
	}
	return out
}
