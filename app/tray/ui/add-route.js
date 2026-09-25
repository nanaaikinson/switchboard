// Add Route: validates like the daemon (config.ValidHostname), then calls the
// add_route command, which POSTs /v1/routes over the control socket.
const { invoke } = window.__TAURI__.core;
const win = window.__TAURI__.window.getCurrentWindow();
const $ = (id) => document.getElementById(id);

const label = "[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?";
const hostname = new RegExp(`^(\\*\\.)?${label}(\\.${label})*$`);

function check() {
  const name = $("name").value.trim().toLowerCase().replace(/\.$/, "");
  const port = $("port").value.trim();
  const nameOK = name.length > 0 && name.length <= 253 && hostname.test(name);
  const portOK = /^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535;
  return { name, port: Number(port), nameOK, portOK };
}

$("name").addEventListener("input", () => {
  const { name, nameOK } = check();
  $("name-hint").textContent = !name ? "Names get .test unless they have it." : nameOK
    ? `https://${name.endsWith(".test") ? name : name + ".test"}`
    : "Use letters, digits and hyphens, like myapp or api.myapp.";
  $("name-hint").className = `hint ${name && !nameOK ? "error" : "muted"}`;
});

$("form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const v = check();
  $("name").setAttribute("aria-invalid", String(!v.nameOK));
  $("port").setAttribute("aria-invalid", String(!v.portOK));
  $("port-hint").textContent = v.portOK ? "" : "Enter a port from 1 to 65535.";
  $("error").textContent = "";
  if (!v.nameOK || !v.portOK) return;
  $("add").disabled = true;
  try {
    await invoke("add_route", { name: v.name, port: v.port, redirect: $("redirect").checked });
    await win.close();
  } catch (err) {
    $("error").textContent = String(err);
    $("add").disabled = false;
  }
});

$("cancel").addEventListener("click", () => win.close());
