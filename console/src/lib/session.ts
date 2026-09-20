// Where the tenant's key lives while the tab is open.
//
// sessionStorage, deliberately: the key is gone when the tab closes, it never reaches
// the console's server, and it is never a cookie, so no cross-site request can ride on
// it (ADR-012). Every access is guarded — storage throws in a private window and in some
// embedded browsers, and a console that cannot remember a key must still work.
//
// The store below is what React subscribes to with useSyncExternalStore. sessionStorage
// is an external mutable source, which is exactly what that hook is for; reading it in an
// effect instead would mean a synchronous setState on mount.

const STORAGE_KEY = "vitts.key";

type Listener = () => void;

const listeners = new Set<Listener>();
let cached: string | null = null;
let primed = false;

function read(): string | null {
  try {
    return window.sessionStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

export function subscribe(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * snapshot is the client's view. It caches, because useSyncExternalStore compares
 * snapshots and re-reading storage on every render would be both slow and pointless:
 * nothing but this module writes the value.
 */
export function snapshot(): string | null {
  if (!primed) {
    cached = read();
    primed = true;
  }
  return cached;
}

/**
 * serverSnapshot is undefined rather than null, and the difference matters: null means
 * "no key, show the gate", undefined means "not known yet". On the server there is no
 * sessionStorage, so rendering the gate there would flash it at every tenant who is
 * already signed in.
 */
export function serverSnapshot(): undefined {
  return undefined;
}

export function rememberKey(key: string): void {
  try {
    window.sessionStorage.setItem(STORAGE_KEY, key);
  } catch {
    // The console works without persistence; the key simply lasts one navigation.
  }
  cached = key;
  primed = true;
  emit();
}

export function forgetKey(): void {
  try {
    window.sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // nothing to clear
  }
  cached = null;
  primed = true;
  emit();
}

function emit(): void {
  for (const listener of listeners) {
    listener();
  }
}

/** maskKey shows what a tenant needs to recognise a key without printing the secret. */
export function maskKey(key: string): string {
  const cut = key.indexOf("_", key.indexOf("_") + 1);
  const prefix = cut > 0 ? key.slice(0, cut + 1) : key.slice(0, 8);
  return `${prefix}${"·".repeat(8)}${key.slice(-4)}`;
}
