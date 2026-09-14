import LogoutIcon from "@mui/icons-material/Logout";
import Avatar from "@mui/material/Avatar";
import Box from "@mui/material/Box";
import Divider from "@mui/material/Divider";
import IconButton from "@mui/material/IconButton";
import ListItemIcon from "@mui/material/ListItemIcon";
import Menu from "@mui/material/Menu";
import MenuItem from "@mui/material/MenuItem";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { useState } from "react";

import type { DemoUser } from "./api";
import { initials } from "./people";

type Props = {
  user: DemoUser;
  onSignOut: () => void;
};

// UserMenu is the signed-in user's avatar, at the right of the app bar, and
// the menu it opens: who they are, and signing out.
export function UserMenu({ user, onSignOut }: Props) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const open = Boolean(anchor);

  return (
    <>
      <Tooltip title={user.name}>
        <IconButton
          onClick={(e) => setAnchor(e.currentTarget)}
          aria-label={`Account of ${user.name}`}
          aria-haspopup="menu"
          aria-expanded={open}
          sx={{ p: 0.5 }}
        >
          <Avatar sx={{ width: 36, height: 36, fontSize: 15, bgcolor: "primary.main" }}>{initials(user.name)}</Avatar>
        </IconButton>
      </Tooltip>
      <Menu
        anchorEl={anchor}
        open={open}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "right" }}
        transformOrigin={{ vertical: "top", horizontal: "right" }}
        slotProps={{ paper: { sx: { minWidth: 260 } } }}
      >
        <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, px: 2, py: 1.5 }}>
          <Avatar sx={{ bgcolor: "primary.main" }}>{initials(user.name)}</Avatar>
          <Box>
            <Typography variant="subtitle2">{user.name}</Typography>
            <Typography variant="body2" color="text.secondary">
              {user.role}
            </Typography>
            <Typography variant="caption" color="text.secondary">
              {user.id} · {user.groups.join(", ")}
            </Typography>
          </Box>
        </Box>
        <Divider />
        <MenuItem
          onClick={() => {
            setAnchor(null);
            onSignOut();
          }}
        >
          <ListItemIcon>
            <LogoutIcon fontSize="small" />
          </ListItemIcon>
          Sign out
        </MenuItem>
      </Menu>
    </>
  );
}
