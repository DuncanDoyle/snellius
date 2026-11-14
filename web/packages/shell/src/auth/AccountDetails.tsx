import styled from "@emotion/styled";
import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { authCopy } from "../copy/auth";
import type { Me } from "./useAuth";

// region Styles
const Backdrop = styled.div`
  position: fixed;
  inset: 0;
  z-index: 1100;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 15vh;
  background: rgba(0, 0, 0, 0.5);
`;
const Panel = styled.div`
  width: min(480px, calc(100vw - 32px));
  max-height: 70vh;
  overflow: auto;
  padding: ${(p) => p.theme.space(4)};
  background: ${(p) => p.theme.color.surfaceRaised};
  color: ${(p) => p.theme.color.text};
  border: 1px solid ${(p) => p.theme.color.borderStrong};
  border-radius: ${(p) => p.theme.radius.md};
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
  h2 {
    margin: 0;
    font-size: ${(p) => p.theme.font.size.lg};
    font-weight: ${(p) => p.theme.font.weight.semibold};
  }
  dt {
    margin-top: ${(p) => p.theme.space(3)};
    color: ${(p) => p.theme.color.textMuted};
    font-size: ${(p) => p.theme.font.size.sm};
  }
  dd {
    margin: 4px 0 0;
    font-family: ${(p) => p.theme.font.mono};
    font-size: ${(p) => p.theme.font.size.code};
    overflow-wrap: anywhere;
  }
  ul {
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
  }
`;
const Head = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
`;
const CloseButton = styled.button`
  display: inline-flex;
  padding: 6px;
  cursor: pointer;
  color: ${(p) => p.theme.color.text};
  background: transparent;
  border: 1px solid transparent;
  border-radius: ${(p) => p.theme.radius.sm};
  &:hover {
    border-color: ${(p) => p.theme.color.borderStrong};
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 8%, ${(p) => p.theme.color.surfaceRaised});
  }
  &:active {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 16%, ${(p) => p.theme.color.surfaceRaised});
  }
`;

// region Component
/** Modal listing the signed-in username and Kubernetes groups; Escape, the close button or the backdrop closes it. */
export function AccountDetails({ me, onClose }: { me: Me; onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => closeRef.current?.focus(), []);
  return createPortal(
    <Backdrop
      onPointerDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <Panel
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-details-title"
        onKeyDown={(e) => {
          if (e.key === "Escape") onClose();
        }}
      >
        <Head>
          <h2 id="account-details-title">{authCopy.accountDetails}</h2>
          <CloseButton ref={closeRef} type="button" aria-label={authCopy.close} onClick={onClose}>
            <X size={16} aria-hidden="true" />
          </CloseButton>
        </Head>
        <dl>
          <dt>{authCopy.usernameLabel}</dt>
          <dd>{me.username}</dd>
          <dt>{authCopy.groupsLabel}</dt>
          <dd>
            {me.groups.length === 0 ? (
              authCopy.noGroups
            ) : (
              <ul>
                {me.groups.map((g) => (
                  <li key={g}>{g}</li>
                ))}
              </ul>
            )}
          </dd>
        </dl>
      </Panel>
    </Backdrop>,
    document.body,
  );
}
