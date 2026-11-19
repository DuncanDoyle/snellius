import { useCallback, useSyncExternalStore } from "react";

// region Helpers
/** localStorage key of the namespace scope: a JSON array of namespace names. */
export const NAMESPACE_SCOPE_KEY = "snellius.namespaceScope";

/** The most namespaces one scope may name, as the server allows. */
export const MAX_SCOPE = 100;

/** A Kubernetes namespace name (a DNS-1123 label), as the server checks it. */
export function isNamespaceName(s: string): boolean {
  return s.length <= 63 && /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(s);
}

/** Distinct valid names, sorted, at most MAX_SCOPE. */
function clean(names: readonly unknown[]): string[] {
  const valid = names.filter((n): n is string => typeof n === "string" && isNamespaceName(n));
  return [...new Set(valid)].sort().slice(0, MAX_SCOPE);
}

/** The stored scope; empty when absent, unreadable or storage is blocked. */
function read(): string[] {
  try {
    const raw = localStorage.getItem(NAMESPACE_SCOPE_KEY);
    const v: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(v) ? clean(v) : [];
  } catch {
    return [];
  }
}

// The scope of this page load, shared by the header and every list page; undefined until first read.
let current: string[] | undefined;
const listeners = new Set<() => void>();

/** The current scope; empty means all namespaces. */
export function getNamespaceScope(): string[] {
  if (current === undefined) current = read();
  return current;
}

/** Sets the scope and stores it; without storage it lasts for the page load. */
export function setNamespaceScope(names: readonly string[]) {
  current = clean(names);
  try {
    if (current.length === 0) localStorage.removeItem(NAMESPACE_SCOPE_KEY);
    else localStorage.setItem(NAMESPACE_SCOPE_KEY, JSON.stringify(current));
  } catch {
    // The choice still holds for this page load.
  }
  for (const l of listeners) l();
}

/** Reads storage again, as a new page load would. Tests only. */
export function reloadNamespaceScope() {
  current = undefined;
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

// region Component
/** The namespace scope and its setter; empty means all namespaces. */
export function useNamespaceScope(): [string[], (names: readonly string[]) => void] {
  const scope = useSyncExternalStore(subscribe, getNamespaceScope, getNamespaceScope);
  const set = useCallback((names: readonly string[]) => setNamespaceScope(names), []);
  return [scope, set];
}
