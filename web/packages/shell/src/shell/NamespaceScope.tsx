import styled from "@emotion/styled";
import { ChevronDown, Layers } from "lucide-react";
import { type FocusEvent, type KeyboardEvent, useCallback, useEffect, useId, useRef, useState } from "react";
import { useNamespaces } from "../api/hooks";
import { shellCopy } from "../copy/shell";
import { Anchor, MenuButton } from "./menu";
import { isNamespaceName, MAX_SCOPE, useNamespaceScope } from "./namespaceScopeStore";
import { Tooltip } from "./Tooltip";

const copy = shellCopy.namespaceScope;

// region Styles
/** The button keeps one width whatever is chosen, so the header never shifts; narrow windows get the icon only. */
const NARROW = "(max-width: 1299px)";
const Fixed = styled(Anchor)`
  flex: none;
`;
const Trigger = styled(MenuButton)`
  position: relative;
  width: 184px;
  justify-content: flex-start;
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-align: left;
  }
  svg {
    flex: none;
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: 2px;
  }
  /* The count marks an active scope where the label is hidden; the label says it where it shows. */
  .count {
    display: none;
  }
  @media ${NARROW} {
    width: 32px;
    padding: 0;
    justify-content: center;
    .label,
    .chevron {
      display: none;
    }
    .count {
      position: absolute;
      top: -6px;
      right: -6px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-width: 16px;
      height: 16px;
      padding: 0 3px;
      border-radius: 8px;
      font-size: 11px;
      font-weight: ${(p) => p.theme.font.weight.semibold};
      line-height: 1;
      &::after {
        content: attr(data-count);
      }
      /* The header's own pair inverted: readable in every theme. */
      background: ${(p) => p.theme.color.headerText};
      color: ${(p) => p.theme.color.headerBg};
    }
    &[data-scoped="true"] {
      border-color: ${(p) => p.theme.color.headerText};
    }
  }
`;
const Panel = styled.div`
  position: absolute;
  top: calc(100% + 6px);
  /* The nav and the account controls are always to its right, so the panel opens rightwards. */
  left: 0;
  z-index: 30;
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 280px;
  max-height: min(420px, 70vh);
  padding: 8px;
  background: ${(p) => p.theme.color.surfaceRaised};
  color: ${(p) => p.theme.color.text};
  border: 1px solid ${(p) => p.theme.color.borderStrong};
  border-radius: ${(p) => p.theme.radius.md};
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
  font-size: ${(p) => p.theme.font.size.md};
`;
const Input = styled.input`
  height: 32px;
  padding: 0 8px;
  font: inherit;
  color: ${(p) => p.theme.color.text};
  background: ${(p) => p.theme.color.surface};
  border: 1px solid ${(p) => p.theme.color.borderStrong};
  border-radius: ${(p) => p.theme.radius.sm};
  &::placeholder {
    color: ${(p) => p.theme.color.textMuted};
  }
  &:hover {
    border-color: ${(p) => p.theme.color.text};
  }
  &:focus-visible {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: 1px;
  }
`;
/** Reserves one line, so a hint appearing does not move the list. */
const Hint = styled.p`
  min-height: 1.4em;
  margin: 0;
  padding: 0 2px;
  font-size: ${(p) => p.theme.font.size.sm};
  color: ${(p) => p.theme.color.textMuted};
`;
const Choices = styled.ul`
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
`;
const Row = styled.label`
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 32px;
  padding: 4px 8px;
  cursor: pointer;
  border-radius: ${(p) => p.theme.radius.sm};
  overflow-wrap: anywhere;
  input {
    flex: none;
    margin: 0;
    accent-color: ${(p) => p.theme.color.accent};
    cursor: pointer;
  }
  &:hover {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 8%, ${(p) => p.theme.color.surfaceRaised});
  }
  &:active {
    background: color-mix(in srgb, ${(p) => p.theme.color.text} 16%, ${(p) => p.theme.color.surfaceRaised});
  }
  &:focus-within {
    outline: 2px solid ${(p) => p.theme.color.focus};
    outline-offset: -2px;
  }
`;
const Empty = styled.p`
  margin: 0;
  padding: 6px 8px;
  color: ${(p) => p.theme.color.textMuted};
`;

