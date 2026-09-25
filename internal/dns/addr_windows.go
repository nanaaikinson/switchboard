package dns

// DefaultAddr is the default listen address for UDP and TCP. Windows NRPT
// rules name a DNS server by IP only, so it must be port 53. Windows has no
// privileged ports, so the daemon can bind it as the user.
const DefaultAddr = "127.0.0.1:53"
