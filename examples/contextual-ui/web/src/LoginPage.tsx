import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import ReceiptLongIcon from "@mui/icons-material/ReceiptLong";
import Alert from "@mui/material/Alert";
import Avatar from "@mui/material/Avatar";
import Box from "@mui/material/Box";
import CircularProgress from "@mui/material/CircularProgress";
import List from "@mui/material/List";
import ListItemAvatar from "@mui/material/ListItemAvatar";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";
import { useEffect, useState } from "react";

import { api, type DemoUser } from "./api";
import { initials } from "./people";

type Props = {
  onSignedIn: (user: DemoUser) => void;
};

// LoginPage stands for an application's sign-in. It asks for no password: it
// lists the demo's people, and choosing one sets the session cookie.
export function LoginPage({ onSignedIn }: Props) {
  const [users, setUsers] = useState<DemoUser[]>([]);
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState<string>();

  useEffect(() => {
    api.users().then(setUsers, (e: Error) => setError(e.message));
  }, []);

  const signIn = async (user: DemoUser) => {
    setBusy(user.id);
    setError(undefined);

    try {
      onSignedIn(await api.signIn(user.id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(undefined);
    }
  };

  return (
    <Box sx={{ minHeight: "100vh", display: "grid", placeItems: "center", px: 2, py: 4, bgcolor: "background.default" }}>
      <Paper component="main" sx={{ p: { xs: 3, sm: 5 }, width: "100%", maxWidth: 520 }}>
        <Stack spacing={1} sx={{ alignItems: "center", textAlign: "center" }}>
          <ReceiptLongIcon color="primary" sx={{ fontSize: 48 }} />
          <Typography variant="h5" component="h1" sx={{ fontWeight: 700 }}>
            Sign in to Acme Purchasing
          </Typography>
          <Typography color="text.secondary">Choose who you are for this demo.</Typography>
        </Stack>

        <Alert severity="warning" variant="outlined" sx={{ my: 3 }}>
          <strong>Demo only, not authentication.</strong> Signing in asks for no password: it sets a cookie the server
          trusts. A real application signs people in with its own authentication. This page illustrates hmntsk&apos;s
          HTTP contracts and is not a UI library.
        </Alert>

        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}

        <List aria-label="Demo users" disablePadding>
          {users.map((user) => (
            <ListItemButton
              key={user.id}
              onClick={() => void signIn(user)}
              disabled={Boolean(busy)}
              sx={{ borderRadius: 2, border: 1, borderColor: "divider", mb: 1 }}
            >
              <ListItemAvatar>
                <Avatar sx={{ bgcolor: "primary.main" }}>{initials(user.name)}</Avatar>
              </ListItemAvatar>
              <ListItemText primary={user.name} secondary={`${user.role} · ${user.id}`} />
              {busy === user.id ? <CircularProgress size={20} /> : <ChevronRightIcon color="action" />}
            </ListItemButton>
          ))}
        </List>

        <Typography variant="body2" color="text.secondary" sx={{ mt: 2, textAlign: "center" }}>
          Sign in as erin to place an order, as carol to approve it, as erin again to send or upload its purchase
          order, then as alice or bob to review and approve the invoice that follows.
        </Typography>
      </Paper>
    </Box>
  );
}
