import { createContext, useContext } from "react";
import { diagnosticsCopy } from "../copy/explorer";
import { NotChecked } from "../health/NotChecked";

// region Helpers
/**
 * True under a response that says delegationNotChecked (D-114). The page that renders the response provides it,
 * so each poll decides it, never a cached /meta.
 */
export const DelegationNotCheckedView = createContext(false);

/** Whether the response being rendered withholds delegation analysis. */
export const useDelegationNotChecked = () => useContext(DelegationNotCheckedView);

// region Component
/** "Not checked" in place of a delegation result the viewer's permissions do not cover. */
export function DelegationNotChecked() {
  return <NotChecked label={diagnosticsCopy.notChecked} reason={diagnosticsCopy.notCheckedReason} />;
}
