import Link, { type LinkProps } from "@mui/material/Link";

import { isPlainClick, navigate } from "./pages";

// AppLink is a real link, so it can be opened in a new tab or copied, that
// moves between the application's pages without reloading on a plain click.
export function AppLink({ href, onClick, ...props }: LinkProps & { href: string }) {
  return (
    <Link
      href={href}
      onClick={(e) => {
        onClick?.(e);

        if (!e.defaultPrevented && isPlainClick(e)) {
          e.preventDefault();
          navigate(href);
        }
      }}
      {...props}
    />
  );
}
