import type { PolicyAccepted, PolicyFeature, PolicyTarget } from "../../api/types";
import { acceptedCopy, checkCopy, refTargetText, selectorText } from "../../copy/policies";
import type { HealthState } from "../../theme/tokens";

// Text and states of policy values, shared by the list and the detail.

/** A target as written: a selector as "HTTPRoute labels app=cart", a ref as "HTTPRoute cart (section checkout)". */
export function targetText(t: PolicyTarget): string {
  return t.selector
    ? selectorText(t.kind, t.matchLabels, t.sectionName)
    : refTargetText(t.kind, t.name ?? "", t.sectionName);
}

/** A target check that names a problem: the ref or its section is missing. */
export const checkFails = (t: PolicyTarget) =>
  t.check?.outcome === "not-found" || t.check?.outcome === "section-not-found";

/** A list cell's target: as written, with a failing check after it. */
export function targetCellText(t: PolicyTarget): string {
  return checkFails(t) && t.check ? `${targetText(t)}: ${checkCopy[t.check.outcome].toLowerCase()}` : targetText(t);
}

/** The icon state of a target check: missing is an error; found is healthy; not checked is unknown. */
export function checkState(t: PolicyTarget): HealthState {
  if (checkFails(t)) return "error";
  return t.check?.outcome === "found" ? "healthy" : "unknown";
}

/** The accepted summary in words, with the reason as written: "Accepted · Valid". */
export function acceptedText(a?: PolicyAccepted): string {
  if (!a) return acceptedCopy.none;
  const head =
    a.status === "True" ? acceptedCopy.yes : a.status === "False" ? acceptedCopy.no : acceptedCopy.other(a.status);
  return a.reason ? `${head} · ${a.reason}` : head;
}

/** The icon state of the accepted summary. */
export function acceptedState(a?: PolicyAccepted): HealthState {
  if (a?.status === "True") return "healthy";
  if (a?.status === "False") return "error";
  return "unknown";
}

/** A feature's name: the pack's label, else the spec key as written. */
export const featureName = (f: PolicyFeature) => f.label || f.key;

/** The kind filter's value: group/Kind, "core" for the empty group (the API's form). */
export const kindValue = (k: { group: string; kind: string }) => `${k.group || "core"}/${k.kind}`;
