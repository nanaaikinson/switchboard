//! Release version comparison for "Check for Updates".

/// Parses "v1.2.3" or "1.2.3-rc.1" into (major, minor, patch, is_prerelease).
fn parse(v: &str) -> Option<(u64, u64, u64, bool)> {
    let v = v.trim().trim_start_matches('v');
    let (core, pre) = match v.split_once(['-', '+']) {
        Some((c, rest)) => (c, !rest.is_empty() && v.contains('-')),
        None => (v, false),
    };
    let mut it = core.split('.').map(|p| p.parse::<u64>().ok());
    let (a, b, c) = (it.next()??, it.next()??, it.next()??);
    it.next().is_none().then_some((a, b, c, pre))
}

/// Reports whether release tag `latest` is newer than `current`. A final
/// release is newer than its own pre-releases. Unparseable versions are
/// never newer.
pub fn newer(latest: &str, current: &str) -> bool {
    match (parse(latest), parse(current)) {
        (Some((a1, b1, c1, p1)), Some((a2, b2, c2, p2))) => {
            (a1, b1, c1) > (a2, b2, c2) || ((a1, b1, c1) == (a2, b2, c2) && p2 && !p1)
        }
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::newer;

    #[test]
    fn compares() {
        for (latest, current, want) in [
            ("v0.2.0", "0.1.0", true),
            ("v0.1.1", "0.1.0", true),
            ("v1.0.0", "0.9.9", true),
            ("v0.10.0", "0.9.0", true), // numeric, not text
            ("v0.1.0", "0.1.0", false),
            ("v0.1.0", "0.2.0", false),
            ("v0.1.0", "0.1.0-rc.2", true), // the final release beats its pre-releases
            ("v0.1.0-rc.2", "0.1.0", false),
            ("v0.1.0+build.5", "0.1.0", false),
            ("garbage", "0.1.0", false),
            ("v1.2", "0.1.0", false),
        ] {
            assert_eq!(newer(latest, current), want, "newer({latest}, {current})");
        }
    }
}
