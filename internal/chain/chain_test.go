package chain

import (
	"testing"
	"time"

	"github.com/IzieStratt/vibetime/internal/event"
)

func build(t *testing.T, n int) []Link {
	prev := Genesis
	var out []Link
	for i := 0; i < n; i++ {
		e := event.Event{Time: time.Unix(int64(1000+i*60), 0).UTC(), Kind: event.Heartbeat, Project: "p"}
		h, err := Next(prev, e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, Link{e, h})
		prev = h
	}
	return out
}

func TestValidChain(t *testing.T) {
	if i, err := Verify(Genesis, build(t, 10)); i != -1 || err != nil {
		t.Fatalf("valid chain rejected at %d: %v", i, err)
	}
}

func TestTamperDetected(t *testing.T) {
	cases := map[string]func([]Link) []Link{
		"edited time":    func(l []Link) []Link { l[4].Event.Time = l[4].Event.Time.Add(-time.Hour); return l },
		"edited project": func(l []Link) []Link { l[2].Event.Project = "other"; return l },
		"dropped link":   func(l []Link) []Link { return append(l[:3], l[4:]...) },
		"inserted link": func(l []Link) []Link {
			fake := Link{Event: l[0].Event, Hash: l[0].Hash}
			return append(l[:1], append([]Link{fake}, l[1:]...)...)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if i, _ := Verify(Genesis, mutate(build(t, 10))); i == -1 {
				t.Fatal("tampering not detected")
			}
		})
	}
}

func TestDeterministic(t *testing.T) {
	a, b := build(t, 3), build(t, 3)
	if a[2].Hash != b[2].Hash {
		t.Fatal("non-deterministic hash")
	}
}

func FuzzVerify(f *testing.F) {
	f.Add("p", int64(0))
	f.Fuzz(func(t *testing.T, project string, ts int64) {
		e := event.Event{Time: time.Unix(ts, 0).UTC(), Kind: event.Prompt, Project: project}
		h, err := Next(Genesis, e)
		if err != nil {
			return
		}
		if i, err := Verify(Genesis, []Link{{e, h}}); i != -1 || err != nil {
			t.Fatal("round trip failed")
		}
	})
}

func TestRedactionPreservesHash(t *testing.T) {
	e := event.Event{Time: time.Unix(5, 0).UTC(), Kind: event.Prompt, Project: "p", PromptText: "secret"}
	h1, _ := Next(Genesis, e)
	h2, _ := Next(Genesis, e.Redacted())
	if h1 != h2 {
		t.Fatal("redacting changed the hash")
	}
	e.PromptText = "different"
	if h3, _ := Next(Genesis, e); h3 == h1 {
		t.Fatal("prompt content not committed")
	}
	if r := e.Redacted(); r.PromptText != "" || len(r.PromptSHA) != 64 {
		t.Fatalf("%+v", r)
	}
}
