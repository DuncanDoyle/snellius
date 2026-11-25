package kgateway

import (
	"github.com/DuncanDoyle/snellius/pkg/kgateway/kgwpolicy"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// Precedence annotation keys kgateway orders policies by (Phase 8 design §5.5).
const (
	PolicyWeightAnnotation            = kgwpolicy.PolicyWeightAnnotation
	InheritedPolicyPriorityAnnotation = kgwpolicy.InheritedPolicyPriorityAnnotation
)

// PolicyInterpreter returns the kgateway pack's reading of its own policy kinds
// (Phase 8 design §8). It reads nothing about any other group.
func PolicyInterpreter() policy.Interpreter { return kgwpolicy.Interpreter() }
