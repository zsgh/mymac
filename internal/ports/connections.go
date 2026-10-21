// Package ports implements the port manager: it maps listening ports to the
// processes that own them and allows stopping, killing or restarting them.
package ports

import (
	"fmt"
	"net"
	"strings"
	"syscall"

	gopsnet "github.com/shirou/gopsutil/v4/net"
)

// Connection is one socket of a process.
type Connection struct {
	Port          uint32 `json:"port"`
	Address       string `json:"address"`
	RemoteAddress string `json:"remoteAddress"`
	RemotePort    uint32 `json:"remotePort"`
	Protocol      string `json:"protocol"`
	State         string `json:"state"`
	IsListen      bool   `json:"isListen"`
}

// groupConnections groups the sockets by pid and removes the duplicates.
//
// The key covers every field that distinguishes two sockets, so the same
// port on 127.0.0.1 and on ::1 stays as two entries.
func groupConnections(conns []gopsnet.ConnectionStat) map[int32][]Connection {
	grouped := make(map[int32][]Connection)
	seen := make(map[string]struct{}, len(conns))
	for _, raw := range conns {
		if raw.Pid == 0 || raw.Laddr.Port == 0 || raw.Laddr.Port > 65535 {
			continue
		}
		conn, ok := convertConnection(raw)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%d|%s|%s|%d|%s|%d|%s", raw.Pid, conn.Protocol, conn.Address,
			conn.Port, conn.RemoteAddress, conn.RemotePort, conn.State)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		grouped[raw.Pid] = append(grouped[raw.Pid], conn)
	}
	return grouped
}

// convertConnection turns a gopsutil socket into a Connection.
func convertConnection(raw gopsnet.ConnectionStat) (Connection, bool) {
	proto := protocolForType(raw.Type)
	if proto == "" {
		return Connection{}, false
	}
	address := normalizeAddress(raw.Laddr.IP)
	if address == "" {
		return Connection{}, false
	}
	remote := normalizeRemote(raw.Raddr.IP)
	state := normalizeState(raw.Status)
	if proto == "udp" && (state == "" || state == "NONE") {
		state = "UNKNOWN"
	}
	isListen := state == "LISTEN" && (remote == "" || remote == "0.0.0.0")
	return Connection{
		Port:          raw.Laddr.Port,
		Address:       address,
		RemoteAddress: remote,
		RemotePort:    raw.Raddr.Port,
		Protocol:      proto,
		State:         state,
		IsListen:      isListen,
	}, true
}

// protocolForType maps a socket type to a protocol name.
func protocolForType(socketType uint32) string {
	switch socketType {
	case syscall.SOCK_STREAM:
		return "tcp"
	case syscall.SOCK_DGRAM:
		return "udp"
	}
	return ""
}

// normalizeAddress normalises a local address.
//
// An IPv4 address is reported as 0.0.0.0 so the view groups the IPv4 and
// the IPv6 listeners of the same port.
func normalizeAddress(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "*" {
		return "0.0.0.0"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.To4() != nil {
		return "0.0.0.0"
	}
	return ip
}

// normalizeRemote normalises a remote address.
func normalizeRemote(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "*" {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.To4() != nil {
		return "0.0.0.0"
	}
	return ip
}

// normalizeState normalises a socket state.
func normalizeState(state string) string {
	state = strings.TrimSpace(state)
	state = strings.ToUpper(state)
	if state == "" {
		return "UNKNOWN"
	}
	return state
}
