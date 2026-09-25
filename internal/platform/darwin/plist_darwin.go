package darwin

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
)

// Labels identify Switchboard's launchd jobs.
const (
	HelperLabel = "dev.switchboard.helper"
	DaemonLabel = "dev.switchboard.daemon"
)

// resolverMarker starts every resolver file Switchboard writes. Uninstall
// removes only files with the marker, so resolver files from other local DNS
// tools, which often also say "nameserver 127.0.0.1", are never touched.
const resolverMarker = "# Managed by Switchboard; removed by 'sb uninstall'\n"

// resolverContent is the /etc/resolver/<tld> file.
func resolverContent(port int) []byte {
	return []byte(resolverMarker + "nameserver 127.0.0.1\nport " + strconv.Itoa(port) + "\n")
}

// helperPlist runs the helper as root at boot and restarts it if it exits.
func helperPlist(binary string, uid int, logPath string) []byte {
	return plist(HelperLabel, []string{binary, "helper", "serve", "--uid", strconv.Itoa(uid)}, logPath)
}

// agentPlist runs `sb daemon` as the user at login and restarts it on crash.
func agentPlist(sbPath, logPath string) []byte {
	return plist(DaemonLabel, []string{sbPath, "daemon"}, logPath)
}

func plist(label string, args []string, logPath string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", esc(label))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", esc(a))
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", esc(logPath))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", esc(logPath))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
