import type { LinkedRef, ProcessSet } from "../../../api/types";
import { diagnosticsCopy, processesCopy } from "../../../copy/explorer";
import { ProcessTable } from "../../../diagnostics/ProcessTable";
import { Muted, refText } from "../listParts";

// region Component
interface ProxiesBodyProps {
  gateway: LinkedRef;
  proxies: ProcessSet;
}

/** Body of the Gateway's proxies card. Pods the caller may not view are left out by the server; the counts stay (§14 rule 16). */
export function ProxiesBody({ gateway, proxies }: ProxiesBodyProps) {
  const counts =
    proxies.observed && !proxies.restricted && proxies.ready !== undefined && proxies.desired !== undefined
      ? diagnosticsCopy.readyOf(proxies.ready, proxies.desired)
      : undefined;
  return (
    <>
      {counts ? <Muted>{counts}</Muted> : null}
      <ProcessTable set={proxies} label={processesCopy.proxiesOf(refText(gateway))} />
    </>
  );
}
