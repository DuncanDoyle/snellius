import { useMeta } from "../api/hooks";
import { isSecureMode } from "../auth/mode";
import { diagnosticsCopy } from "../copy/explorer";
import { NotChecked } from "../health/NotChecked";

// region Helpers
/** False when secure mode withholds delegation analysis from this viewer (D-114); /meta's limitedView alone decides. */
export function useDelegationChecked(): boolean {
  const { data } = useMeta();
  return !(isSecureMode() && data?.limitedView);
}

// region Component
/** "Not checked" in place of a delegation result the viewer's permissions do not cover. */
export function DelegationNotChecked() {
  return <NotChecked label={diagnosticsCopy.notChecked} reason={diagnosticsCopy.notCheckedReason} />;
}
