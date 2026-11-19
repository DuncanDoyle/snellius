package app

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// The SAR client has no client-side limit; the shared config keeps client-go's default.
func TestSARConfigUnthrottled(t *testing.T) {
	base := &rest.Config{Host: "https://example.invalid", QPS: 5, Burst: 10}
	c := sarConfig(base)
	if c.RateLimiter == nil {
		t.Fatal("the SAR config has no rate limiter set, so client-go's 5 QPS default applies")
	}
	for i := range 1000 {
		if !c.RateLimiter.TryAccept() {
			t.Fatalf("request %d throttled client-side", i)
		}
	}
	if base.RateLimiter != nil || base.QPS != 5 || base.Burst != 10 {
		t.Fatalf("the shared config changed: %+v", base)
	}
	kube, err := kubernetes.NewForConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if rl := kube.AuthorizationV1().RESTClient().GetRateLimiter(); rl != c.RateLimiter {
		t.Fatalf("the SAR client uses rate limiter %T, not the unlimited one", rl)
	}
}
