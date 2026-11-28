package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// The web app and the CLI read not_restored as the reason text.
func TestPlanConfigNotRestoredIsAStringOmittedWhenEmpty(t *testing.T) {
	with, err := json.Marshal(planConfigView{Name: "app.env", NotRestored: "config copy expired"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"not_restored":"config copy expired"`) {
		t.Errorf("view = %s, want not_restored as the reason", with)
	}
	without, _ := json.Marshal(planConfigView{Name: "app.env"})
	if strings.Contains(string(without), "not_restored") {
		t.Errorf("view = %s, want not_restored omitted when empty", without)
	}
}
