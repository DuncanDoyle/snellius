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

/** The element's border-box width, measured before paint and on every resize; undefined until known. */
export function useWidth(ref: RefObject<HTMLElement | null>): number | undefined {
  const [width, setWidth] = useState<number>();
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const set = (w: number) => setWidth(w > 0 ? w : undefined);
    set(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([e]) => e && set(e.borderBoxSize?.[0]?.inlineSize ?? e.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}
