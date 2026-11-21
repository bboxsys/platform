package apicontract

import (
	"encoding/json"
	"testing"
	"time"
)

type schemaInner struct {
	Count int `json:"count"`
}

type schemaOuter struct {
	schemaInner
	Name     string            `json:"name"`
	Note     string            `json:"note,omitempty"`
	When     time.Time         `json:"when"`
	Labels   map[string]string `json:"labels"`
	Items    []schemaInner     `json:"items"`
	Raw      json.RawMessage   `json:"raw"`
	Ignored  string            `json:"-"`
	internal int
}

func TestSchemaFollowsJSONTags(t *testing.T) {
	s := Schema[schemaOuter]()
	properties := s["properties"].(map[string]any)
	for _, name := range []string{"count", "name", "note", "when", "labels", "items", "raw"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("missing property %s in %v", name, properties)
		}
	}
	if _, ok := properties["Ignored"]; ok {
		t.Fatal("json:\"-\" field was described")
	}
	if properties["when"].(map[string]any)["format"] != "date-time" {
		t.Fatalf("time.Time is not date-time: %v", properties["when"])
	}
	required := s["required"].([]string)
	for _, name := range required {
		if name == "note" {
			t.Fatal("omitempty field marked required")
		}
	}
	if len(required) != 6 {
		t.Fatalf("required: %v", required)
	}
}
