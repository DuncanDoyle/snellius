import { type RefObject, useCallback, useLayoutEffect, useState, useSyncExternalStore } from "react";

// region Helpers
const mediaQuery = (q: string) => (typeof window.matchMedia === "function" ? window.matchMedia(q) : undefined);

// region Component
/** True while the viewport matches q; read synchronously, so the first render already has the right layout. */
export function useMedia(q: string): boolean {
  const subscribe = useCallback(
    (cb: () => void) => {
      const m = mediaQuery(q);
      m?.addEventListener?.("change", cb);
      return () => m?.removeEventListener?.("change", cb);
    },
    [q],
  );
  return useSyncExternalStore(
    subscribe,
    () => mediaQuery(q)?.matches ?? false,
    () => false,
  );
}

/** The element's content-box width: read before first paint, then on every resize; undefined until known or when off. */
export function useWidth(ref: RefObject<HTMLElement | null>, on = true): number | undefined {
  const [width, setWidth] = useState<number>();
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !on) {
      setWidth(undefined);
      return;
    }
    const set = (w: number) => setWidth(w > 0 ? w : undefined);
    const s = getComputedStyle(el);
    set(el.clientWidth - (Number.parseFloat(s.paddingLeft) || 0) - (Number.parseFloat(s.paddingRight) || 0));
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([e]) => e && set(e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref, on]);
  return width;
}
