package netboxtool

import (
	"fmt"
	"net/url"
	"strconv"
)

// Custom field type slugs as accepted by POST/PATCH /api/extras/custom-fields/.
const (
	CFTypeText    = "text"
	CFTypeInteger = "integer"
	CFTypeBoolean = "boolean"
	CFTypeSelect  = "select"
)

type restChoiceSet struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	ExtraChoices any    `json:"extra_choices"`
}

func (r restChoiceSet) toNB() *NBChoiceSet {
	return &NBChoiceSet{
		NetboxID:     r.ID,
		Name:         r.Name,
		ExtraChoices: parseExtraChoices(r.ExtraChoices),
	}
}

func parseExtraChoices(raw any) [][2]string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out [][2]string
	for _, item := range list {
		pair, ok := item.([]any)
		if !ok || len(pair) < 2 {
			continue
		}
		val, _ := pair[0].(string)
		lab, _ := pair[1].(string)
		if val == "" {
			continue
		}
		if lab == "" {
			lab = val
		}
		out = append(out, [2]string{val, lab})
	}
	return out
}

type restChoiceSetList struct {
	Results []restChoiceSet `json:"results"`
}

// GetCustomFieldChoiceSet looks up extras.CustomFieldChoiceSet by exact name.
// Returns nil, nil if none matches.
func (nb *NetboxClient) GetCustomFieldChoiceSet(name string) (*NBChoiceSet, error) {
	var page restChoiceSetList
	if err := nb.restGet("/api/extras/custom-field-choice-sets/?name="+url.QueryEscape(name), &page); err != nil {
		return nil, err
	}
	if len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0].toNB(), nil
}

// EnsureCustomFieldChoiceSet returns the named choice set, creating it or
// adding any missing extra_choices. Existing choices are never removed —
// Netbox forbids shrinking a set that objects already use.
func (nb *NetboxClient) EnsureCustomFieldChoiceSet(name string, choices [][2]string) (*NBChoiceSet, error) {
	set, err := nb.GetCustomFieldChoiceSet(name)
	if err != nil {
		return nil, err
	}
	if set == nil {
		payload := map[string]any{
			"name":          name,
			"extra_choices": extraChoicesPayload(choices),
		}
		var created restChoiceSet
		if err := nb.restPost("/api/extras/custom-field-choice-sets/", payload, &created); err != nil {
			return nil, err
		}
		return created.toNB(), nil
	}
	merged, added := mergeChoices(set.ExtraChoices, choices)
	if !added {
		return set, nil
	}
	var updated restChoiceSet
	if err := nb.restPatchOut("/api/extras/custom-field-choice-sets/"+strconv.FormatUint(uint64(set.NetboxID), 10)+"/",
		map[string]any{"extra_choices": extraChoicesPayload(merged)}, &updated); err != nil {
		return nil, err
	}
	return updated.toNB(), nil
}

func extraChoicesPayload(choices [][2]string) [][]string {
	out := make([][]string, 0, len(choices))
	for _, c := range choices {
		lab := c[1]
		if lab == "" {
			lab = c[0]
		}
		out = append(out, []string{c[0], lab})
	}
	return out
}

func mergeChoices(have, want [][2]string) (merged [][2]string, added bool) {
	seen := make(map[string]bool, len(have))
	merged = append(merged, have...)
	for _, c := range have {
		seen[c[0]] = true
	}
	for _, c := range want {
		if c[0] == "" || seen[c[0]] {
			continue
		}
		merged = append(merged, c)
		seen[c[0]] = true
		added = true
	}
	return merged, added
}

func (w CustomFieldWrite) toPayload() map[string]any {
	p := map[string]any{
		"name":         w.Name,
		"type":         w.Type,
		"object_types": w.ObjectTypes,
		"required":     w.Required,
	}
	if w.Label != "" {
		p["label"] = w.Label
	}
	if w.Description != "" {
		p["description"] = w.Description
	}
	if w.GroupName != "" {
		p["group_name"] = w.GroupName
	}
	if w.ChoiceSetID != 0 {
		p["choice_set"] = w.ChoiceSetID
	}
	return p
}

// CreateCustomField POSTs extras.CustomField.
func (nb *NetboxClient) CreateCustomField(w CustomFieldWrite) (*NBCustomField, error) {
	var created restCustomField
	if err := nb.restPost("/api/extras/custom-fields/", w.toPayload(), &created); err != nil {
		return nil, err
	}
	return created.toNBCustomField(), nil
}

// UpdateCustomField PATCHes extras.CustomField. Type and name are left
// alone — Netbox rejects changing type on an existing field.
func (nb *NetboxClient) UpdateCustomField(id uint, changes map[string]any) (*NBCustomField, error) {
	if id == 0 {
		return nil, fmt.Errorf("netbox: update custom field: id is 0")
	}
	var updated restCustomField
	if err := nb.restPatchOut("/api/extras/custom-fields/"+strconv.FormatUint(uint64(id), 10)+"/", changes, &updated); err != nil {
		return nil, err
	}
	return updated.toNBCustomField(), nil
}