// region Helpers
/** The button's text: all, the one name, or a count. */
function summary(scope: readonly string[]): string {
  if (scope.length === 0) return copy.all;
  return scope.length === 1 ? (scope[0] as string) : copy.count(scope.length);
}

// region Component
/**
 * The header's namespace scope (design §11): a multi-select over the install's visible namespaces plus typed
 * entry, all by default. It scopes every list page; overview, health, diagnostics and the map stay install-wide.
 */
export function NamespaceScope({ installId }: { installId: string }) {
  const [scope, setScope] = useNamespaceScope();
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [hint, setHint] = useState("");
  const anchorRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const panelId = useId();
  const { data } = useNamespaces(installId, open);
  // Chosen names stay choices even when the server does not list them (typed, or no longer visible).
  const names = [...new Set([...(data?.namespaces ?? []), ...scope])].sort();
  const label = `${copy.label}: ${scope.length === 0 ? copy.all : scope.join(", ")}`;

  const close = useCallback((refocus: boolean) => {
    setOpen(false);
    setText("");
    setHint("");
    if (refocus) buttonRef.current?.focus();
  }, []);
  useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
    const onPointerDown = (e: PointerEvent) => {
      if (!anchorRef.current?.contains(e.target as Node)) close(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open, close]);

  const toggle = (ns: string) => setScope(scope.includes(ns) ? scope.filter((s) => s !== ns) : [...scope, ns]);
  const add = () => {
    const ns = text.trim();
    if (!ns) return;
    if (!isNamespaceName(ns)) return setHint(copy.invalid);
    if (!scope.includes(ns) && scope.length >= MAX_SCOPE) return setHint(copy.full);
    setScope(scope.includes(ns) ? scope : [...scope, ns]);
    setText("");
    setHint("");
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "Escape") return;
    e.preventDefault();
    close(true);
  };
  // Focus leaving the button and panel closes it, as Tab past the last choice does.
  const onBlur = (e: FocusEvent<HTMLDivElement>) => {
    if (open && e.relatedTarget && !anchorRef.current?.contains(e.relatedTarget as Node)) close(false);
  };

  return (
    <Fixed ref={anchorRef} onKeyDown={onKeyDown} onBlur={onBlur} data-testid="namespace-scope">
      <Tooltip label={label} placement="end" wrap>
        <Trigger
          ref={buttonRef}
          type="button"
          aria-label={label}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? panelId : undefined}
          data-scoped={scope.length > 0 || undefined}
          onClick={() => (open ? close(false) : setOpen(true))}
        >
          <Layers size={14} aria-hidden="true" />
          {/* The count is CSS content, so the button's visible text stays within its accessible name. */}
          {scope.length > 0 ? (
            <span
              className="count"
              data-testid="scope-count"
              data-count={scope.length > 99 ? "99+" : scope.length}
              aria-hidden="true"
            />
          ) : null}
          <span className="label" aria-hidden="true">
            {summary(scope)}
          </span>
          <ChevronDown className="chevron" size={14} aria-hidden="true" />
        </Trigger>
      </Tooltip>
      {open ? (
        <Panel id={panelId} role="dialog" aria-label={copy.label}>
          <Input
            ref={inputRef}
            type="text"
            value={text}
            placeholder={copy.placeholder}
            aria-label={copy.inputLabel}
            aria-describedby={`${panelId}-hint`}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => {
              setText(e.target.value);
              setHint("");
            }}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              add();
            }}
          />
          <Hint id={`${panelId}-hint`} aria-live="polite">
            {hint}
          </Hint>
          <Choices>
            <li>
              <Row>
                <input type="checkbox" checked={scope.length === 0} onChange={() => setScope([])} />
                {copy.all}
              </Row>
            </li>
            {names.map((ns) => (
              <li key={ns}>
                <Row>
                  <input type="checkbox" checked={scope.includes(ns)} onChange={() => toggle(ns)} />
                  {ns}
                </Row>
              </li>
            ))}
          </Choices>
          {data && names.length === 0 ? <Empty>{copy.empty}</Empty> : null}
        </Panel>
      ) : null}
    </Fixed>
  );
}
