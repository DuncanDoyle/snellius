import useSWR from "swr";
import { type ApiError, apiFetch } from "../api/client";
import { type StatusResponse, signInStatus } from "./authState";

/** /auth/status, in secure mode only; undefined until it answers. Shares the one request the API client waits for. */
export function useSignInStatus(enabled: boolean) {
  return useSWR<StatusResponse, Error>(enabled ? "/auth/status" : null, signInStatus, {
    revalidateOnFocus: false,
    revalidateOnReconnect: false,
    shouldRetryOnError: false,
  });
}

export interface Me {
  username: string;
  groups: string[];
}

/** GET /auth/me while signed in; the key is null (no request) otherwise. SWR revalidates it on focus only while the tab is visible. */
export function useMe(enabled: boolean) {
  return useSWR<Me, ApiError>(enabled ? "/auth/me" : null, apiFetch, {
    shouldRetryOnError: false,
  });
}
