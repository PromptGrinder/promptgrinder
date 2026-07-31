package runfolder

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProgressEventJSONBackwardCompatibility(t *testing.T) {
	var old ProgressEvent
	if err := json.Unmarshal([]byte(`{"type":"prompt.started","sequence_id":"seq","prompt_name":"a.md","completed":0,"total":1}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Duration != 0 || old.Inventory != nil || old.Folder != "" {
		t.Fatalf("new optional fields have nonzero defaults: %#v", old)
	}

	data, err := json.Marshal(ProgressEvent{Type: "run.started", SequenceID: "seq", Completed: 0, Total: 1})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, field := range []string{"duration", "inventory", "folder"} {
		if strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("zero optional field %q serialized: %s", field, text)
		}
	}
}

func TestProgressEventJSONNewFieldsRoundTrip(t *testing.T) {
	want := ProgressEvent{Type: "run.started", SequenceID: "seq", Folder: "prompts", Duration: 1500 * time.Millisecond, Inventory: []ProgressPrompt{{Name: "a.md", Type: TypeReview, Status: "pending"}}, Total: 1}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ProgressEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Folder != want.Folder || got.Duration != want.Duration || len(got.Inventory) != 1 || got.Inventory[0] != want.Inventory[0] {
		t.Fatalf("round trip = %#v", got)
	}
}
