package health

import "github.com/DuncanDoyle/snellius/pkg/health/own"

// CodeRuleReplaced is KGWUI-015: the controller replaced or dropped a route rule (design §7.4).
// Package own reports it (D-106) under the same code.
const CodeRuleReplaced = "KGWUI-015"

// RuleReplacedHint ends a 015's detail once the rule has been replaced for
// longer than RuleReplacedHintAge (kgateway #14047).
const RuleReplacedHint = own.RuleReplacedHint

// RuleReplacedHintAge is how long a rule stays replaced before the hint shows.
const RuleReplacedHintAge = own.RuleReplacedHintAge
