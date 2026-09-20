"use client";

import { useCallback, useMemo, useSyncExternalStore } from "react";

import { forgetKey, rememberKey, serverSnapshot, snapshot, subscribe } from "@/lib/session";

interface KeySession {
  key: string | null;
  /** loaded is false until the browser's storage has been read, so the gate never flashes. */
  loaded: boolean;
  signIn: (key: string) => void;
  signOut: () => void;
}

export function useKey(): KeySession {
  const key = useSyncExternalStore(subscribe, snapshot, serverSnapshot);

  const signIn = useCallback((next: string) => rememberKey(next), []);
  const signOut = useCallback(() => forgetKey(), []);

  return useMemo(
    () => ({ key: key ?? null, loaded: key !== undefined, signIn, signOut }),
    [key, signIn, signOut],
  );
}

/**
 * KeyProvider is kept as a component so the tree reads the same, but there is no context
 * to provide: useSyncExternalStore subscribes every consumer to the one store directly.
 */
export function KeyProvider({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
