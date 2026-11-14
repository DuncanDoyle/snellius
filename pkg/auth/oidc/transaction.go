package oidc

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

// Login transaction bounds (design §4.1).
const (
	transactionTTL  = 10 * time.Minute
	maxTransactions = 10000
)

// transaction is one sign-in in progress.
type transaction struct {
	state, nonce, verifier, returnPath string
	created                            time.Time
}

// transactions holds the open sign-ins: one-use, expiring after
// transactionTTL, at most max open with the oldest evicted. Safe for concurrent use.
type transactions struct {
	clk clock.PassiveClock
	max int

	mu    sync.Mutex
	byID  map[string]*list.Element
	order *list.List // of *entry, oldest first
}

type entry struct {
	id string
	tx transaction
}

// newTransactions builds a store; max <= 0 means maxTransactions.
func newTransactions(clk clock.PassiveClock, max int) *transactions {
	if max <= 0 {
		max = maxTransactions
	}
	return &transactions{clk: clk, max: max, byID: map[string]*list.Element{}, order: list.New()}
}

// put stores tx under a new random 128-bit ID and returns the ID.
func (s *transactions) put(tx transaction) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	tx.created = s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for front := s.order.Front(); front != nil && (s.order.Len() >= s.max || s.expired(front.Value.(*entry).tx)); front = s.order.Front() {
		s.remove(front)
	}
	s.byID[id] = s.order.PushBack(&entry{id: id, tx: tx})
	return id, nil
}

// take removes and returns the transaction for id; false when it is unknown or expired.
func (s *transactions) take(id string) (transaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.byID[id]
	if !ok {
		return transaction{}, false
	}
	s.remove(el)
	tx := el.Value.(*entry).tx
	if s.expired(tx) {
		return transaction{}, false
	}
	return tx, true
}

func (s *transactions) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

func (s *transactions) expired(tx transaction) bool { return s.clk.Since(tx.created) > transactionTTL }

func (s *transactions) remove(el *list.Element) {
	delete(s.byID, el.Value.(*entry).id)
	s.order.Remove(el)
}
