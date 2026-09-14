import { useEffect, useState } from "react";

// useTicker counts up every interval while the page is visible, and once more
// when it becomes visible again; a null interval pauses it. The demo polls the
// application's own records with it: hmntsk notifies people about their
// tasks, not about the orders a host keeps.
export function useTicker(intervalMs: number | null): number {
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (intervalMs === null) {
      return;
    }

    const bump = () => {
      if (document.visibilityState === "visible") {
        setTick((t) => t + 1);
      }
    };

    const timer = window.setInterval(bump, intervalMs);
    document.addEventListener("visibilitychange", bump);

    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", bump);
    };
  }, [intervalMs]);

  return tick;
}
