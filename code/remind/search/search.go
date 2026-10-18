// Package search finds reminders by their text. Each owner's documents
// are indexed and ranked separately.
package search

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
)

// A Hit is one search result.
type Hit struct {
	DocID string
	Score float64
}

// An Index is a full-text index of documents, each belonging to one
// owner. It is safe for concurrent use.
type Index struct {
	mu     sync.RWMutex
	owners map[string]*shard // by owner
	docs   map[string]string // doc ID to owner
}

// A shard holds one owner's documents.
type shard struct {
	docs     map[string]*doc
	postings map[string]map[string]int // term to doc ID to count
}

type doc struct {
	terms  map[string]int // term to count
	length int            // number of terms
}

// New returns an empty index.
func New() *Index {
	return &Index{
		owners: make(map[string]*shard),
		docs:   make(map[string]string),
	}
}

// Add indexes text as the document docID belonging to owner, replacing
// any document already indexed under docID.
func (ix *Index) Add(docID, owner, text string) {
	terms := tokenize(text)
	d := &doc{terms: make(map[string]int), length: len(terms)}
	for _, t := range terms {
		d.terms[t]++
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.remove(docID)
	sh := ix.owners[owner]
	if sh == nil {
		sh = &shard{
			docs:     make(map[string]*doc),
			postings: make(map[string]map[string]int),
		}
		ix.owners[owner] = sh
	}
	sh.docs[docID] = d
	for t, n := range d.terms {
		p := sh.postings[t]
		if p == nil {
			p = make(map[string]int)
			sh.postings[t] = p
		}
		p[docID] = n
	}
	ix.docs[docID] = owner
}

// Remove drops a document from the index. Removing an unknown document
// does nothing.
func (ix *Index) Remove(docID string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.remove(docID)
}

func (ix *Index) remove(docID string) {
	owner, ok := ix.docs[docID]
	if !ok {
		return
	}
	delete(ix.docs, docID)
	sh := ix.owners[owner]
	for t := range sh.docs[docID].terms {
		delete(sh.postings[t], docID)
		if len(sh.postings[t]) == 0 {
			delete(sh.postings, t)
		}
	}
	delete(sh.docs, docID)
	if len(sh.docs) == 0 {
		delete(ix.owners, owner)
	}
}

// Search returns owner's documents that match query, best first, with
// ties in doc ID order. The last word of the query also matches longer
// words that start with it, so "dent" finds "dentist". A limit of zero
// or less returns every match.
func (ix *Index) Search(owner, query string, limit int) []Hit {
	words := tokenize(query)
	if len(words) == 0 {
		return nil
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()
	sh := ix.owners[owner]
	if sh == nil {
		return nil
	}

	matched := make(map[string]bool)
	for _, w := range words[:len(words)-1] {
		if sh.postings[w] != nil {
			matched[w] = true
		}
	}
	prefix := words[len(words)-1]
	for t := range sh.postings {
		if strings.HasPrefix(t, prefix) {
			matched[t] = true
		}
	}

	n := float64(len(sh.docs))
	scores := make(map[string]float64)
	for _, t := range slices.Sorted(maps.Keys(matched)) {
		p := sh.postings[t]
		idf := math.Log(1 + n/float64(len(p)))
		for id, count := range p {
			scores[id] += float64(count) / float64(sh.docs[id].length) * idf
		}
	}

	hits := make([]Hit, 0, len(scores))
	for id, s := range scores {
		hits = append(hits, Hit{DocID: id, Score: s})
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return strings.Compare(a.DocID, b.DocID)
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
