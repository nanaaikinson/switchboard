// The first-launch setup wizard. The plan comes from `sb setup --print-plan`;
// Set Up runs `sb setup --yes --admin-dialog` (see src-tauri/src/lib.rs).
const { invoke } = window.__TAURI__.core;
const win = window.__TAURI__.window.getCurrentWindow();
const $ = (id) => document.getElementById(id);

function show(id) {
  for (const s of document.querySelectorAll("section")) s.hidden = s.id !== id;
}

function fail(title, text) {
  $("failed-title").textContent = title;
  $("output").textContent = text;
  show("failed");
}

async function loadPlan() {
  show("loading");
  try {
    const plan = await invoke("setup_plan");
    const list = $("changes");
    list.replaceChildren(...plan.changes.map((c) => Object.assign(document.createElement("li"), { textContent: c })));
    $("notes").textContent = plan.notes;
    $("command").textContent = plan.command.map((a) => (/^[\w/.:=-]+$/.test(a) ? a : `'${a.replaceAll("'", "'\\''")}'`)).join(" ");
    show("plan");
  } catch (e) {
    fail("Setup can't start yet.", String(e));
    $("retry").textContent = "Check Again";
  }
}

async function runSetup() {
  show("running");
  try {
    await invoke("run_setup");
    show("done");
  } catch (e) {
    fail("Setup didn't finish.", String(e));
    $("retry").textContent = "Try Again";
  }
}

$("run").addEventListener("click", runSetup);
$("retry").addEventListener("click", () => ($("retry").textContent === "Try Again" ? runSetup() : loadPlan()));
for (const id of ["later", "close", "cancel"]) $(id).addEventListener("click", () => win.close());
loadPlan();
