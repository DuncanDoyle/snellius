/**
 * User-facing copy of the Policies list and policy detail (Phase 8, sub-phase 8.2). Strings marked (fixed) come from
 * docs/phases/phase-8/design.md; the rest were picked from five options each (recorded in the 8.2 completion record).
 * Never say "effective", "applied" or "active" about a policy here (design §3.2); policies.test.ts checks it.
 */
import type { PolicyCheckOutcome, PolicySource } from "../api/types";

type Noun = readonly [string, string];

// region List
export const policiesCopy = {
  title: "Policies", // (fixed) nav entry
  noun: ["policy", "policies"] as Noun,
  // (fixed, design §5.5) the list never reads as precedence.
  sortNote: "Sorted by namespace and name, not by precedence.",
  show: "Show",
  hasProblems: "Has problems", // (fixed, design §6.2)
  kinds: "Policy kinds",
  features: "Features",
  namespaces: "Policy namespaces",
  kindsChips: "Kind",
  featuresChips: "Feature",
  namespacesChips: "Namespace",
};

export const policyColumnCopy = {
  policy: "Policy",
  kind: "Kind",
  targets: "Targets",
  accepted: "Accepted",
  attachedOn: "Reported attached on", // (fixed, design §6.2)
  features: "Features",
};

// region Values
/** A selector as written (fixed form, design §5.4): "HTTPRoute labels app=cart". */
export function selectorText(kind: string, labels: Record<string, string> | undefined, section?: string): string {
  const pairs = Object.entries(labels ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join(",");
  return withSection(`${kind} labels ${pairs}`, section);
}

/** A targetRef as written: "HTTPRoute cart (section checkout)". */
export const refTargetText = (kind: string, name: string, section?: string) => withSection(`${kind} ${name}`, section);

const withSection = (text: string, section?: string) => (section ? `${text} (section ${section})` : text);

/** An ancestor or attached-on object: "Gateway gateway/public (section http)", or ", port 80" for a backend port. */
export function ancestorText(r: {
  kind: string;
  namespace?: string;
  name: string;
  sectionName?: string;
  port?: number;
}) {
  const name = r.namespace ? `${r.namespace}/${r.name}` : r.name;
  const at = r.sectionName ? ` (section ${r.sectionName})` : r.port ? ` (port ${r.port})` : "";
  return `${r.kind} ${name}${at}`;
}

export const checkCopy: Record<PolicyCheckOutcome, string> = {
  found: "Found",
  "not-found": "Not found",
  "section-not-found": "Section not found",
  unknown: "Not checked",
};

export const acceptedCopy = {
  yes: "Accepted",
  no: "Not accepted",
  /** No current ancestor reports Accepted. */
  none: "Not reported",
  /** A status that is neither True nor False, as written. */
  other: (status: string) => `Accepted: ${status}`,
};

export const attachedCopy = {
  none: "None reported",
};

// region Sources
/** The label of each source (design §3.2) and what it means, for its tooltip. */
export const sourceCopy: Record<PolicySource, { label: string; tip: string }> = {
  declared: { label: "Declared", tip: "Written in the policy's spec." },
  "controller-reported": {
    label: "Reported",
    tip: "Written by the controller in the policy's status. Shown as is.",
  },
  "controller-reported-contribution": {
    label: "Reported",
    tip: "Read from the proxy configuration the controller sent.",
  },
  inferred: {
    label: "Inferred",
    tip: "Worked out by Snellius from the spec or status.",
  },
  unknown: {
    label: "Unknown",
    tip: "Nothing can tell, or you cannot read what it needs.",
  },
};
/** The prefix of a source tag's accessible name: "Source: Declared". */
export const sourcePrefix = "Source";

// region Detail
export const policyDetailCopy = {
  summary: "Summary",
  accepted: "Accepted",
  attachedOn: "Reported attached on",
  problems: "Has problems",
  yes: "Yes",
  no: "No",
  targets: "Targets",
  noTargets: "No targets.",
  check: "Check",
  health: "Health",
  /** The label of an ancestor entry left out of the summaries. */
  summaries: "Left out",
  status: "Status per ancestor",
  noStatus: "No status reported.",
  controller: "Controller",
  configuration: "Configuration",
  noFeatures: "No configuration besides targets.",
  rawKeys: "Named by spec key.",
  annotations: "Annotations",
  // (fixed, design §5.5)
  annotationNote: "kgateway uses this to order policies. Snellius does not evaluate it.",
  /** The controller's own entry for missing targets (design §4.1). */
  summaryEntry: "Missing-target entry",
  summaryEntryTip: "The controller lists targets it cannot find here.",
  leftOut: "Not counted in Accepted or Reported attached on.",
};

/** Why an ancestor entry is left out of the summaries (design §6.3 item 2, §6.4). */
export const notCurrentCopy: Record<string, string> = {
  "other-controller": "Other controller", // (fixed)
  "not-owned": "Not this install's",
  stale: "Stale", // (fixed)
  "not-fresh": "Stale", // (fixed)
  unreadable: "You cannot view this ancestor",
};

/** The Merged/Overridden note, worded by granularity (fixed, design §6.3 item 2). */
export const readingNoteCopy: Record<string, string> = {
  "gateway-wide": "kgateway reports this for the whole Gateway. It does not say which routes or fields are affected.",
  "backend-port": "kgateway reports this for this backend port.",
};
