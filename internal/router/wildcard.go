package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WildcardModelConfig is the JSON/YAML shape used by the admin API and optional
// startup file for the model="*" fail-down ladder.
type WildcardModelConfig struct {
	Models []string `json:"models" yaml:"models"`
}

// NormalizeWildcardModelIDs trims, de-duplicates, and validates an ordered
// wildcard ladder. An empty list clears the runtime override and falls back to
// the built-in defaults.
func NormalizeWildcardModelIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("models[%d] must not be empty", i)
		}
		if IsWildcardModelHint(id) {
			return nil, fmt.Errorf("models[%d] must be a concrete model ID, not %q", i, WildcardModelHint)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// ParseWildcardModelIDs accepts either:
//
//	models:
//	  - gpt-5.5
//	  - gpt-5.4-mini
//
// or a top-level JSON/YAML string array.
func ParseWildcardModelIDs(data []byte) ([]string, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, errors.New("wildcard model config is empty")
	}

	var jsonList []string
	if err := json.Unmarshal(data, &jsonList); err == nil && jsonList != nil {
		return NormalizeWildcardModelIDs(jsonList)
	}
	var jsonDoc WildcardModelConfig
	if err := json.Unmarshal(data, &jsonDoc); err == nil && jsonDoc.Models != nil {
		return NormalizeWildcardModelIDs(jsonDoc.Models)
	}

	var yamlList []string
	if err := yaml.Unmarshal(data, &yamlList); err == nil && yamlList != nil {
		return NormalizeWildcardModelIDs(yamlList)
	}
	var yamlDoc WildcardModelConfig
	if err := yaml.Unmarshal(data, &yamlDoc); err == nil && yamlDoc.Models != nil {
		return NormalizeWildcardModelIDs(yamlDoc.Models)
	}

	return nil, errors.New(`wildcard model config must be {"models":["model-a"]} or a top-level model ID list`)
}

// SetWildcardModelIDs replaces the runtime wildcard ladder. Passing an empty
// list clears the override and restores the built-in defaults.
func (e *Engine) SetWildcardModelIDs(ids []string) error {
	normalized, err := NormalizeWildcardModelIDs(ids)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.wildcardModelIDs = append([]string(nil), normalized...)
	return nil
}

// ConfiguredWildcardModelIDs returns the runtime override, if one is configured.
func (e *Engine) ConfiguredWildcardModelIDs() ([]string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.wildcardModelIDs) == 0 {
		return nil, false
	}
	return append([]string(nil), e.wildcardModelIDs...), true
}

// WildcardModelIDs returns the effective wildcard ladder, including built-in
// defaults when no runtime override has been configured.
func (e *Engine) WildcardModelIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.effectiveWildcardModelIDsLocked()
}

func (e *Engine) effectiveWildcardModelIDsLocked() []string {
	if len(e.wildcardModelIDs) > 0 {
		return append([]string(nil), e.wildcardModelIDs...)
	}
	return DefaultWildcardRoundRobinModelIDs()
}

func modelIDSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func sortModelsByIDOrder(models []Model, order []string) {
	if len(models) < 2 || len(order) == 0 {
		return
	}
	ranks := make(map[string]int, len(order))
	for i, id := range order {
		if _, ok := ranks[id]; !ok {
			ranks[id] = i
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		ri, iok := ranks[models[i].ID]
		rj, jok := ranks[models[j].ID]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		default:
			return models[i].ID < models[j].ID
		}
	})
}
