package sar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

const sarPath = "/apis/authorization.k8s.io/v1/subjectaccessreviews"

// fakeSARServer serves SubjectAccessReviews through handle and records the last request.
func fakeSARServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, got *authzv1.SubjectAccessReview)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != sarPath {
			http.NotFound(w, r)
			return
		}
		got := &authzv1.SubjectAccessReview{}
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		handle(w, r, got)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// answer writes got back with status.
func answer(w http.ResponseWriter, got *authzv1.SubjectAccessReview, status authzv1.SubjectAccessReviewStatus) {
	resp := got.DeepCopy()
	resp.APIVersion, resp.Kind = "authorization.k8s.io/v1", "SubjectAccessReview"
	resp.Status = status
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func kubeChecker(t *testing.T, host string) *KubeChecker {
	t.Helper()
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: host, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	return NewKubeChecker(kube)
}

func TestKubeCheckerRequest(t *testing.T) {
	var got authzv1.SubjectAccessReview
	srv := fakeSARServer(t, func(w http.ResponseWriter, _ *http.Request, sar *authzv1.SubjectAccessReview) {
		got = *sar
		answer(w, sar, authzv1.SubjectAccessReviewStatus{Allowed: true})
	})
	id := oidc.Identity{Username: "oidc:alice", UID: "u1", Groups: []string{"oidc:dev", "oidc:ops"}, Extra: map[string][]string{"scopes": {"a", "b"}}}
	at := Attributes{Verb: "get", Group: "gateway.networking.k8s.io", Resource: "httproutes", Namespace: "shop", Name: "web"}
	ok, err := kubeChecker(t, srv.URL).Check(context.Background(), id, at)
	if err != nil || !ok {
		t.Fatalf("Check() = %v, %v; want allowed", ok, err)
	}
	want := authzv1.SubjectAccessReviewSpec{
		User: "oidc:alice", UID: "u1", Groups: []string{"oidc:dev", "oidc:ops"},
		Extra: map[string]authzv1.ExtraValue{"scopes": {"a", "b"}},
		ResourceAttributes: &authzv1.ResourceAttributes{
			Verb: "get", Group: "gateway.networking.k8s.io", Resource: "httproutes", Namespace: "shop", Name: "web",
		},
	}
	if !reflect.DeepEqual(got.Spec, want) {
		t.Errorf("spec = %+v\nwant %+v", got.Spec, want)
	}
}

func TestKubeCheckerOutcomes(t *testing.T) {
	status := func(s authzv1.SubjectAccessReviewStatus) func(http.ResponseWriter, *http.Request, *authzv1.SubjectAccessReview) {
		return func(w http.ResponseWriter, _ *http.Request, sar *authzv1.SubjectAccessReview) { answer(w, sar, s) }
	}
	for name, c := range map[string]struct {
		handle  func(http.ResponseWriter, *http.Request, *authzv1.SubjectAccessReview)
		allowed bool
		errCat  string
	}{
		"allowed":                       {handle: status(authzv1.SubjectAccessReviewStatus{Allowed: true}), allowed: true},
		"denied":                        {handle: status(authzv1.SubjectAccessReviewStatus{})},
		"denied explicitly":             {handle: status(authzv1.SubjectAccessReviewStatus{Denied: true, Reason: "no"})},
		"evaluation error, not allowed": {handle: status(authzv1.SubjectAccessReviewStatus{EvaluationError: "webhook down"}), errCat: categoryEvaluation},
		"evaluation error, allowed":     {handle: status(authzv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "partial"}), errCat: categoryEvaluation},
		"HTTP 500": {handle: func(w http.ResponseWriter, _ *http.Request, _ *authzv1.SubjectAccessReview) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}, errCat: categoryRequest},
		"slower than the timeout": {handle: func(_ http.ResponseWriter, r *http.Request, _ *authzv1.SubjectAccessReview) {
			<-r.Context().Done() // returns once the client gives up
		}, errCat: categoryTimeout},
	} {
		t.Run(name, func(t *testing.T) {
			k := kubeChecker(t, fakeSARServer(t, c.handle).URL)
			k.timeout = 50 * time.Millisecond
			var logs bytes.Buffer
			k.log = slog.New(slog.NewTextHandler(&logs, nil))
			id := oidc.Identity{Username: "oidc:alice", Groups: []string{"oidc:secret-group"}}
			ok, err := k.Check(context.Background(), id, Attributes{Verb: "list", Resource: "services", Namespace: "shop"})
			if c.errCat == "" {
				if err != nil || ok != c.allowed {
					t.Fatalf("Check() = %v, %v; want %v", ok, err, c.allowed)
				}
				return
			}
			if ok || !errors.Is(err, ErrCheck) || !strings.HasSuffix(err.Error(), c.errCat) {
				t.Fatalf("Check() = %v, %v; want ErrCheck %q and no allow", ok, err, c.errCat)
			}
			if strings.Contains(err.Error(), "secret-group") || strings.Contains(err.Error(), "alice") {
				t.Errorf("the error names the identity: %v", err)
			}
			if !strings.Contains(logs.String(), "category="+c.errCat) || strings.Contains(logs.String(), "secret-group") {
				t.Errorf("log = %q; want the category and no groups", logs.String())
			}
		})
	}
}
