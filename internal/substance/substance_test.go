package substance

import "testing"

func TestTokens(t *testing.T) {
	got := Tokens("The 12abc retry_loop, naïve Flush!")
	want := []string{"abc", "retry_loop", "flush"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestRelevance(t *testing.T) {
	v := Vocab{}
	v.Add("func Flush retries the syncer queue")
	if r := Relevance("fix syncer flush retries", v); r != 750 {
		t.Fatalf("relevance %d, want 750", r)
	}
	if r := Relevance("continue", v); r != 0 {
		t.Fatalf("relevance %d, want 0", r)
	}
	if r := Relevance("", v); r != 0 {
		t.Fatal(r)
	}
}

func TestTrivial(t *testing.T) {
	if !Trivial(19, 900) || !Trivial(200, 149) || Trivial(20, 150) {
		t.Fatal("boundaries")
	}
	if Trivial(200, -1) || !Trivial(5, -1) {
		t.Fatal("unknown relevance must fall back to length")
	}
}
