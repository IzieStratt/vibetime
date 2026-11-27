package syncer

import (
	"strings"
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

// The transient cwd must never reach a submission bundle, even when prompt text is included.
func TestCwdNeverInSubmissionBundle(t *testing.T) {
	sy, srv, _, _ := setup(t)
	rel := 880
	for i := 0; i < 3; i++ {
		e := event.Event{
			Time: t0.Add(time.Duration(i) * time.Minute), Kind: event.Prompt, Project: "cs2",
			Repo: "github.com/rowan/cs2", PromptText: "fix the SECRETBODY flush retry in syncer", RelevancePM: &rel,
			Cwd: "/Users/rowan/private/cs2",
		}
		if _, err := sy.Store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := sy.Submit("cs2", "github.com/rowan/cs2", t0, t0.Add(time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	var bundle string
	if err := srv.DB.QueryRow(`SELECT bundle FROM submissions WHERE id = ?`, sub.ID).Scan(&bundle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundle, "SECRETBODY") {
		t.Fatal("opt-in prompt text should be present, so this test is checking the right bundle")
	}
	for _, leak := range []string{"cwd", "/Users/rowan", "private/cs2"} {
		if strings.Contains(bundle, leak) {
			t.Fatalf("submission bundle contains %q", leak)
		}
	}
}
