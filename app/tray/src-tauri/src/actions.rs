//! Menu actions that talk to the outside world: the dashboard window, the
//! command-line tool, and update checks.

use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};
use tauri_plugin_opener::OpenerExt;

use crate::{daemon, rollout, version};

const RELEASES_API: &str = "https://api.github.com/repos/nanaaikinson/switchboard/releases/latest";

pub fn error_dialog(app: &AppHandle, msg: &str) {
    app.dialog()
        .message(msg)
        .title("Switchboard")
        .kind(MessageDialogKind::Error)
        .show(|_| {});
}

fn info_dialog(app: &AppHandle, msg: &str) {
    app.dialog()
        .message(msg)
        .title("Switchboard")
        .kind(MessageDialogKind::Info)
        .show(|_| {});
}

/// Signs in with a one-time token from the control socket and shows the
/// dashboard in its own window. The window loads a remote page, so it has no
/// access to the app's commands (see capabilities/local-windows.json).
pub async fn open_dashboard(app: &AppHandle) -> Result<(), String> {
    let st = daemon::status().await?;
    let origin = st
        .dashboard_origin()
        .ok_or("The dashboard needs HTTPS, which isn't running. Run 'sb doctor' in a terminal to see why.")?;
    let token = daemon::dashboard_login().await?;
    let url: tauri::Url = format!("{origin}/login?token={token}")
        .parse()
        .map_err(|e| format!("{e}"))?;
    if let Some(win) = app.get_webview_window("dashboard") {
        win.navigate(url).map_err(|e| e.to_string())?;
        return win.set_focus().map_err(|e| e.to_string());
    }
    let win = WebviewWindowBuilder::new(app, "dashboard", WebviewUrl::External(url))
        .title("Switchboard")
        .inner_size(1100.0, 760.0)
        .min_inner_size(420.0, 400.0)
        .center()
        .build()
        .map_err(|e| e.to_string())?;
    win.set_focus().map_err(|e| e.to_string())
}

/// Where "Install Command-Line Tool" links sb.
#[cfg(unix)]
const CLI_LINK: &str = "/usr/local/bin/sb";

/// Links /usr/local/bin/sb to the sb bundled with the app, so the command
/// updates with the app. Asks for an administrator password in the OS dialog.
/// Runs on a blocking thread: it shows modal dialogs.
pub fn install_cli(app: &AppHandle) {
    if let Err(e) = try_install_cli(app) {
        error_dialog(app, &e);
    }
}

#[cfg(unix)]
fn try_install_cli(app: &AppHandle) -> Result<(), String> {
    use std::path::Path;

    let exe = std::env::current_exe().map_err(|e| e.to_string())?;
    let sb = exe.with_file_name("sb");
    if !sb.is_file() {
        return Err(format!(
            "The bundled sb binary is missing ({}). Reinstall Switchboard.",
            sb.display()
        ));
    }
    let src = sb.to_string_lossy().into_owned();
    if src.contains("/AppTranslocation/") || src.starts_with("/Volumes/") {
        return Err(
            "Move Switchboard to your Applications folder and open it from there first; \
                    the command links to this copy of the app."
                .into(),
        );
    }
    if std::fs::read_link(CLI_LINK).ok().as_deref() == Some(Path::new(&src)) {
        info_dialog(app, &format!("The sb command is already installed at {CLI_LINK}."));
        return Ok(());
    }
    if std::fs::symlink_metadata(CLI_LINK).is_ok() {
        let replace = app
            .dialog()
            .message(format!(
                "{CLI_LINK} already exists (perhaps from Homebrew or install.sh). Replace it with a link to the sb in this app?"
            ))
            .title("Install Command-Line Tool")
            .kind(MessageDialogKind::Warning)
            .buttons(MessageDialogButtons::OkCancelCustom("Replace".into(), "Cancel".into()))
            .blocking_show();
        if !replace {
            return Ok(());
        }
    }
    let cmd = format!("mkdir -p /usr/local/bin && ln -sfn {} {CLI_LINK}", sh_quote(&src));
    run_as_admin(&cmd, "Switchboard wants to install the sb command in /usr/local/bin.")?;
    info_dialog(
        app,
        &format!("Installed. Open a new terminal and run: sb --help\n\n{CLI_LINK} → {src}"),
    );
    Ok(())
}

#[cfg(not(unix))]
fn try_install_cli(_: &AppHandle) -> Result<(), String> {
    Err("Installing the command-line tool isn't supported on this OS yet.".into())
}

/// Runs a shell command as root behind the OS password dialog: osascript on
/// macOS, pkexec on Linux. Cancelling the dialog is an error.
#[cfg(unix)]
fn run_as_admin(cmd: &str, prompt: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    let out = std::process::Command::new("/usr/bin/osascript")
        .arg("-e")
        .arg(format!(
            "do shell script {} with prompt {} with administrator privileges",
            applescript_string(cmd),
            applescript_string(prompt)
        ))
        .output();
    #[cfg(not(target_os = "macos"))]
    let out = {
        let _ = prompt;
        std::process::Command::new("pkexec")
            .args(["/bin/sh", "-c", cmd])
            .output()
    };
    let out = out.map_err(|e| e.to_string())?;
    if !out.status.success() {
        let err = String::from_utf8_lossy(&out.stderr);
        return Err(if err.contains("User canceled") || err.contains("(-128)") {
            "Cancelled; nothing was changed.".into()
        } else {
            format!("Couldn't install the command: {}", err.trim())
        });
    }
    Ok(())
}

