import { useEffect, useRef, useState } from 'react';

export function useMediaQuery(
  query: string,
  options?: { onChange?: (matches: boolean) => void },
): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return false;
    }
    return window.matchMedia(query).matches;
  });

  // Keep the latest onChange in a ref so changing identity does not re-attach
  // the listener, but the synchronous call inside the handler still uses the
  // most-recently-passed callback.
  const onChangeRef = useRef(options?.onChange);
  useEffect(() => {
    onChangeRef.current = options?.onChange;
  });

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
      return;
    }
    const mql = window.matchMedia(query);
    setMatches(mql.matches);
    const handler = (event: MediaQueryListEvent) => {
      // Run the consumer's side-effects (e.g. history.go) BEFORE updating
      // state, so React's next render observes the post-side-effect world.
      onChangeRef.current?.(event.matches);
      setMatches(event.matches);
    };
    mql.addEventListener('change', handler);
    return () => mql.removeEventListener('change', handler);
  }, [query]);

  return matches;
}
