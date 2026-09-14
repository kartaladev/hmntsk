import { useEffect, useState } from "react";

import { matchPage, type PageRoute } from "./pages";

// usePage is the page the location shows, kept current across navigate and the
// browser's back and forward buttons.
export function usePage(): PageRoute {
  const [route, setRoute] = useState(() => matchPage(window.location));

  useEffect(() => {
    const onPop = () => setRoute(matchPage(window.location));
    window.addEventListener("popstate", onPop);

    return () => window.removeEventListener("popstate", onPop);
  }, []);

  return route;
}
