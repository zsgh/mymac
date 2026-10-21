package ports

import (
	"syscall"
	"testing"

	gopsnet "github.com/shirou/gopsutil/v4/net"
)

func TestConvertConnection(t *testing.T) {
	raw := gopsnet.ConnectionStat{
		Pid:    42,
		Type:   syscall.SOCK_STREAM,
		Laddr:  gopsnet.Addr{IP: "127.0.0.1", Port: 8080},
		Raddr:  gopsnet.Addr{IP: "0.0.0.0", Port: 0},
		Status: "LISTEN",
	}
	conn, ok := convertConnection(raw)
	if !ok {
		t.Fatal("the connection must convert")
	}
	if conn.Protocol != "tcp" {
		t.Errorf("protocol = %q", conn.Protocol)
	}
	// An IPv4 address is reported as 0.0.0.0.
	if conn.Address != "0.0.0.0" {
		t.Errorf("address = %q", conn.Address)
	}
	if conn.Port != 8080 {
		t.Errorf("port = %d", conn.Port)
	}
	if !conn.IsListen {
		t.Error("a LISTEN socket must be flagged")
	}
}

func TestConvertConnectionRejectsBadInput(t *testing.T) {
	// An unknown socket type.
	if _, ok := convertConnection(gopsnet.ConnectionStat{Pid: 1, Type: 999}); ok {
		t.Error("an unknown socket type must be rejected")
	}
	// An unparsable address.
	raw := gopsnet.ConnectionStat{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "nonsense", Port: 80}}
	if _, ok := convertConnection(raw); ok {
		t.Error("an unparsable address must be rejected")
	}
}

func TestNormalizeAddress(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1": "0.0.0.0",
		"0.0.0.0":   "0.0.0.0",
		"":          "0.0.0.0",
		"*":         "0.0.0.0",
		"::1":       "::1",
		"fe80::1":   "fe80::1",
	}
	for in, want := range cases {
		if got := normalizeAddress(in); got != want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeState(t *testing.T) {
	if got := normalizeState("listen"); got != "LISTEN" {
		t.Errorf("state = %q", got)
	}
	if got := normalizeState(""); got != "UNKNOWN" {
		t.Errorf("empty state = %q", got)
	}
}

func TestGroupConnectionsDeduplicates(t *testing.T) {
	conns := []gopsnet.ConnectionStat{
		{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 80}, Status: "LISTEN"},
		{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 80}, Status: "LISTEN"},
		{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "::1", Port: 80}, Status: "LISTEN"},
		{Pid: 2, Type: syscall.SOCK_DGRAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 53}, Status: ""},
	}
	grouped := groupConnections(conns)
	if len(grouped[1]) != 2 {
		t.Errorf("pid 1 has %d connections, want 2: %+v", len(grouped[1]), grouped[1])
	}
	if len(grouped[2]) != 1 {
		t.Errorf("pid 2 has %d connections, want 1", len(grouped[2]))
	}
	// A UDP socket without a state is UNKNOWN.
	if grouped[2][0].State != "UNKNOWN" {
		t.Errorf("state = %q", grouped[2][0].State)
	}
}

func TestGroupConnectionsSkipsInvalid(t *testing.T) {
	conns := []gopsnet.ConnectionStat{
		{Pid: 0, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 80}},
		{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 0}},
		{Pid: 1, Type: syscall.SOCK_STREAM, Laddr: gopsnet.Addr{IP: "127.0.0.1", Port: 99999}},
	}
	if got := groupConnections(conns); len(got) != 0 {
		t.Errorf("invalid connections must be dropped: %+v", got)
	}
}

func TestValidateCreateTime(t *testing.T) {
	if err := validateCreateTime(100, 100); err != nil {
		t.Errorf("the same time must pass: %v", err)
	}
	if err := validateCreateTime(100, 0); err == nil {
		t.Error("a missing create time must be rejected")
	}
	if err := validateCreateTime(0, 100); err == nil {
		t.Error("an unknown create time must be rejected")
	}
	if err := validateCreateTime(100, 200); err == nil {
		t.Error("a different create time must be rejected")
	}
}

func TestIsKernelThread(t *testing.T) {
	// pid 1 is not a kernel thread by name; it is guarded separately.
	if !isKernelThread(100, 2, "kworker/0:1", "") {
		t.Error("a kernel thread must be detected")
	}
	if !isKernelThread(100, 2, "kernel_task", "") {
		t.Error("kernel_task must be detected")
	}
	if isKernelThread(500, 1, "my-app", "/Applications/my-app") {
		t.Error("a normal process must not be a kernel thread")
	}
}

func TestHardGuardReason(t *testing.T) {
	if hardGuardReason(1, 100, 0, "launchd", "/sbin/launchd") == "" {
		t.Error("pid 1 must be guarded")
	}
	if hardGuardReason(500, 100, 1, "my-app", "/x") != "" {
		t.Error("a normal process must not be guarded")
	}
	if hardGuardReason(100, 100, 1, "mymac", "/x") == "" {
		t.Error("the tool itself must be guarded")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := map[string]string{
		"0s":      "—",
		"90s":     "1分",
		"2h30m":   "2小时30分",
		"50h0m0s": "2天2小时",
	}
	for in, want := range cases {
		got := FormatUptime(mustDuration(t, in))
		if got != want {
			t.Errorf("FormatUptime(%s) = %q, want %q", in, got, want)
		}
	}
}
