import styled from "@emotion/styled";
import { ExternalLink } from "lucide-react";
import { useSearchParams } from "react-router-dom";
import { authCopy, SECURE_MODE_DOCS } from "../copy/auth";
import { PageState } from "../states/PageState";
import { clearSessionFlags } from "./authState";
import { navigate } from "./navigate";

// region Styles
export const PrimaryButton = styled.button`
  padding: 8px 16px;
  cursor: pointer;
  font: inherit;
  font-weight: ${(p) => p.theme.font.weight.semibold};
  border-radius: ${(p) => p.theme.radius.sm};
  border: 1px solid ${(p) => p.theme.color.accent};
  background: ${(p) => p.theme.color.accent};
  color: ${(p) => p.theme.color.headerText};
  /* Darkening keeps the light text readable in both themes. */
  &:hover {
    background: color-mix(in srgb, #000 22%, ${(p) => p.theme.color.accent});
  }
  &:active {
    background: color-mix(in srgb, #000 40%, ${(p) => p.theme.color.accent});
  }
`;
const DocLink = styled.a`
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: ${(p) => p.theme.color.accentText};
  &:hover {
    text-decoration-thickness: 2px;
  }
  &:active {
    opacity: 0.8;
  }
`;

// region Components
/** The login route for a new attempt; the return path is the root, never the failure page itself. */
const NEW_LOGIN = "/auth/login?return=%2F";

/** Shown while the browser is sent to the sign-in provider. */
export function RedirectingPage() {
  return <PageState kind="connecting" title={authCopy.redirecting} />;
}

/** Shown when /auth/status reports signIn: not-configured. */
export function NotConfiguredPage() {
  return (
    <PageState
      kind="empty"
      title={authCopy.notConfiguredTitle}
      message={
        <>
          {authCopy.notConfiguredBody}
          <br />
          <DocLink href={SECURE_MODE_DOCS} target="_blank" rel="noreferrer">
            {authCopy.notConfiguredLink}
            <ExternalLink size={14} aria-hidden="true" />
          </DocLink>
        </>
      }
    />
  );
}

/** Shown when /auth/status reports signIn: unavailable; "Try again" reloads, which asks again. */
export function UnavailablePage() {
  return (
    <PageState
      kind="error"
      title={authCopy.unavailableTitle}
      message={authCopy.unavailableBody}
      action={<PrimaryButton onClick={() => window.location.reload()}>{authCopy.tryAgain}</PrimaryButton>}
    />
  );
}

/** The /signin-failed route; the callback's reason picks the page. */
export function SignInFailedPage() {
  const [params] = useSearchParams();
  return <FailedPage reason={params.get("reason")} />;
}

/** The failure page for a reason: unauthenticated means the cluster refuses the sign-in. */
export function FailedPage({ reason }: { reason: string | null }) {
  const notAccepted = reason === "unauthenticated";
  return (
    <PageState
      kind="error"
      title={notAccepted ? authCopy.notAcceptedTitle : authCopy.failedTitle}
      message={notAccepted ? authCopy.notAcceptedBody : undefined}
      action={<PrimaryButton onClick={() => navigate(NEW_LOGIN)}>{authCopy.tryAgain}</PrimaryButton>}
    />
  );
}

/** The signed-out page, after a session ended or a sign-out; no automatic redirect, the person chooses. */
export function SessionEndedPage({ login }: { login: string }) {
  const signInAgain = () => {
    clearSessionFlags();
    navigate(login);
  };
  return (
    <PageState
      kind="empty"
      title={authCopy.endedTitle}
      action={<PrimaryButton onClick={signInAgain}>{authCopy.signInAgain}</PrimaryButton>}
    />
  );
}
