//! Switchboard's menu-bar app. It shows routes from the daemon's control API
//! (refreshed from /v1/events), runs first-time setup through the bundled
//! `sb` sidecar, and opens the dashboard in its own window.

mod actions;
mod daemon;
mod version;

use std::sync::Mutex;
use std::time::Duration;

use tauri::menu::{CheckMenuItem, Menu, MenuEvent, MenuItem, PredefinedMenuItem};
use tauri::tray::TrayIconBuilder;
use tauri::{AppHandle, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder, Wry};
use tauri_plugin_autostart::{MacosLauncher, ManagerExt};
use tauri_plugin_shell::ShellExt;

use daemon::Status;

const TRAY_ID: &str = "main";
/// Routes listed in the menu before "…and N more".
const MENU_ROUTES: usize = 25;

/// What the tray last learned from the daemon.
#[derive(Default)]
struct Shared {
    status: Mutex<Option<Status>>,
}

pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        .manage(Shared::default())
        .invoke_handler(tauri::generate_handler![setup_plan, run_setup, add_route])
        .setup(|app| {
            #[cfg(target_os = "macos")]
            app.set_activation_policy(tauri::ActivationPolicy::Accessory); // menu bar only, no Dock icon
            let menu = build_menu(app.handle(), None)?;
            TrayIconBuilder::with_id(TRAY_ID)
                .icon(tauri::image::Image::from_bytes(include_bytes!("../icons/tray.png"))?)
                .icon_as_template(true)
                .tooltip("Switchboard")
                .menu(&menu)
                .show_menu_on_left_click(true)
                .on_menu_event(on_menu_event)
                .build(app)?;
            tauri::async_runtime::spawn(watch(app.handle().clone()));
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("build the Switchboard tray app");

    app.run(|_, event| {
        // Closing the last window (the wizard, the dashboard) must not quit a
        // menu-bar app; only Quit does.
        if let RunEvent::ExitRequested { api, code: None, .. } = event {
            api.prevent_exit();
        }
    });
}

/// Keeps the menu current: loads the status, follows /v1/events, and when the
/// daemon isn't reachable retries every few seconds. On the very first
/// failure after launch it offers the setup wizard.
async fn watch(app: AppHandle) {
    let mut offered_setup = false;
    loop {
        match daemon::status().await {
            Ok(st) => set_status(&app, Some(st)),
            Err(_) => {
                set_status(&app, None);
                if !offered_setup && !setup_done(&app) {
                    let _ = open_wizard(&app);
                }
                offered_setup = true;
                tokio::time::sleep(Duration::from_secs(3)).await;
                continue;
            }
        }
        offered_setup = true;
        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel::<()>();
        let events = daemon::follow_events(move |_| {
            let _ = tx.send(());
        });
        tokio::pin!(events);
        loop {
            tokio::select! {
                _ = &mut events => break, // stream ended: reload and reconnect
                Some(()) = rx.recv() => {
                    // Let a burst settle, then reload once.
                    tokio::time::sleep(Duration::from_millis(150)).await;
                    while rx.try_recv().is_ok() {}
                    if let Ok(st) = daemon::status().await {
                        set_status(&app, Some(st));
                    }
                }
            }
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

fn set_status(app: &AppHandle, st: Option<Status>) {
    let shared = app.state::<Shared>();
    let mut cur = shared.status.lock().unwrap();
    if *cur == st {
        return;
    }
    *cur = st;
    let (menu, tip) = (build_menu(app, cur.as_ref()), tooltip(cur.as_ref()));
    drop(cur);
    if let (Some(tray), Ok(menu)) = (app.tray_by_id(TRAY_ID), menu) {
        let _ = tray.set_menu(Some(menu));
        let _ = tray.set_tooltip(Some(tip));
    }
}

fn tooltip(st: Option<&Status>) -> String {
    match st {
        None => "Switchboard isn't running".into(),
        Some(s) if s.paused => "Switchboard is paused".into(),
        Some(s) => format!("Switchboard: {} route(s)", s.routes.len()),
    }
}

fn health_dot(health: &str) -> &'static str {
    match health {
        "up" => "🟢",
        "down" => "🔴",
        _ => "⚪",
    }
}

fn build_menu(app: &AppHandle, st: Option<&Status>) -> tauri::Result<Menu<Wry>> {
    let menu = Menu::new(app)?;
    let item = |id: &str, text: &str, enabled: bool| MenuItem::with_id(app, id, text, enabled, None::<&str>);
    let sep = || PredefinedMenuItem::separator(app);

    match st {
        None => {
            menu.append(&item("header", "Switchboard isn't running", false)?)?;
            menu.append(&item("setup", "Set Up Switchboard…", true)?)?;
        }
        Some(st) => {
            let header = match (st.paused, st.routes.len()) {
                (true, _) => "Paused: every route is off".to_owned(),
                (false, 0) => "No routes yet".to_owned(),
                (false, 1) => "1 route".to_owned(),
                (false, n) => format!("{n} routes"),
            };
            menu.append(&item("header", &header, false)?)?;
            for r in st.routes.iter().take(MENU_ROUTES) {
                let from = match (r.source.as_str(), &r.container) {
                    ("docker", Some(c)) => format!("  (docker: {c})"),
                    ("file", _) => "  (project file)".into(),
                    _ => String::new(),
                };
                let label = format!("{} {}  :{}{from}", health_dot(&r.health), r.name, r.port);
                let clickable = st.route_url(&r.name).is_some();
                menu.append(&item(&format!("route:{}", r.name), &label, clickable)?)?;
            }
            if st.routes.len() > MENU_ROUTES {
                let more = format!("…and {} more: open the dashboard", st.routes.len() - MENU_ROUTES);
                menu.append(&item("dashboard-more", &more, st.dashboard_origin().is_some())?)?;
            }
        }
    }
    let up = st.is_some();
    menu.append(&sep()?)?;
    menu.append(&item("add-route", "Add Route…", up)?)?;
    let https = st.and_then(Status::dashboard_origin).is_some();
    menu.append(&item("dashboard", "Open Dashboard", https)?)?;
    let paused = st.is_some_and(|s| s.paused);
    menu.append(&CheckMenuItem::with_id(
        app,
        "pause",
        "Pause All",
        up,
        paused,
        None::<&str>,
    )?)?;
    menu.append(&sep()?)?;
    menu.append(&item("install-cli", "Install Command-Line Tool…", true)?)?;
    menu.append(&item("updates", "Check for Updates…", true)?)?;
    let at_login = app.autolaunch().is_enabled().unwrap_or(false);
    menu.append(&CheckMenuItem::with_id(
        app,
        "autostart",
        "Start at Login",
        true,
        at_login,
        None::<&str>,
    )?)?;
    menu.append(&sep()?)?;
    menu.append(&item("quit", "Quit Switchboard", true)?)?;
    Ok(menu)
}

fn on_menu_event(app: &AppHandle, event: MenuEvent) {
    let id = event.id().as_ref().to_owned();
    let app = app.clone();
    let status = app.state::<Shared>().status.lock().unwrap().clone();
    match id.as_str() {
        "setup" => report(&app, open_wizard(&app)),
        "add-route" => report(&app, open_add_route(&app)),
        "dashboard" | "dashboard-more" => {
            tauri::async_runtime::spawn(async move { report(&app, actions::open_dashboard(&app).await) });
        }
        "pause" => {
            let paused = !status.is_some_and(|s| s.paused);
            tauri::async_runtime::spawn(async move {
                report(&app, daemon::set_paused(paused).await);
                if let Ok(st) = daemon::status().await {
                    set_status(&app, Some(st)); // also fixes the checkmark if it failed
                }
            });
        }
        "install-cli" => {
            tauri::async_runtime::spawn_blocking(move || actions::install_cli(&app));
        }
        "updates" => {
            tauri::async_runtime::spawn(async move { actions::check_for_updates(&app).await });
        }
        "autostart" => {
            let al = app.autolaunch();
            let res = if al.is_enabled().unwrap_or(false) {
                al.disable()
            } else {
                al.enable()
            };
            report(&app, res.map_err(|e| format!("Couldn't change Start at Login: {e}")));
            let cur = app.state::<Shared>().status.lock().unwrap().clone();
            if let (Some(tray), Ok(menu)) = (app.tray_by_id(TRAY_ID), build_menu(&app, cur.as_ref())) {
                let _ = tray.set_menu(Some(menu));
            }
        }
        "quit" => app.exit(0),
        other => {
            if let (Some(name), Some(st)) = (other.strip_prefix("route:"), status)
                && let Some(url) = st.route_url(name)
            {
                use tauri_plugin_opener::OpenerExt;
                report(
                    &app,
                    app.opener().open_url(url, None::<&str>).map_err(|e| e.to_string()),
                );
            }
        }
    }
}

/// Shows an error from a menu action in a dialog.
fn report<E: std::fmt::Display>(app: &AppHandle, res: Result<(), E>) {
    if let Err(e) = res {
        actions::error_dialog(app, &e.to_string());
    }
}

// ---- windows ----

fn show_window(app: &AppHandle, label: &str, title: &str, page: &str, (w, h): (f64, f64)) -> Result<(), String> {
    if let Some(win) = app.get_webview_window(label) {
        return win.set_focus().map_err(|e| e.to_string());
    }
    let win = WebviewWindowBuilder::new(app, label, WebviewUrl::App(page.into()))
        .title(title)
        .inner_size(w, h)
        .resizable(false)
        .center()
        .build()
        .map_err(|e| e.to_string())?;
    win.set_focus().map_err(|e| e.to_string())
}

fn open_wizard(app: &AppHandle) -> Result<(), String> {
    show_window(app, "wizard", "Set Up Switchboard", "index.html", (560.0, 640.0))
}

fn open_add_route(app: &AppHandle) -> Result<(), String> {
    show_window(app, "add-route", "Add Route", "add-route.html", (420.0, 330.0))
}

// ---- setup ----

fn setup_marker(app: &AppHandle) -> Option<std::path::PathBuf> {
    app.path().app_config_dir().ok().map(|d| d.join("setup-done"))
}

fn setup_done(app: &AppHandle) -> bool {
    setup_marker(app).is_some_and(|p| p.exists())
}

/// Refuses to set up from a temporary location, because setup points the
/// login services at this copy of sb.
fn check_location() -> Result<(), String> {
    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let path = exe.to_string_lossy();
    if path.contains("/AppTranslocation/") || path.starts_with("/Volumes/") {
        return Err(
            "Move Switchboard to your Applications folder and open it from there first. \
                    Setup points your login services at this copy of the app, and macOS is running it \
                    from a temporary or removable location."
                .into(),
        );
    }
    Ok(())
}

/// The changes setup will make, from `sb setup --print-plan`.
#[tauri::command]
async fn setup_plan(app: AppHandle) -> Result<serde_json::Value, String> {
    check_location()?;
    let out = app
        .shell()
        .sidecar("sb")
        .map_err(|e| e.to_string())?
        .args(["setup", "--print-plan"])
        .output()
        .await
        .map_err(|e| e.to_string())?;
    if !out.status.success() {
        return Err(output_text(&out.stderr, &out.stdout));
    }
    serde_json::from_slice(&out.stdout).map_err(|e| format!("read the setup plan: {e}"))
}

/// Runs `sb setup --yes --admin-dialog`: macOS asks for the password.
#[tauri::command]
async fn run_setup(app: AppHandle) -> Result<String, String> {
    check_location()?;
    let out = app
        .shell()
        .sidecar("sb")
        .map_err(|e| e.to_string())?
        .args(["setup", "--yes", "--admin-dialog"])
        .output()
        .await
        .map_err(|e| e.to_string())?;
    let text = output_text(&out.stdout, &out.stderr);
    if !out.status.success() {
        return Err(text);
    }
    if let Some(marker) = setup_marker(&app) {
        let _ = std::fs::create_dir_all(marker.parent().unwrap());
        let _ = std::fs::write(marker, b"");
    }
    Ok(text)
}

fn output_text(first: &[u8], second: &[u8]) -> String {
    let mut s = String::from_utf8_lossy(first).trim().to_owned();
    let rest = String::from_utf8_lossy(second);
    if !rest.trim().is_empty() {
        s.push_str("\n\n");
        s.push_str(rest.trim());
    }
    s
}

#[tauri::command]
async fn add_route(app: AppHandle, name: String, port: u16, redirect: bool) -> Result<(), String> {
    daemon::put_route(&daemon::NewRoute {
        name,
        port,
        wildcard: false,
        redirect_https: redirect,
    })
    .await?;
    if let Ok(st) = daemon::status().await {
        set_status(&app, Some(st));
    }
    Ok(())
}
