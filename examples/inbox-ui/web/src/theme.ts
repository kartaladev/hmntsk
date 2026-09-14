import { createTheme } from "@mui/material/styles";

// One theme for the whole page, following Material UI's theming guidance:
// light and dark colour schemes the viewer's system (or the toggle) picks
// between, and CSS variables so switching schemes needs no re-render. App-wide
// component defaults live here; one-off layout stays in `sx` where it is used.
export const theme = createTheme({
  cssVariables: { colorSchemeSelector: "class" },
  colorSchemes: {
    light: {
      palette: {
        primary: { main: "#3f51b5" },
        secondary: { main: "#00897b" },
        background: { default: "#f4f6fb", paper: "#ffffff" },
      },
    },
    dark: {
      palette: {
        primary: { main: "#8c9eff" },
        secondary: { main: "#4db6ac" },
        background: { default: "#0f1320", paper: "#171c2c" },
      },
    },
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily: 'Inter, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif',
    h6: { fontWeight: 700 },
    button: { fontWeight: 600 },
  },
  components: {
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: { root: { textTransform: "none" } },
    },
    MuiPaper: {
      defaultProps: { variant: "outlined" },
    },
    MuiChip: {
      styleOverrides: { root: { fontWeight: 600 } },
    },
    MuiTableCell: {
      styleOverrides: { head: { fontWeight: 700 } },
    },
  },
});
