import type { ReactNode } from "react";
import { useAuthState } from "./authState";
import { isSecureMode } from "./mode";
import { FailedPage, NotConfiguredPage, RedirectingPage, SessionEndedPage, UnavailablePage } from "./SignInPages";
import { useSignInStatus } from "./useAuth";

/** What the shell renders in place of its pages while the sign-in blocks them; null means pages show. */
export function useSignInBlock(): ReactNode | null {
  const secure = isSecureMode();
  const auth = useAuthState();
  const { data: status } = useSignInStatus(secure);
  if (!secure) return null;
  if (auth.kind === "ended") return <SessionEndedPage login={auth.login} />;
  if (auth.kind === "loop") return <FailedPage reason="invalid" />;
  if (auth.kind === "redirecting") return <RedirectingPage />;
  if (status?.signIn === "not-configured") return <NotConfiguredPage />;
  if (status?.signIn === "unavailable") return <UnavailablePage />;
  return null;
}
