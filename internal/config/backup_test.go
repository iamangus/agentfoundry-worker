package config

import (
	"encoding/json"
	"testing"
)

func TestBackupExecutionProjection(t *testing.T) {
	const wire = `{"agent_id":"a","provider_id":"primary","backup_provider_id":"backup","backup_model":"alternate","kind":"agent","name":"test","system_prompt":"help"}`
	var def Definition
	if err := json.Unmarshal([]byte(wire), &def); err != nil {
		t.Fatal(err)
	}
	if def.BackupProviderID != "backup" || def.BackupModel != "alternate" {
		t.Fatalf("lost backup projection: %+v", def)
	}
	result, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(result, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["backup_provider_id"] != "backup" || fields["backup_model"] != "alternate" {
		t.Fatalf("lost wire fields: %s", result)
	}
}
