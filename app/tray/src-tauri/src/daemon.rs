//! Client for the Switchboard daemon's control API, over its Unix socket in
//! the config dir (see docs/api.md).

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct Listener {
    #[serde(default)]
    pub addrs: Option<Vec<String>>,
    #[serde(default)]
    pub listening: bool,
    #[serde(default)]
    pub error: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
pub struct RouteStatus {
    pub name: String,
    pub port: u16,
    #[serde(default)]
    pub wildcard: bool,
    pub health: String,
    pub source: String,
    #[serde(default)]
    pub container: Option<String>,
    /// Set for routes under .local (experimental mDNS mode): "announced",
    /// "pending" or "wildcard".
    #[serde(default)]
    pub mdns: Option<String>,
}

impl RouteStatus {
    /// A menu note for routes under .local, which is experimental.
    pub fn mdns_note(&self) -> &'static str {
        match self.mdns.as_deref() {
            None => "",
            Some("wildcard") => "  (.local: not on mDNS)",
            Some(_) => "  (.local, experimental)",
        }
    }
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq)]
pub struct Status {
    pub version: String,
    pub tlds: Vec<String>,
    #[serde(default)]
    pub proxy: Listener,
    #[serde(default)]
    pub https: Listener,
    #[serde(default)]
    pub paused: bool,
    #[serde(default)]
    pub routes: Vec<RouteStatus>,
}

#[derive(Debug, Serialize)]
pub struct NewRoute {
    pub name: String,
    pub port: u16,
    pub wildcard: bool,
    pub redirect_https: bool,
}

/// The control socket, `sb.sock` in the config dir.
pub fn socket_path() -> PathBuf {
    config_dir().join("sb.sock")
}

/// Switchboard's config dir: `$SWITCHBOARD_CONFIG_DIR`, else
/// `$XDG_CONFIG_HOME/switchboard`, else `~/.config/switchboard` (the same
/// search as the sb binary).
pub fn config_dir() -> PathBuf {
    std::env::var_os("SWITCHBOARD_CONFIG_DIR")
        .map(PathBuf::from)
        .or_else(|| {
            std::env::var_os("XDG_CONFIG_HOME")
                .map(PathBuf::from)
                .filter(|p| p.is_absolute())
                .map(|p| p.join("switchboard"))
        })
        .unwrap_or_else(|| home().join(".config").join("switchboard"))
}

fn home() -> PathBuf {
    std::env::var_os("HOME").map(PathBuf::from).unwrap_or_default()
}

impl Status {
    /// The URL a route opens at: https:// while HTTPS is listening, else
    /// http://, with the port when it isn't the default. None for a pure
    /// wildcard like *.x.test.
    pub fn route_url(&self, name: &str) -> Option<String> {
        if name.starts_with("*.") {
            return None;
        }
        let (scheme, l, default) = if self.https.listening {
            ("https", &self.https, "443")
        } else {
            ("http", &self.proxy, "80")
        };
        Some(format!("{scheme}://{name}{}/", port_suffix(l, default)))
    }

    /// The dashboard's origin, if HTTPS is listening.
    pub fn dashboard_origin(&self) -> Option<String> {
        let tld = self.tlds.first()?;
        self.https
            .listening
            .then(|| format!("https://switchboard.{tld}{}", port_suffix(&self.https, "443")))
    }
}

fn port_suffix(l: &Listener, default: &str) -> String {
    match l
        .addrs
        .as_ref()
        .and_then(|a| a.first())
        .and_then(|a| a.rsplit_once(':'))
    {
        Some((_, port)) if port != default => format!(":{port}"),
        _ => String::new(),
    }
}

/// An error message from the daemon ({"error": "..."}) or the connection.
fn api_error(status: u16, body: &[u8]) -> String {
    #[derive(Deserialize)]
    struct E {
        error: String,
    }
    serde_json::from_slice::<E>(body)
        .map(|e| e.error)
        .unwrap_or_else(|_| format!("HTTP {status}"))
}

#[cfg(unix)]
mod unix {
    use bytes::Bytes;
    use http_body_util::{BodyExt, Full};
    use hyper::{Request, body::Incoming, client::conn::http1};
    use hyper_util::rt::TokioIo;
    use tokio::net::UnixStream;

