package sar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// Attributes are the resource attributes of one check.
type Attributes struct {
	Verb      string
	Group     string
	Resource  string
	Namespace string
	Name      string
}

// Checker asks whether an identity may perform a verb on a resource.
type Checker interface {
	// Check returns the decision, or an error when there is none; an error is never a deny.
	Check(ctx context.Context, id oidc.Identity, a Attributes) (bool, error)
}

// ErrCheck is a check without a decision. It wraps a fixed category and
// never names the identity.
var ErrCheck = errors.New("authorisation check failed")

// Check failure categories.
const (
	categoryTimeout    = "timeout"
	categoryRequest    = "request"
	categoryEvaluation = "evaluation-error"
	categoryLate       = "result-expired"
)

func checkError(category string) error { return fmt.Errorf("%w: %s", ErrCheck, category) }

// checkTimeout bounds one SubjectAccessReview, below the 30 s minimum TTL.
const checkTimeout = 10 * time.Second

// KubeChecker checks through the authorization.k8s.io/v1 SubjectAccessReview API.
type KubeChecker struct {
	kube    kubernetes.Interface
	timeout time.Duration
	log     *slog.Logger
}

// NewKubeChecker returns a Checker backed by the cluster's SubjectAccessReview API.
func NewKubeChecker(kube kubernetes.Interface) *KubeChecker {
	return &KubeChecker{kube: kube, timeout: checkTimeout, log: slog.Default()}
}

// Check implements Checker (design §6.3): allowed is allow, not allowed is
// deny, and an evaluation error, HTTP error or timeout is ErrCheck.
func (k *KubeChecker) Check(ctx context.Context, id oidc.Identity, a Attributes) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	spec := authzv1.SubjectAccessReviewSpec{
		User:   id.Username,
		UID:    id.UID,
		Groups: id.Groups,
		ResourceAttributes: &authzv1.ResourceAttributes{
			Verb: a.Verb, Group: a.Group, Resource: a.Resource, Namespace: a.Namespace, Name: a.Name,
		},
	}
	if len(id.Extra) > 0 {
		spec.Extra = make(map[string]authzv1.ExtraValue, len(id.Extra))
		for key, v := range id.Extra {
			spec.Extra[key] = authzv1.ExtraValue(v)
		}
	}
	res, err := k.kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{Spec: spec}, metav1.CreateOptions{})
	switch {
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil):
		return false, k.fail(categoryTimeout, a)
	case err != nil:
		return false, k.fail(categoryRequest, a)
	case res.Status.EvaluationError != "":
		return false, k.fail(categoryEvaluation, a)
	}
	return res.Status.Allowed, nil
}

// fail logs a failed check by category and attributes, never the identity.
func (k *KubeChecker) fail(category string, a Attributes) error {
	k.log.Warn("authorisation check failed", "category", category, "verb", a.Verb, "group", a.Group, "resource", a.Resource)
	return checkError(category)
}
