import DarkModeIcon from "@mui/icons-material/DarkMode";
import LightModeIcon from "@mui/icons-material/LightMode";
import IconButton from "@mui/material/IconButton";
import { useColorScheme } from "@mui/material/styles";
import Tooltip from "@mui/material/Tooltip";

export function ColorModeToggle() {
  const { mode, systemMode, setMode } = useColorScheme();

  // mode is undefined on the first render, before the stored preference is read.
  if (!mode) {
    return null;
  }

  const dark = (mode === "system" ? systemMode : mode) === "dark";

  return (
    <Tooltip title={dark ? "Light mode" : "Dark mode"}>
      <IconButton onClick={() => setMode(dark ? "light" : "dark")} aria-label="Toggle colour mode">
        {dark ? <LightModeIcon /> : <DarkModeIcon />}
      </IconButton>
    </Tooltip>
  );
}
