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
	tx, res := s.take(id, "s")
	if res != txTaken || tx.state != "s" || tx.nonce != "n" || tx.verifier != "v" || tx.returnPath != "/x" {
		t.Fatalf("take = %+v, %v", tx, res)
	}
	if _, res := s.take(id, "s"); res != txMissing {
		t.Fatal("a transaction was taken twice")
	}
	if _, res := s.take("unknown", ""); res != txMissing {
		t.Fatal("an unknown ID was accepted")
	}
}

func TestTransactionsExpire(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Now())
	s := newTransactions(clk, 10)
	id, _ := s.put(transaction{state: "s"})
	clk.Step(10*time.Minute + time.Second)
	if _, res := s.take(id, "s"); res != txMissing {
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
	if _, res := s.take(ids[0], "0"); res != txMissing {
		t.Error("the oldest transaction was not evicted")
	}
	for i, id := range ids[1:] {
		if _, res := s.take(id, fmt.Sprint(i+1)); res != txTaken {
			t.Errorf("transaction %s was evicted", id)
		}
	}
	if newTransactions(clocktesting.NewFakeClock(time.Now()), 0).max != maxTransactions {
		t.Error("the default bound is not 10,000")
	}
}

// Concurrent callbacks for one transaction: exactly one takes it.
func TestTransactionsConcurrent(t *testing.T) {
	s := newTransactions(clocktesting.NewFakeClock(time.Now()), 100)
	id, _ := s.put(transaction{state: "s"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range 16 {
		wg.Go(func() {
			state := "s"
			if i%2 == 1 {
				state = "forged"
			}
			if _, res := s.take(id, state); res == txTaken {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d callbacks took one transaction, want exactly 1", wins)
	}
}

// A state mismatch leaves the transaction open; the right state then takes it.
func TestTransactionsStateMismatch(t *testing.T) {
	s := newTransactions(clocktesting.NewFakeClock(time.Now()), 10)
	id, _ := s.put(transaction{state: "s", returnPath: "/x"})
	if _, res := s.take(id, "forged"); res != txMismatch {
		t.Fatalf("forged state = %v, want a mismatch", res)
	}
	if tx, res := s.take(id, "s"); res != txTaken || tx.returnPath != "/x" {
		t.Fatalf("real state = %+v, %v", tx, res)
	}
	if _, res := s.take(id, "s"); res != txMissing {
		t.Fatalf("second take = %v, want missing", res)
	}
}
