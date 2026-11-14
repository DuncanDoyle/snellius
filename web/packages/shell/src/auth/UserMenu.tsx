import { css } from "@emotion/react";
import styled from "@emotion/styled";
import { ChevronDown, CircleUser, LogOut, UserRound } from "lucide-react";
import { useState } from "react";
import { authCopy } from "../copy/auth";
import { Anchor, Menu, MenuButton, useMenu } from "../shell/menu";
import { Tooltip } from "../shell/Tooltip";
import { Shimmer } from "../states/Shimmer";
import { AccountDetails } from "./AccountDetails";
import { navigate } from "./navigate";
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

const fill = css`
  width: 100%;
`;

// region Component
/** Header control for secure mode: the username opening a menu with Account details and Sign out. */
/** signedIn is undefined while /auth/status is unknown. */
export function UserMenu({ signedIn }: { signedIn: boolean | undefined }) {
  const { data: me } = useMe(signedIn === true);
  const menu = useMenu();
  const [details, setDetails] = useState(false);

  const signOut = async () => {
    menu.close(false);
    try {
      const res = await fetch("/auth/logout", { method: "POST", headers: { Accept: "application/json" } });
      const { next } = (await res.json()) as { next?: string };
      navigate(next ?? "/");
    } catch {
      // The server did not answer; the page stays as it was.
    }
  };

  if (!me) return <Slot>{signedIn !== false ? <Shimmer h={32} stylesOverride={fill} /> : null}</Slot>;
  return (
    <Slot ref={menu.anchorRef}>
      <Tooltip label={me.username} placement="end">
        <MenuButton
          ref={menu.buttonRef}
          type="button"
          aria-label={me.username}
          aria-haspopup="menu"
          aria-expanded={menu.open}
          onClick={menu.toggle}
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
      {details ? <AccountDetails me={me} onClose={() => setDetails(false)} /> : null}
    </Slot>
  );
}
