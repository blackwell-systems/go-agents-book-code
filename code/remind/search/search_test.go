package search

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func ids(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.DocID
	}
	return out
}

func newIndex(docs map[string]string) *Index {
	ix := New()
	for id, text := range docs {
		ix.Add(id, "ana", text)
	}
	return ix
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{"Call the Dentist at 9", []string{"call", "dentist", "9"}},
		{"pick-up: milk, eggs!", []string{"pick", "up", "milk", "eggs"}},
		{"Lembrar de pagar a conta", []string{"lembrar", "pagar", "conta"}},
		{"Der Termin ist für Straße 5", []string{"termin", "straße", "5"}},
		{"ÉCOLE Café", []string{"école", "café"}},
		{"明天开会", []string{"明", "天", "开", "会"}},
		{"会議at 3pm", []string{"会", "議", "3pm"}},
		{"にほんご と カタカナ", []string{"に", "ほ", "ん", "ご", "と", "カ", "タ", "カ", "ナ"}},
		{"Ὀδυσσεύς", []string{"ὀδυσσεύς"}},
		{"", nil},
		{"the and of", nil},
	}
	for _, tt := range tests {
		if got := tokenize(tt.text); !slices.Equal(got, tt.want) {
			t.Errorf("tokenize(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestSearchRanking(t *testing.T) {
	ix := newIndex(map[string]string{
		"r1": "buy milk",
		"r2": "buy milk, more milk, milk",
		"r3": "buy bread for the party",
		"r4": "call mom",
	})
	got := ids(ix.Search("ana", "milk", 0))
	if want := []string{"r2", "r1"}; !slices.Equal(got, want) {
		t.Errorf("milk: %v, want %v", got, want)
	}
	// "bread" is rarer than "buy", so it outweighs it.
	got = ids(ix.Search("ana", "bread buy", 0))
	if want := []string{"r3", "r1", "r2"}; !slices.Equal(got, want) {
		t.Errorf("bread buy: %v, want %v", got, want)
	}
	if hits := ix.Search("ana", "taxes", 0); len(hits) != 0 {
		t.Errorf("taxes: %v, want none", hits)
	}
	if hits := ix.Search("ana", "the", 0); len(hits) != 0 {
		t.Errorf("stopword query: %v, want none", hits)
	}
}

func TestSearchTies(t *testing.T) {
	ix := newIndex(map[string]string{
		"b": "water plants",
		"c": "water plants",
		"a": "water plants",
	})
	hits := ix.Search("ana", "water", 0)
	if got, want := ids(hits), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("ties: %v, want %v", got, want)
	}
	if hits[0].Score <= 0 {
		t.Errorf("score = %v, want positive", hits[0].Score)
	}
}

func TestSearchPrefix(t *testing.T) {
	ix := newIndex(map[string]string{
		"r1": "call the dentist",
		"r2": "dental floss",
		"r3": "accident report",
		"r4": "dent in the car door",
	})
	got := ids(ix.Search("ana", "dent", 0))
	slices.Sort(got)
	if want := []string{"r1", "r2", "r4"}; !slices.Equal(got, want) {
		t.Errorf("dent: %v, want %v", got, want)
	}
	// Only the last word is a prefix.
	got = ids(ix.Search("ana", "dent car", 0))
	if want := []string{"r4"}; !slices.Equal(got, want) {
		t.Errorf("dent car: %v, want %v", got, want)
	}
	got = ids(ix.Search("ana", "call dent", 0))
	if len(got) != 3 || got[0] != "r1" {
		t.Errorf("call dent: %v, want r1 first of 3", got)
	}
}

func TestSearchOwners(t *testing.T) {
	ix := New()
	ix.Add("r1", "ana", "renew passport")
	ix.Add("r2", "bo", "renew passport")
	ix.Add("r3", "bo", "renew library books")
	if got := ids(ix.Search("ana", "renew", 0)); !slices.Equal(got, []string{"r1"}) {
		t.Errorf("ana: %v, want [r1]", got)
	}
	if got := ids(ix.Search("bo", "renew", 0)); !slices.Equal(got, []string{"r2", "r3"}) {
		t.Errorf("bo: %v, want [r2 r3]", got)
	}
	if hits := ix.Search("cy", "renew", 0); hits != nil {
		t.Errorf("cy: %v, want none", hits)
	}
}

func TestAddReplaceRemove(t *testing.T) {
	ix := New()
	ix.Add("r1", "ana", "book flights")
	ix.Add("r1", "ana", "book hotel")
	if hits := ix.Search("ana", "flights", 0); len(hits) != 0 {
		t.Errorf("old text still found: %v", hits)
	}
	if got := ids(ix.Search("ana", "hotel", 0)); !slices.Equal(got, []string{"r1"}) {
		t.Errorf("hotel: %v, want [r1]", got)
	}

	ix.Add("r1", "bo", "book hotel")
	if hits := ix.Search("ana", "hotel", 0); len(hits) != 0 {
		t.Errorf("doc moved to bo, but ana still finds it: %v", hits)
	}
	if got := ids(ix.Search("bo", "hotel", 0)); !slices.Equal(got, []string{"r1"}) {
		t.Errorf("bo: %v, want [r1]", got)
	}

	ix.Remove("r1")
	ix.Remove("missing")
	if hits := ix.Search("bo", "hotel", 0); len(hits) != 0 {
		t.Errorf("removed doc found: %v", hits)
	}
	if len(ix.owners) != 0 || len(ix.docs) != 0 {
		t.Errorf("index not empty after remove: %d owners, %d docs", len(ix.owners), len(ix.docs))
	}
}

func TestSearchUnicode(t *testing.T) {
	ix := newIndex(map[string]string{
		"r1": "明天下午开会",
		"r2": "Zahnarzttermin in der Straße",
		"r3": "Reunião com o médico",
	})
	tests := []struct{ query, want string }{
		{"开会", "r1"},
		{"STRASSE straße", "r2"},
		{"zahn", "r2"},
		{"MÉDICO", "r3"},
		{"reuni", "r3"},
	}
	for _, tt := range tests {
		if got := ids(ix.Search("ana", tt.query, 0)); !slices.Equal(got, []string{tt.want}) {
			t.Errorf("%q: %v, want [%s]", tt.query, got, tt.want)
		}
	}
}

func TestSearchLimit(t *testing.T) {
	ix := New()
	for i := range 10 {
		ix.Add(fmt.Sprintf("r%02d", i), "ana", "stretch")
	}
	tests := []struct{ limit, want int }{{3, 3}, {10, 10}, {20, 10}, {0, 10}, {-1, 10}}
	for _, tt := range tests {
		if got := len(ix.Search("ana", "stretch", tt.limit)); got != tt.want {
			t.Errorf("limit %d: %d hits, want %d", tt.limit, got, tt.want)
		}
	}
	if got := ids(ix.Search("ana", "stretch", 2)); !slices.Equal(got, []string{"r00", "r01"}) {
		t.Errorf("limit 2: %v, want [r00 r01]", got)
	}
}

func TestConcurrent(t *testing.T) {
	ix := New()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for i := range 50 {
				id := fmt.Sprintf("g%d-%d", g, i)
				ix.Add(id, "ana", "water the plants")
				ix.Search("ana", "plan", 5)
				if i%2 == 0 {
					ix.Remove(id)
				}
			}
		})
	}
	wg.Wait()
	if got := len(ix.Search("ana", "water", 0)); got != 100 {
		t.Errorf("%d docs left, want 100", got)
	}
}
