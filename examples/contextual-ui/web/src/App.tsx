import InboxIcon from "@mui/icons-material/Inbox";
import ReceiptLongIcon from "@mui/icons-material/ReceiptLong";
import ShoppingCartIcon from "@mui/icons-material/ShoppingCart";
import Alert from "@mui/material/Alert";
import AppBar from "@mui/material/AppBar";
import Box from "@mui/material/Box";
import CircularProgress from "@mui/material/CircularProgress";
import Container from "@mui/material/Container";
import Stack from "@mui/material/Stack";
import Tab from "@mui/material/Tab";
import Tabs from "@mui/material/Tabs";
import Toolbar from "@mui/material/Toolbar";
import Typography from "@mui/material/Typography";
import { useCallback, useEffect, useState } from "react";

import { api, type DemoUser, type TaskType } from "./api";
import { ColorModeToggle } from "./ColorModeToggle";
import { InboxPage } from "./InboxPage";
import { LoginPage } from "./LoginPage";
import { NotificationBell } from "./Notifications";
import { OrderPage } from "./OrderPage";
import { OrdersPage } from "./OrdersPage";
import { afterSignIn, navigate, signInPath } from "./pages";
import { UserMenu } from "./UserMenu";
import { usePage } from "./usePage";

export function App() {
  const route = usePage();
  // undefined while the server is asked who is signed in; null when nobody is.
  const [user, setUser] = useState<DemoUser | null>();
  const [types, setTypes] = useState<Record<string, TaskType>>({});
  const [revision, setRevision] = useState(0);

  const refresh = useCallback(() => setRevision((r) => r + 1), []);

  useEffect(() => {
    api.session().then(setUser, () => setUser(null));
  }, []);

  useEffect(() => {
    if (!user) {
      return;
    }

    api.taskTypes().then(
      ({ types }) => setTypes(Object.fromEntries(types.map((t) => [t.name, t]))),
      () => setTypes({}),
    );
  }, [user?.id]);

  // A viewer who is not signed in signs in first and comes back to the page
  // they opened; a signed-in viewer never sees the sign-in page.
  useEffect(() => {
    if (user === null && route.page !== "login") {
      navigate(signInPath(window.location.pathname + window.location.search), { replace: true });
    } else if (user && route.page === "login") {
      navigate(afterSignIn(route.next, user), { replace: true });
    }
  }, [user, route]);

  const signOut = async () => {
    await api.signOut();
    navigate("/login");
    setUser(null);
  };

  if (user === null && route.page === "login") {
    return <LoginPage onSignedIn={setUser} />;
  }

  if (!user || route.page === "login") {
    return (
      <Box sx={{ minHeight: "100vh", display: "grid", placeItems: "center" }}>
        <CircularProgress />
      </Box>
    );
  }

  const section = route.page === "inbox" || route.page === "orders" ? route.page : false;

  return (
    <Box sx={{ minHeight: "100vh", bgcolor: "background.default" }}>
      <AppBar position="sticky" color="inherit" elevation={0} sx={{ borderBottom: 1, borderColor: "divider" }}>
        <Toolbar sx={{ gap: { xs: 1, sm: 2 } }}>
          <Stack direction="row" spacing={1} sx={{ alignItems: "center", mr: { xs: 0, sm: 2 } }}>
            <ReceiptLongIcon color="primary" />
            <Typography variant="h6" component="span" sx={{ display: { xs: "none", sm: "block" }, whiteSpace: "nowrap" }}>
              Acme Purchasing
            </Typography>
          </Stack>
          <Tabs value={section} aria-label="Pages" sx={{ flexGrow: 1, alignSelf: "stretch", "& .MuiTabs-flexContainer": { height: "100%" } }}>
            <Tab value="inbox" label="Inbox" icon={<InboxIcon />} iconPosition="start" onClick={() => navigate("/")} sx={{ minHeight: 64 }} />
            <Tab
              value="orders"
              label="Orders"
              icon={<ShoppingCartIcon />}
              iconPosition="start"
              onClick={() => navigate("/orders")}
              sx={{ minHeight: 64 }}
            />
          </Tabs>
          <NotificationBell user={user.id} onChange={refresh} />
          <ColorModeToggle />
          <UserMenu user={user} onSignOut={() => void signOut()} />
        </Toolbar>
      </AppBar>

      <Container maxWidth="xl" component="main" sx={{ py: 3 }}>
        {route.page === "inbox" && <InboxPage user={user} types={types} revision={revision} />}
        {route.page === "orders" && <OrdersPage user={user} />}
        {route.page === "order" && (
          <OrderPage
            key={route.orderId}
            orderId={route.orderId}
            taskId={route.taskId}
            user={user}
            types={types}
            revision={revision}
            onChange={refresh}
          />
        )}
        {route.page === "notFound" && <Alert severity="warning">There is no such page.</Alert>}
      </Container>
    </Box>
  );
}
