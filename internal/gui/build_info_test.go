package gui

import (
	"encoding/json"
	"testing"
)

func TestBuildInfoUsesFrontendFieldNames(t *testing.T) {
	info := (&BuildInfoService{Version: "v1.0.1", BuildTime: "2026-10-13T20:00:00Z"}).BuildInfo()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"version":"v1.0.1","buildTime":"2026-10-13T20:00:00Z"}` {
		t.Fatalf("frontend build information: %s", data)
	}
}
