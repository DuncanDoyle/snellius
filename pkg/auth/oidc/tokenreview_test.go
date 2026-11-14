package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// fakeAPIServer answers TokenReviews with status and records the last request.
func fakeAPIServer(t *testing.T, status authnv1.TokenReviewStatus) (*httptest.Server, *authnv1.TokenReview) {
	t.Helper()
	got := &authnv1.TokenReview{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := got.DeepCopy()
		resp.APIVersion, resp.Kind = "authentication.k8s.io/v1", "TokenReview"
		resp.Status = status
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func kubeReviewer(t *testing.T, host string) *KubeReviewer {
	t.Helper()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: host, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	return NewKubeReviewer(kube, "snellius")
}

func TestKubeReviewer(t *testing.T) {
	user := authnv1.UserInfo{Username: "oidc:alice", UID: "u1", Groups: []string{"oidc:dev"}, Extra: map[string]authnv1.ExtraValue{"k": {"v"}}}
	t.Run("accepted", func(t *testing.T) {
		srv, got := fakeAPIServer(t, authnv1.TokenReviewStatus{Authenticated: true, User: user, Audiences: []string{"snellius"}})
		id, err := kubeReviewer(t, srv.URL).Review(context.Background(), "id-token")
		if err != nil {
			t.Fatal(err)
		}
		want := Identity{Username: "oidc:alice", UID: "u1", Groups: []string{"oidc:dev"}, Extra: map[string][]string{"k": {"v"}}}
		if !reflect.DeepEqual(id, want) {
			t.Errorf("identity = %+v, want %+v", id, want)
		}
		if got.Spec.Token != "id-token" || !reflect.DeepEqual(got.Spec.Audiences, []string{"snellius"}) {
			t.Errorf("request spec = %+v, want the token and audiences [snellius]", got.Spec)
		}
	})
	for name, c := range map[string]struct {
		status authnv1.TokenReviewStatus
		want   error
	}{
		"not authenticated":      {authnv1.TokenReviewStatus{Authenticated: false, Error: "invalid bearer token"}, ErrUnauthenticated},
		"missing audiences":      {authnv1.TokenReviewStatus{Authenticated: true, User: user}, ErrAudience},
		"wrong audience":         {authnv1.TokenReviewStatus{Authenticated: true, User: user, Audiences: []string{"kubernetes"}}, ErrAudience},
		"not authenticated wins": {authnv1.TokenReviewStatus{Authenticated: false, Audiences: []string{"snellius"}}, ErrUnauthenticated},
		"no username":            {authnv1.TokenReviewStatus{Authenticated: true, Audiences: []string{"snellius"}}, ErrUnauthenticated},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := fakeAPIServer(t, c.status)
			id, err := kubeReviewer(t, srv.URL).Review(context.Background(), "id-token")
			if !errors.Is(err, c.want) || !reflect.DeepEqual(id, Identity{}) {
				t.Fatalf("Review() = %+v, %v; want no identity and %v", id, err, c.want)
			}
		})
	}
	t.Run("transport failure", func(t *testing.T) {
		srv, _ := fakeAPIServer(t, authnv1.TokenReviewStatus{})
		srv.Close()
		_, err := kubeReviewer(t, srv.URL).Review(context.Background(), "id-token")
		if err == nil || errors.Is(err, ErrAudience) || errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Review() error = %v, want a transport error", err)
		}
		if strings.Contains(err.Error(), "id-token") {
			t.Error("the error holds the token")
		}
	})
}
