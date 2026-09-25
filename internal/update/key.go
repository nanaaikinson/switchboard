package update

// ReleaseKey is the minisign public key ("RW...") release archives are
// signed with. It is set at build time,
//
//	-ldflags "-X github.com/nanaaikinson/switchboard/internal/update.ReleaseKey=RW..."
//
// which .goreleaser.yaml does from SB_UPDATE_PUBLIC_KEY; it can also be
// committed here once the release key exists. Empty disables self-update.
var ReleaseKey = ""
