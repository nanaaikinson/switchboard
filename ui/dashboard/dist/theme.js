// Applies the saved theme before the first paint, so a dark page doesn't
// flash white. Loaded as a file because the CSP forbids inline scripts.
// src/lib/theme.ts takes over once the app runs; keep the two in step.
(function () {
  var choice = null;
  try {
    choice = localStorage.getItem("switchboard-theme");
  } catch (e) {
    /* storage blocked: follow the system */
  }
  var dark = choice === "dark" || (choice !== "light" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  document.documentElement.style.colorScheme = dark ? "dark" : "light";
})();
