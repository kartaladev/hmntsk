import DoneIcon from "@mui/icons-material/Done";
import NotificationsIcon from "@mui/icons-material/Notifications";
import Badge from "@mui/material/Badge";
import Box from "@mui/material/Box";
import Divider from "@mui/material/Divider";
import IconButton from "@mui/material/IconButton";
import ListItemText from "@mui/material/ListItemText";
import Menu from "@mui/material/Menu";
import MenuItem from "@mui/material/MenuItem";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { type MouseEvent, useCallback, useEffect, useState } from "react";

import { api, type Notification } from "./api";
import { isLocalPath, isPlainClick, navigate } from "./pages";

type Props = {
  user: string;
  onChange: () => void;
};

// NotificationBell shows how many notifications the user has not read, and
// keeps it current from the server-sent event stream. A signal only says that
// something changed; the page re-reads, because signals are best effort and the
// store is the truth.
export function NotificationBell({ user, onChange }: Props) {
  const [unread, setUnread] = useState<number>();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const [items, setItems] = useState<Notification[]>([]);
  const [live, setLive] = useState(false);
  const [problem, setProblem] = useState<string>();

  const reloadCount = useCallback(() => {
    api.unread().then(({ count }) => setUnread(count), () => setUnread(undefined));
  }, []);

  useEffect(() => {
    // The cookie goes with the stream request, so the server subscribes this
    // user. EventSource reconnects on its own, and every open, the first one
    // included, re-reads whatever was missed.
    const stream = new EventSource("/v1/notifications/stream");
    stream.onopen = () => {
      setLive(true);
      reloadCount();
    };
    stream.onerror = () => setLive(false);
    stream.addEventListener("unread-changed", () => {
      reloadCount();
      onChange();
    });

    return () => stream.close();
  }, [user, reloadCount, onChange]);

  const open = Boolean(anchor);

  // The list is read only while it is shown, and again whenever the count moves.
  useEffect(() => {
    if (!open) {
      setProblem(undefined);

      return;
    }

    api.notifications().then(({ notifications }) => setItems(notifications), () => setItems([]));
  }, [open, unread]);

  // Marking read can fail, for instance once the session has ended. The menu
  // says so, and the count is read again either way, so the badge shows what the
  // server holds rather than what the click hoped for.
  const markRead = async (id: string) => {
    try {
      await api.markRead(id);
      setProblem(undefined);
    } catch (e) {
      setProblem(`Could not mark it read: ${(e as Error).message}`);
    } finally {
      reloadCount();
    }
  };

  return (
    <>
      <Tooltip title={live ? "Notifications (live)" : "Notifications (reconnecting…)"}>
        <IconButton onClick={(e) => setAnchor(e.currentTarget)} aria-label="Notifications" aria-expanded={open}>
          <Badge badgeContent={unread} color="error" max={99}>
            <NotificationsIcon color={live ? "inherit" : "disabled"} />
          </Badge>
        </IconButton>
      </Tooltip>
      <Menu
        anchorEl={anchor}
        open={open}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
        transformOrigin={{ vertical: "top", horizontal: "right" }}
        slotProps={{ paper: { sx: { width: 380, maxWidth: "100vw" } } }}
      >
        <Box sx={{ px: 2, py: 1 }}>
          <Typography variant="subtitle2">Unread</Typography>
          {problem && (
            <Typography variant="body2" color="error" role="alert" sx={{ mt: 0.5 }}>
              {problem}
            </Typography>
          )}
        </Box>
        <Divider />
        {items.length === 0 && (
          <MenuItem disabled>
            <ListItemText primary="Nothing unread." />
          </MenuItem>
        )}
        {items.map((n) => (
          <MenuItem
            key={n.id}
            component="a"
            href={n.links?.context ?? n.links?.task}
            // A context link is a page of this application, so it opens without a reload.
            onClick={(e: MouseEvent<HTMLAnchorElement>) => {
              const href = n.links?.context;
              if (href && isLocalPath(href) && isPlainClick(e)) {
                e.preventDefault();
                setAnchor(null);
                navigate(href);
              }
            }}
            sx={{ gap: 1, whiteSpace: "normal" }}
          >
            <ListItemText primary={n.title ?? n.kind} secondary={new Date(n.createdAt).toLocaleString()} />
            <Tooltip title="Mark read">
              <IconButton
                size="small"
                edge="end"
                aria-label="Mark read"
                onClick={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  void markRead(n.id);
                }}
              >
                <DoneIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </MenuItem>
        ))}
      </Menu>
    </>
  );
}
