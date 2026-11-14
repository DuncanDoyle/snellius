import useSWR from "swr";
import { type ApiError, apiFetch } from "../api/client";

export type SignInState = "ready" | "not-configured" | "unavailable";

interface StatusResponse {
  mode: "local" | "secure";
  signIn?: SignInState;
}

/** GET /auth/status once, in secure mode only; undefined until it answers. */
export function useSignInStatus(enabled: boolean) {
  return useSWR<StatusResponse, ApiError>(enabled ? "/auth/status" : null, apiFetch, {
    revalidateOnFocus: false,
    revalidateOnReconnect: false,
    shouldRetryOnError: false,
  });
}

export interface Me {
  username: string;
  groups: string[];
}

/** GET /auth/me while signed in; the key is null (no request) otherwise. */
export function useMe(enabled: boolean) {
  return useSWR<Me, ApiError>(enabled ? "/auth/me" : null, apiFetch, {
    revalidateOnFocus: false,
    shouldRetryOnError: false,
  });
}
