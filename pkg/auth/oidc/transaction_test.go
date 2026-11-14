package oidc

import (
	"fmt"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestTransactionsOneUse(t *testing.T) {
	s := newTransactions(clocktesting.NewFakeClock(time.Now()), 10)
	id, err := s.put(transaction{state: "s", nonce: "n", verifier: "v", returnPath: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("id %q: want 128 bits as 32 hex characters", id)
	}
	tx, ok := s.take(id)
	if !ok || tx.state != "s" || tx.nonce != "n" || tx.verifier != "v" || tx.returnPath != "/x" {
		t.Fatalf("take = %+v, %v", tx, ok)
	}
	if _, ok := s.take(id); ok {
		t.Fatal("a transaction was taken twice")
	}
	if _, ok := s.take("unknown"); ok {
		t.Fatal("an unknown ID was accepted")
	}
}

func TestTransactionsExpire(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Now())
	s := newTransactions(clk, 10)
	id, _ := s.put(transaction{state: "s"})
	clk.Step(10*time.Minute + time.Second)
	if _, ok := s.take(id); ok {
		t.Fatal("an expired transaction was accepted")
	}
	// Expired records are dropped when new ones arrive.
	_, _ = s.put(transaction{state: "a"})
	clk.Step(11 * time.Minute)
	_, _ = s.put(transaction{state: "b"})
	if n := s.len(); n != 1 {
		t.Errorf("open transactions = %d, want 1", n)
	}
}

func TestTransactionsEvictOldest(t *testing.T) {
	s := newTransactions(clocktesting.NewFakeClock(time.Now()), 3)
	var ids []string
	for i := range 4 {
		id, _ := s.put(transaction{state: fmt.Sprint(i)})
		ids = append(ids, id)
	}
	if _, ok := s.take(ids[0]); ok {
		t.Error("the oldest transaction was not evicted")
	}
	for _, id := range ids[1:] {
		if _, ok := s.take(id); !ok {
			t.Errorf("transaction %s was evicted", id)
		}
	}
	if newTransactions(clocktesting.NewFakeClock(time.Now()), 0).max != maxTransactions {
		t.Error("the default bound is not 10,000")
	}
}

func TestTransactionsConcurrent(t *testing.T) {
	s := newTransactions(clocktesting.NewFakeClock(time.Now()), 100)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				id, err := s.put(transaction{state: "s"})
				if err != nil {
					t.Error(err)
					return
				}
				s.take(id)
			}
		})
	}
	wg.Wait()
}
