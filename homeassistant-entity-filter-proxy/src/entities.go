package main

import (
	"strings"
)

// extractEntities walks a Lovelace config and extracts all referenced entity IDs.
// This is a port of the frontend's computeUsedEntities() from
// frontend/src/panels/lovelace/common/compute-unused-entities.ts
func extractEntities(config map[string]any) []string {
	entities := make(map[string]struct{})

	views, ok := config["views"].([]any)
	if !ok {
		return nil
	}
	for _, view := range views {
		if viewMap, ok := view.(map[string]any); ok {
			addEntities(entities, viewMap)
		}
	}

	result := make([]string, 0, len(entities))
	for id := range entities {
		result = append(result, id)
	}
	return result
}

func addEntities(entities map[string]struct{}, obj map[string]any) {
	// entity field (string or object with entity sub-field)
	if entity, ok := obj["entity"]; ok {
		addEntityID(entities, entity)
	}

	// entities array
	if entitiesArr, ok := obj["entities"].([]any); ok {
		for _, entity := range entitiesArr {
			addEntityID(entities, entity)
		}
	}

	// Recurse into nested structures
	if card, ok := obj["card"].(map[string]any); ok {
		addEntities(entities, card)
	}
	if cards, ok := obj["cards"].([]any); ok {
		for _, card := range cards {
			if cardMap, ok := card.(map[string]any); ok {
				addEntities(entities, cardMap)
			}
		}
	}
	if elements, ok := obj["elements"].([]any); ok {
		for _, elem := range elements {
			if elemMap, ok := elem.(map[string]any); ok {
				addEntities(entities, elemMap)
			}
		}
	}
	if badges, ok := obj["badges"].([]any); ok {
		for _, badge := range badges {
			addEntityID(entities, badge)
		}
	}
	if sections, ok := obj["sections"].([]any); ok {
		for _, section := range sections {
			if sectionMap, ok := section.(map[string]any); ok {
				addEntities(entities, sectionMap)
			}
		}
	}
}

func addEntityID(entities map[string]struct{}, entity any) {
	if entity == nil {
		return
	}

	// Simple string entity ID
	if s, ok := entity.(string); ok {
		if looksLikeEntityID(s) {
			entities[s] = struct{}{}
		}
		return
	}

	// Object with sub-fields
	obj, ok := entity.(map[string]any)
	if !ok {
		return
	}

	if e, ok := obj["entity"].(string); ok && looksLikeEntityID(e) {
		entities[e] = struct{}{}
	}
	if cam, ok := obj["camera_image"].(string); ok && looksLikeEntityID(cam) {
		entities[cam] = struct{}{}
	}

	// Extract entities from actions
	if tapAction, ok := obj["tap_action"].(map[string]any); ok {
		addFromAction(entities, tapAction)
	}
	if holdAction, ok := obj["hold_action"].(map[string]any); ok {
		addFromAction(entities, holdAction)
	}
}

func addFromAction(entities map[string]struct{}, action map[string]any) {
	actionType, _ := action["action"].(string)
	if actionType != "call-service" {
		return
	}

	// Check target.entity_id, data.entity_id, service_data.entity_id
	for _, key := range []string{"target", "data", "service_data"} {
		if sub, ok := action[key].(map[string]any); ok {
			if eid, ok := sub["entity_id"]; ok {
				addEntityIDValue(entities, eid)
			}
		}
	}
}

func addEntityIDValue(entities map[string]struct{}, value any) {
	switch v := value.(type) {
	case string:
		if looksLikeEntityID(v) {
			entities[v] = struct{}{}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && looksLikeEntityID(s) {
				entities[s] = struct{}{}
			}
		}
	}
}

func looksLikeEntityID(s string) bool {
	return strings.Contains(s, ".")
}