    use super::*;

    async fn send(method: &str, path: &str, body: Option<Vec<u8>>) -> Result<hyper::Response<Incoming>, String> {
        let stream = UnixStream::connect(socket_path())
            .await
            .map_err(|e| format!("the Switchboard daemon isn't running ({e})"))?;
        let (mut sender, conn) = http1::handshake(TokioIo::new(stream))
            .await
            .map_err(|e| e.to_string())?;
        tokio::spawn(conn);
        let mut req = Request::builder().method(method).uri(path).header("host", "sb");
        if body.is_some() {
            req = req.header("content-type", "application/json");
        }
        let req = req
            .body(Full::new(Bytes::from(body.unwrap_or_default())))
            .map_err(|e| e.to_string())?;
        sender.send_request(req).await.map_err(|e| e.to_string())
    }

    async fn call(method: &str, path: &str, body: Option<Vec<u8>>) -> Result<Bytes, String> {
        let res = send(method, path, body).await?;
        let status = res.status();
        let bytes = res.into_body().collect().await.map_err(|e| e.to_string())?.to_bytes();
        if !status.is_success() {
            return Err(api_error(status.as_u16(), &bytes));
        }
        Ok(bytes)
    }

    pub async fn status() -> Result<Status, String> {
        let b = call("GET", "/v1/status", None).await?;
        serde_json::from_slice(&b).map_err(|e| format!("decode status: {e}"))
    }

    pub async fn put_route(r: &NewRoute) -> Result<(), String> {
        call(
            "POST",
            "/v1/routes",
            Some(serde_json::to_vec(r).map_err(|e| e.to_string())?),
        )
        .await?;
        Ok(())
    }

    pub async fn set_paused(paused: bool) -> Result<(), String> {
        call(
            "POST",
            "/v1/pause",
            Some(serde_json::json!({ "paused": paused }).to_string().into_bytes()),
        )
        .await?;
        Ok(())
    }

    pub async fn dashboard_login() -> Result<String, String> {
        let b = call("POST", "/v1/dashboard/login", Some(b"{}".to_vec())).await?;
        let v: serde_json::Value = serde_json::from_slice(&b).map_err(|e| e.to_string())?;
        v["token"]
            .as_str()
            .map(str::to_owned)
            .ok_or_else(|| "no token in the daemon's answer".into())
    }

    /// Follows /v1/events, calling on_event with each event's type, until the
    /// stream ends. Returns why it ended.
    pub async fn follow_events(mut on_event: impl FnMut(&str)) -> String {
        let res = match send("GET", "/v1/events", None).await {
            Ok(r) => r,
            Err(e) => return e,
        };
        let mut body = res.into_body();
        let mut buf = String::new();
        loop {
            match body.frame().await {
                None => return "event stream closed".into(),
                Some(Err(e)) => return e.to_string(),
                Some(Ok(frame)) => {
                    if let Some(data) = frame.data_ref() {
                        buf.push_str(&String::from_utf8_lossy(data));
                        for t in drain_event_types(&mut buf) {
                            on_event(&t);
                        }
                    }
                }
            }
        }
    }
}

#[cfg(unix)]
pub use unix::*;

/// On Windows the daemon isn't supported yet; every call reports that.
#[cfg(not(unix))]
mod unsupported {
    use super::*;
    const MSG: &str = "the Switchboard daemon isn't supported on this OS yet";
    pub async fn status() -> Result<Status, String> {
        Err(MSG.into())
    }
    pub async fn put_route(_: &NewRoute) -> Result<(), String> {
        Err(MSG.into())
    }
    pub async fn set_paused(_: bool) -> Result<(), String> {
        Err(MSG.into())
    }
    pub async fn dashboard_login() -> Result<String, String> {
        Err(MSG.into())
    }
    pub async fn follow_events(_: impl FnMut(&str)) -> String {
        MSG.into()
    }
}

#[cfg(not(unix))]
pub use unsupported::*;

