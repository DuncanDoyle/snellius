package oidc

import (
	"context"
	"errors"
	"fmt"
	"slices"

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

// TokenReview refusals; any other Review error is a failure to ask.
var (
	ErrUnauthenticated = errors.New("token review: not authenticated")
	ErrAudience        = errors.New("token review: the client ID is not an accepted audience")
)

// KubeReviewer reviews tokens through the authentication.k8s.io/v1 TokenReview API.
type KubeReviewer struct {
	kube     kubernetes.Interface
	clientID string
}

// NewKubeReviewer returns a Reviewer that requires clientID as the token's audience.
func NewKubeReviewer(kube kubernetes.Interface, clientID string) *KubeReviewer {
	return &KubeReviewer{kube: kube, clientID: clientID}
}

// Review creates a TokenReview for the client ID audience. It returns the
// identity only when the token is authenticated for that audience.
func (k *KubeReviewer) Review(ctx context.Context, idToken string) (Identity, error) {
	tr, err := k.kube.AuthenticationV1().TokenReviews().Create(ctx, &authnv1.TokenReview{
		Spec: authnv1.TokenReviewSpec{Token: idToken, Audiences: []string{k.clientID}},
	}, metav1.CreateOptions{})
	if err != nil {
		return Identity{}, fmt.Errorf("token review: %w", err)
	}
	st := tr.Status
	if !st.Authenticated || st.User.Username == "" {
		return Identity{}, ErrUnauthenticated
	}
	if !slices.Contains(st.Audiences, k.clientID) {
		return Identity{}, ErrAudience
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
