package oidc

import (
	"context"
	"errors"
	"fmt"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Identity is the Kubernetes identity the API server returns for a token.
type Identity struct {
	Username string
	UID      string
	Groups   []string
	Extra    map[string][]string
}

// Reviewer asks the cluster who an ID token belongs to (design §4.2).
type Reviewer interface {
	Review(ctx context.Context, idToken string) (Identity, error)
}

// ErrUnauthenticated is the TokenReview refusal; any other Review error is a failure to ask.
var ErrUnauthenticated = errors.New("token review: not authenticated")

// KubeReviewer reviews tokens through the authentication.k8s.io/v1 TokenReview API.
type KubeReviewer struct {
	kube kubernetes.Interface
}

// NewKubeReviewer returns a Reviewer backed by the cluster's TokenReview API.
func NewKubeReviewer(kube kubernetes.Interface) *KubeReviewer {
	return &KubeReviewer{kube: kube}
}

// Review returns the identity the API server gives the token, if it authenticates it.
// It sets no spec.audiences: the API server checks a JWT against its own configured
// audiences, and matches spec.audiences only against its ServiceAccount audiences. The
// binding to Snellius's client is the aud check in checkClaims, trusted once this passes.
func (k *KubeReviewer) Review(ctx context.Context, idToken string) (Identity, error) {
	tr, err := k.kube.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: idToken},
	}, metav1.CreateOptions{})
	if err != nil {
		return Identity{}, fmt.Errorf("token review: %w", err)
	}
	st := tr.Status
	if !st.Authenticated || st.User.Username == "" {
		return Identity{}, ErrUnauthenticated
	}
	id := Identity{Username: st.User.Username, UID: st.User.UID, Groups: st.User.Groups}
	if len(st.User.Extra) > 0 {
		id.Extra = make(map[string][]string, len(st.User.Extra))
		for key, v := range st.User.Extra {
			id.Extra[key] = []string(v)
		}
	}
	return id, nil
}
