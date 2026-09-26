package update

// ReleaseKey is the minisign public key ("RW...") release archives and
// manifests are signed with: the "RW..." line of the release key's .pub file,
// the same value as the SB_UPDATE_PUBLIC_KEY repository variable and as the
// key pinned in install/install.sh and install/install.ps1 (a test checks).
// A build can override it,
//
//	-ldflags "-X github.com/nanaaikinson/switchboard/internal/update.ReleaseKey=RW..."
//
// which .goreleaser.yaml does when SB_UPDATE_PUBLIC_KEY is set. Empty
// disables self-update.
var ReleaseKey = "RWTezUT5l2DDkBWjTKayvSXpwTUkehmo3D8dXmRrmdv8IX6AT94tI15d"
