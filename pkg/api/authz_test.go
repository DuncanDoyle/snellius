package api

import (
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSpendCheckLimit(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/routes", nil)
	if err := SpendCheck(r); err != nil {
		t.Fatalf("SpendCheck outside a committed request = %v, want nil", err)
	}
	r, _ = withAuthzState(r)
	var wg sync.WaitGroup
	errs := make(chan error, CheckLimit)
	for range CheckLimit {
		wg.Go(func() { errs <- SpendCheck(r) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("check within the limit = %v", err)
		}
	}
	if err := SpendCheck(r); !errors.Is(err, ErrCheckLimit) {
		t.Fatalf("check %d = %v, want ErrCheckLimit", CheckLimit+1, err)
	}
}

func TestReportAuthzErrorKeepsFirst(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/routes", nil)
	ReportAuthzError(r, errors.New("ignored")) // no state: a no-op
	r, s := withAuthzState(r)
	first := errors.New("first")
	ReportAuthzError(r, first)
	ReportAuthzError(r, errors.New("second"))
	if s.err != first {
		t.Fatalf("recorded %v, want the first error", s.err)
	}
}
