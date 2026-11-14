import { css } from "@emotion/react";
import styled from "@emotion/styled";
import { ChevronDown, CircleUser, LogOut, UserRound } from "lucide-react";
import { useCallback, useState } from "react";
import { ApiError } from "../api/client";
import { authCopy } from "../copy/auth";
import { Anchor, Menu, MenuButton, useMenu } from "../shell/menu";
import { Tooltip } from "../shell/Tooltip";
import { PageNotice } from "../states/PageNotice";
import { Shimmer } from "../states/Shimmer";
import { AccountDetails } from "./AccountDetails";
import { useAuthState } from "./authState";
import { navigate, safeNext } from "./navigate";
import { useMe } from "./useAuth";

// region Styles
/** The slot keeps this width while the name loads, so the header does not shift. */
const Slot = styled(Anchor)`
  flex: none;
  min-width: 120px;
  max-width: 220px;
  justify-content: flex-end;
`;
const Name = styled.span`
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const noticePos = css`
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  z-index: 30;
  width: max-content;
  max-width: 320px;
  margin: 0;
`;
const fill = css`
  width: 100%;
`;

// region Component
/** Header control for secure mode: the username opening a menu with Account details and Sign out. signedIn is undefined while /auth/status is unknown. */
export function UserMenu({ signedIn }: { signedIn: boolean | undefined }) {
  // An ended session drops the cached identity, so the old name does not stay in the header.
  const ended = useAuthState().kind === "ended";
  const { data: me } = useMe(signedIn === true && !ended);
  const menu = useMenu();
  const [details, setDetails] = useState(false);
  const [logoutError, setLogoutError] = useState<ApiError | null>(null);
  const { buttonRef } = menu;
  const closeDetails = useCallback(() => {
    setDetails(false);
    buttonRef.current?.focus();
  }, [buttonRef]);

  const signOut = async () => {
    menu.close(false);
    setLogoutError(null);
    try {
      const res = await fetch("/auth/logout", { method: "POST", headers: { Accept: "application/json" } });
      if (!res.ok) return setLogoutError(new ApiError(res.status, res.statusText || `status ${res.status}`));
      const { next } = (await res.json()) as { next?: string };
      navigate(safeNext(next));
    } catch (e) {
      setLogoutError(new ApiError(0, (e as Error).message));
    }
  };

  if (!me) return <Slot>{signedIn !== false && !ended ? <Shimmer h={32} stylesOverride={fill} /> : null}</Slot>;
  return (
    <Slot ref={menu.anchorRef}>
      <Tooltip label={me.username} placement="end">
        <MenuButton
          ref={menu.buttonRef}
          type="button"
          aria-label={me.username}
          aria-haspopup="menu"
          aria-expanded={menu.open}
          onClick={() => {
            setLogoutError(null);
            menu.toggle();
          }}
        >
          <CircleUser size={14} aria-hidden="true" />
          <Name>{me.username}</Name>
          <ChevronDown size={14} aria-hidden="true" />
        </MenuButton>
      </Tooltip>
      {menu.open ? (
        <Menu role="menu" aria-label={me.username} ref={menu.menuRef} onKeyDown={menu.onMenuKeyDown}>
          <button
            type="button"
            role="menuitem"
            tabIndex={-1}
            onClick={() => {
              menu.close(false);
              setDetails(true);
            }}
          >
            <UserRound size={14} aria-hidden="true" />
            {authCopy.accountDetails}
          </button>
          <button type="button" role="menuitem" tabIndex={-1} onClick={() => void signOut()}>
            <LogOut size={14} aria-hidden="true" />
            {authCopy.signOut}
          </button>
        </Menu>
      ) : null}
      {logoutError ? (
        <PageNotice error={logoutError} title="" text={authCopy.signOutFailed} stylesOverride={noticePos} />
      ) : null}
      {details ? <AccountDetails me={me} onClose={closeDetails} /> : null}
    </Slot>
  );
}
