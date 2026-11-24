import type { HealthState } from "../theme/tokens";
import type { Condition, LinkedRef, Page } from "./exploreTypes";

/** Mirrors pkg/api/policies_types.go (Phase 8 design §6.2, §6.3). */

/** Where a policy value comes from (design §3.2); every value carries one. */
export type PolicySource =
  | "declared"
  | "controller-reported"
  | "controller-reported-contribution"
  | "inferred"
  | "unknown";

/** A target check (design §5.2). */
export type PolicyCheckOutcome = "found" | "not-found" | "section-not-found" | "unknown";

/** Why an ancestor entry is left out of the summaries (design §6.4). */
export type PolicyNotCurrent = "other-controller" | "not-owned" | "stale" | "pending" | "not-fresh" | "unreadable";

/** One targetRef or targetSelector as written. ref and health are set on a detail's targetRefs only. */
export interface PolicyTarget {
  index: number;
  group: string;
  kind: string;
  name?: string;
  sectionName?: string;
  selector?: boolean;
  matchLabels?: Record<string, string>;
  source: PolicySource;
  check?: { outcome: PolicyCheckOutcome; source: PolicySource };
  ref?: LinkedRef;
  health?: HealthState;
  healthSource?: PolicySource;
}

/** The worst Accepted over the current ancestors (inferred). */
export interface PolicyAccepted {
  status: string;
  reason: string;
  source: PolicySource;
}

/** A current ancestor that reports Attached=True. */
export interface PolicyAttached {
  group: string;
  kind: string;
  namespace?: string;
  name: string;
  sectionName?: string;
  port?: number;
  source: PolicySource;
}

/** One spec section; value is sent on the detail only. */
export interface PolicyFeature {
  key: string;
  label?: string;
  value?: unknown;
  source: PolicySource;
}

export interface PolicyProblems {
  has: boolean;
  source: PolicySource;
}

/** One row of GET …/policies. */
export interface PolicyRow {
  ref: LinkedRef;
  targets: PolicyTarget[];
  accepted?: PolicyAccepted;
  /** The source of the Accepted summary, also when there is none (8.2 re-review). */
  acceptedSource: PolicySource;
  attachedOn: PolicyAttached[];
  /** The source of the Reported attached on summary, also when it is empty. */
  attachedOnSource: PolicySource;
  features: PolicyFeature[];
  problems: PolicyProblems;
}

export interface PolicyFacets {
  kinds: { group: string; kind: string; policies: number }[];
  features: { key: string; policies: number }[];
  namespaces: { namespace: string; policies: number }[];
  hasProblems: number;
}

/** GET …/policies: readable policies only, sorted by namespace, name, group and kind. */
export interface PoliciesResponse extends Page<PolicyRow> {
  facets: PolicyFacets;
}

/** The pack's reading of one ancestor condition; note is a key the client words. */
export interface PolicyReading {
  type: string;
  warning?: boolean;
  note?: "gateway-wide" | "backend-port" | string;
  source: PolicySource;
}

/** One status.ancestors entry, conditions verbatim. */
export interface PolicyAncestor {
  ref: LinkedRef;
  sectionName?: string;
  port?: number;
  health?: HealthState;
  healthSource?: PolicySource;
  controller: string;
  conditions: Condition[];
  staleSource: PolicySource;
  current: boolean;
  notCurrent?: PolicyNotCurrent | string;
  why?: string;
  currentSource: PolicySource;
  summary?: boolean;
  summarySource: PolicySource;
  readings: PolicyReading[];
  source: PolicySource;
}

export interface PolicyAnnotation {
  key: string;
  value: string;
  source: PolicySource;
}

/** GET …/policies/{group}/{kind}/{ns}/{name}. */
export interface PolicyDetail {
  install: string;
  ref: LinkedRef;
  yaml?: string;
  generation: number;
  interpreted: boolean;
  targets: PolicyTarget[];
  ancestors: PolicyAncestor[];
  accepted?: PolicyAccepted;
  acceptedSource: PolicySource;
  attachedOn: PolicyAttached[];
  attachedOnSource: PolicySource;
  features: PolicyFeature[];
  annotations: PolicyAnnotation[];
  problems: PolicyProblems;
  collectedAt: string;
}

/** One hop of a targets-parent relation: a parentRef as written, its link computed by name. */
export interface PolicyRelationHop {
  ref: LinkedRef;
  sectionName?: string;
  port?: number;
}

/** A policy related to a subject (design §6.5); groups are sorted by namespace and name, never precedence. */
export interface PolicyRelation {
  policy: LinkedRef;
  relation: "targets" | "targets-parent" | "attached-on-ancestor";
  source: PolicySource;
  sectionName?: string;
  port?: number;
  /** targets-parent only: the parentRefs walked; the policy names the last hop. */
  via?: PolicyRelationHop[];
  /** attached-on-ancestor only: the ancestor's Attached condition as written. */
  condition?: Condition;
  /** attached-on-ancestor only: the pack's granularity note key. */
  note?: "gateway-wide" | "backend-port" | string;
  noteSource?: PolicySource;
}

/** The selectors hint: policy kinds in the namespace whose targetSelectors name the subject's kind. */
export interface PolicySelectorsHint {
  namespace: string;
  kinds: { group: string; kind: string }[];
  source: PolicySource;
}

/** policyRelations on a Gateway, ListenerSet, route or backend detail; a group that does not apply is omitted. */
export interface PolicyRelations {
  targeting: PolicyRelation[];
  targetingParent?: PolicyRelation[];
  attachedHere?: PolicyRelation[];
  selectors: PolicySelectorsHint;
}