/// Removes complete server-sent events ("...\n\n") from buf and returns their
/// types. Comments (": keepalive") are dropped; data lines are ignored,
/// since the tray reloads the status after any event.
pub fn drain_event_types(buf: &mut String) -> Vec<String> {
    let mut out = Vec::new();
    while let Some(end) = buf.find("\n\n") {
        let block: String = buf.drain(..end + 2).collect();
        for line in block.lines() {
            if let Some(t) = line.strip_prefix("event:") {
                out.push(t.trim().to_owned());
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mdns_note_marks_local_routes() {
        let route = |mdns: Option<&str>| RouteStatus {
            name: "x".into(),
            port: 1,
            wildcard: false,
            health: "up".into(),
            source: "config".into(),
            container: None,
            mdns: mdns.map(Into::into),
        };
        assert_eq!(route(None).mdns_note(), "");
        assert_eq!(route(Some("announced")).mdns_note(), "  (.local, experimental)");
        assert_eq!(route(Some("pending")).mdns_note(), "  (.local, experimental)");
        assert_eq!(route(Some("wildcard")).mdns_note(), "  (.local: not on mDNS)");
    }

    fn listener(addr: &str, listening: bool) -> Listener {
        Listener {
            addrs: Some(vec![addr.into()]),
            listening,
            error: None,
        }
    }

    #[test]
    fn route_urls() {
        let mut st = Status {
            tlds: vec!["test".into()],
            https: listener("127.0.0.1:443", true),
            proxy: listener("127.0.0.1:80", true),
            ..Default::default()
        };
        assert_eq!(st.route_url("myapp.test").as_deref(), Some("https://myapp.test/"));
        assert_eq!(st.route_url("*.myapp.test"), None);
        assert_eq!(st.dashboard_origin().as_deref(), Some("https://switchboard.test"));
        st.https = listener("127.0.0.1:8443", true);
        assert_eq!(st.route_url("a.test").as_deref(), Some("https://a.test:8443/"));
        assert_eq!(st.dashboard_origin().as_deref(), Some("https://switchboard.test:8443"));
        // HTTPS down: plain HTTP, keeping a non-default port.
        st.https = listener("127.0.0.1:443", false);
        st.proxy = listener("[::1]:8080", true);
        assert_eq!(st.route_url("a.test").as_deref(), Some("http://a.test:8080/"));
        assert_eq!(st.dashboard_origin(), None);
    }

    #[test]
    fn server_sent_events() {
        let mut buf = String::from(
            ": connected\n\nevent: route.added\ndata: {}\n\nevent: health.changed\ndata: {\"x\":1}\n\nevent: par",
        );
        assert_eq!(drain_event_types(&mut buf), vec!["route.added", "health.changed"]);
        assert_eq!(buf, "event: par"); // incomplete: kept for the next frame
        buf.push_str("tial\ndata: {}\n\n: keepalive\n\n");
        assert_eq!(drain_event_types(&mut buf), vec!["partial"]);
        assert!(buf.is_empty());
    }

    #[test]
    fn status_decodes_the_daemon_json() {
        let st: Status = serde_json::from_str(
            r#"{"version":"v1","uptime_seconds":3,"tlds":["test"],"dns":{"addrs":["127.0.0.1:15353"],"listening":true},
               "proxy":{"addrs":null,"listening":false,"error":"bind"},"https":{"addrs":["127.0.0.1:443"],"listening":true},
               "docker":{"enabled":false,"connected":false},"paused":true,
               "routes":[{"name":"web.test","port":8080,"wildcard":false,"redirect_https":true,"health":"up","source":"docker","container":"web"}]}"#,
        )
        .unwrap();
        assert!(st.paused && !st.proxy.listening && st.routes[0].container.as_deref() == Some("web"));
    }

    #[test]
    fn api_errors() {
        assert_eq!(
            api_error(400, br#"{"error":"invalid route: bad"}"#),
            "invalid route: bad"
        );
        assert_eq!(api_error(502, b"<html>"), "HTTP 502");
    }

    #[test]
    fn socket_path_follows_the_config_dir() {
        // SAFETY: tests in this module don't read these variables concurrently.
        unsafe { std::env::set_var("SWITCHBOARD_CONFIG_DIR", "/tmp/sbcfg") };
        assert_eq!(socket_path(), PathBuf::from("/tmp/sbcfg/sb.sock"));
        unsafe { std::env::remove_var("SWITCHBOARD_CONFIG_DIR") };
    }
}