/// s quoted for /bin/sh.
pub fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// s as an AppleScript string literal.
pub fn applescript_string(s: &str) -> String {
    format!("\"{}\"", s.replace('\\', r"\\").replace('"', "\\\""))
}

/// Checks for a newer version of the app. With an updater key configured, it
/// uses the signed Tauri updater manifest on the update host and can install
/// and restart; without one it can only point to the latest GitHub release.
pub async fn check_for_updates(app: &AppHandle) {
    if updater_pubkey(app).is_none() {
        return check_github_release(app).await;
    }
    if let Err(e) = check_signed_update(app).await {
        error_dialog(app, &format!("Couldn't check for updates: {e}"));
    }
}

/// The updater plugin's public key, if this build has one.
fn updater_pubkey(app: &AppHandle) -> Option<String> {
    let cfg = app.config().plugins.0.get("updater")?;
    cfg.get("pubkey")?.as_str().filter(|k| !k.is_empty()).map(str::to_owned)
}

async fn check_signed_update(app: &AppHandle) -> Result<(), String> {
    use tauri_plugin_updater::UpdaterExt;
    let current = app.package_info().version.to_string();
    let update = app
        .updater()
        .map_err(|e| e.to_string())?
        .check()
        .await
        .map_err(|e| e.to_string())?;
    let Some(update) = update else {
        info_dialog(app, &format!("You're up to date (v{current})."));
        return Ok(());
    };
    // Staged rollout, decided exactly as sb self-update does.
    let percent = update
        .raw_json
        .get("rollout_percent")
        .and_then(|v| v.as_u64())
        .unwrap_or(100)
        .min(100) as u32;
    let id = rollout::install_id(&daemon::config_dir()).map_err(|e| format!("install ID: {e}"))?;
    if !rollout::in_rollout(rollout::bucket(&id, &update.version), percent) {
        info_dialog(
            app,
            &format!(
                "Switchboard {} is rolling out gradually, and this Mac isn't included yet. Check again later.",
                update.version
            ),
        );
        return Ok(());
    }
    let app2 = app.clone();
    app.dialog()
        .message(format!(
            "Switchboard {} is available. You have v{current}. Install it and restart the app?\n\n\
             The update is signed, and checked before it's installed.",
            update.version
        ))
        .title("Update Available")
        .buttons(MessageDialogButtons::OkCancelCustom(
            "Install and Restart".into(),
            "Later".into(),
        ))
        .show(move |install| {
            if !install {
                return;
            }
            tauri::async_runtime::spawn(async move {
                match update.download_and_install(|_, _| {}, || {}).await {
                    Ok(()) => app2.restart(),
                    Err(e) => error_dialog(&app2, &format!("The update didn't install: {e}")),
                }
            });
        });
    Ok(())
}

/// Compares the app's version with the latest GitHub release and offers to
/// open its download page.
async fn check_github_release(app: &AppHandle) {
    let current = app.package_info().version.to_string();
    let latest = async {
        let client = reqwest::Client::builder()
            .user_agent(format!("switchboard-tray/{current}"))
            .timeout(std::time::Duration::from_secs(10))
            .build()
            .map_err(|e| e.to_string())?;
        let res = client
            .get(RELEASES_API)
            .header("Accept", "application/vnd.github+json")
            .send()
            .await
            .map_err(|e| e.to_string())?;
        if !res.status().is_success() {
            return Err(format!("GitHub answered {}", res.status()));
        }
        let v: serde_json::Value = res.json().await.map_err(|e| e.to_string())?;
        let tag = v["tag_name"].as_str().ok_or("no tag_name in the release")?.to_owned();
        let url = v["html_url"]
            .as_str()
            .unwrap_or("https://github.com/nanaaikinson/switchboard/releases")
            .to_owned();
        Ok::<_, String>((tag, url))
    }
    .await;
    match latest {
        Err(e) => error_dialog(app, &format!("Couldn't check for updates: {e}")),
        Ok((tag, url)) if version::newer(&tag, &current) => {
            let app2 = app.clone();
            app.dialog()
                .message(format!("Switchboard {tag} is available. You have v{current}."))
                .title("Update Available")
                .buttons(MessageDialogButtons::OkCancelCustom("Download".into(), "Later".into()))
                .show(move |download| {
                    if download {
                        let _ = app2.opener().open_url(url, None::<&str>);
                    }
                });
        }
        Ok(_) => info_dialog(app, &format!("You're up to date (v{current}).")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quoting() {
        assert_eq!(
            sh_quote("/Applications/My Apps/it's/sb"),
            r"'/Applications/My Apps/it'\''s/sb'"
        );
        assert_eq!(applescript_string(r#"ln -s 'a' "b\c""#), r#""ln -s 'a' \"b\\c\"""#);
    }

    /// The shell must get the path back unchanged, and run nothing in it.
    #[cfg(unix)]
    #[test]
    fn sh_quote_round_trips() {
        let tricky = "/tmp/a b/$(touch /tmp/sb-tray-pwned)/`id`/it's";
        let out = std::process::Command::new("/bin/sh")
            .arg("-c")
            .arg(format!("printf %s {}", sh_quote(tricky)))
            .output()
            .unwrap();
        assert_eq!(String::from_utf8_lossy(&out.stdout), tricky);
        assert!(!std::path::Path::new("/tmp/sb-tray-pwned").exists());
    }
}
