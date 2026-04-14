package cli

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	gopshost "github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gopsnet "github.com/shirou/gopsutil/v3/net"
	"github.com/spf13/cobra"
)

func newHostCommand(ctx *commandContext) *cobra.Command {
	root := &cobra.Command{
		Use:   "host",
		Short: "Host-level commands for the deployed gateway server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(newHostStatusCommand(ctx))
	return root
}

// newHostStatusCommand returns `host status`. It collects host metrics aligned
// with the default-enabled collectors of prometheus/node_exporter.
func newHostStatusCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show host system metrics for the gateway server",
		Long: `Display host-level metrics from the machine where the Koios gateway is running.

Covers the same surface as prometheus/node_exporter default collectors (Linux):
  uname        – kernel / hostname
  os           – OS release info
  cpu          – logical CPUs, model, per-CPU usage
  cpufreq      – per-CPU frequency (MHz)
  loadavg      – 1m / 5m / 15m load averages
  meminfo      – RAM and swap utilisation
  filesystem   – per-mount disk space (excludes virtual FSes)
  diskstats    – per-device I/O counters
  netdev       – per-interface network byte/packet counters
  netstat      – TCP/UDP protocol counters
  sockstat     – socket counts (/proc/net/sockstat)
  stat         – forks, context switches, procs running/blocked
  vmstat       – page faults, OOM kills, swap I/O
  entropy      – available kernel entropy
  filefd       – allocated/maximum file descriptors
  pressure     – PSI (Pressure Stall Information) for cpu, memory, io
  conntrack    – netfilter connection tracking counts
  thermal_zone – sensor temperatures
  dmi          – DMI/SMBIOS hardware identity`,
		RunE: func(cmd *cobra.Command, args []string) error {
			collectCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			payload, err := collectHostMetrics(collectCtx)
			if err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, payload)
				return nil
			}
			printHostStatus(cmd, payload)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "collection timeout")
	return cmd
}

// ── Metric structs ────────────────────────────────────────────────────────────

type hostMetrics struct {
	CollectedAt time.Time               `json:"collected_at"`
	System      hostSystem              `json:"system"`
	CPU         hostCPU                 `json:"cpu"`
	LoadAvg     hostLoadAvg             `json:"load_avg"`
	Memory      hostMemory              `json:"memory"`
	Filesystems []hostFilesystem        `json:"filesystems,omitempty"`
	DiskIO      map[string]hostDiskIO   `json:"disk_io,omitempty"`
	Network     map[string]hostNetIface `json:"network,omitempty"`
	NetStat     *hostNetStat            `json:"netstat,omitempty"`
	SockStat    *hostSockStat           `json:"sockstat,omitempty"`
	ProcStat    *hostProcStat           `json:"proc_stat,omitempty"`
	VMStat      *hostVMStat             `json:"vmstat,omitempty"`
	Entropy     *hostEntropy            `json:"entropy,omitempty"`
	FileDesc    *hostFileDesc           `json:"filefd,omitempty"`
	Pressure    *hostPressure           `json:"pressure,omitempty"`
	Conntrack   *hostConntrack          `json:"conntrack,omitempty"`
	Thermal     *hostThermal            `json:"thermal,omitempty"`
	DMI         *hostDMI                `json:"dmi,omitempty"`
}

type hostSystem struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	KernelVersion   string `json:"kernel_version"`
	KernelArch      string `json:"kernel_arch"`
	GoArch          string `json:"go_arch"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
	BootTime        uint64 `json:"boot_time"`
	Procs           uint64 `json:"procs"`
}

// hostCPU covers node_exporter cpu + cpufreq collectors.
type hostCPU struct {
	LogicalCount  int       `json:"logical_count"`
	PhysicalCount int       `json:"physical_count"`
	ModelName     string    `json:"model_name,omitempty"`
	FreqMHz       []float64 `json:"freq_mhz,omitempty"` // cpufreq collector
	UsagePercent  []float64 `json:"usage_percent,omitempty"`
	TotalPercent  float64   `json:"total_percent"`
}

type hostLoadAvg struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type hostMemory struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	FreeBytes      uint64  `json:"free_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
	BuffersBytes   uint64  `json:"buffers_bytes"`
	CachedBytes    uint64  `json:"cached_bytes"`
	SwapTotalBytes uint64  `json:"swap_total_bytes"`
	SwapUsedBytes  uint64  `json:"swap_used_bytes"`
	SwapFreeBytes  uint64  `json:"swap_free_bytes"`
	SwapPercent    float64 `json:"swap_percent"`
}

type hostFilesystem struct {
	Device      string  `json:"device"`
	Mountpoint  string  `json:"mountpoint"`
	Fstype      string  `json:"fstype"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedBytes   uint64  `json:"used_bytes"`
	FreeBytes   uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type hostDiskIO struct {
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
	ReadCount  uint64 `json:"read_count"`
	WriteCount uint64 `json:"write_count"`
}

type hostNetIface struct {
	BytesSent   uint64 `json:"bytes_sent"`
	BytesRecv   uint64 `json:"bytes_recv"`
	PacketsSent uint64 `json:"packets_sent"`
	PacketsRecv uint64 `json:"packets_recv"`
	Errin       uint64 `json:"errin"`
	Errout      uint64 `json:"errout"`
	Dropin      uint64 `json:"dropin"`
	Dropout     uint64 `json:"dropout"`
}

// hostNetStat covers node_exporter netstat collector (TCP/UDP counters).
type hostNetStat struct {
	TCPActiveOpens  int64 `json:"tcp_active_opens"`
	TCPPassiveOpens int64 `json:"tcp_passive_opens"`
	TCPAttemptFails int64 `json:"tcp_attempt_fails"`
	TCPEstabResets  int64 `json:"tcp_estab_resets"`
	TCPCurrEstab    int64 `json:"tcp_curr_estab"`
	TCPInSegs       int64 `json:"tcp_in_segs"`
	TCPOutSegs      int64 `json:"tcp_out_segs"`
	TCPRetransSegs  int64 `json:"tcp_retrans_segs"`
	UDPInDatagrams  int64 `json:"udp_in_datagrams"`
	UDPOutDatagrams int64 `json:"udp_out_datagrams"`
	UDPInErrors     int64 `json:"udp_in_errors"`
}

// hostSockStat covers node_exporter sockstat collector (/proc/net/sockstat).
type hostSockStat struct {
	SocketsUsed int64 `json:"sockets_used"`
	TCPInUse    int64 `json:"tcp_inuse"`
	TCPOrphan   int64 `json:"tcp_orphan"`
	TCPTimewait int64 `json:"tcp_timewait"`
	TCPAlloc    int64 `json:"tcp_alloc"`
	UDPInUse    int64 `json:"udp_inuse"`
	UDP6InUse   int64 `json:"udp6_inuse"`
}

// hostProcStat covers node_exporter stat collector (/proc/stat).
type hostProcStat struct {
	ProcsRunning    int64 `json:"procs_running"`
	ProcsBlocked    int64 `json:"procs_blocked"`
	ContextSwitches int64 `json:"context_switches_total"`
	ForksTotal      int64 `json:"forks_total"`
	InterruptsTotal int64 `json:"interrupts_total"`
}

// hostVMStat covers node_exporter vmstat collector (/proc/vmstat).
type hostVMStat struct {
	PgFault    int64 `json:"pgfault_total"`
	PgMajFault int64 `json:"pgmajfault_total"`
	PgPgIn     int64 `json:"pgin_total"`
	PgPgOut    int64 `json:"pgout_total"`
	SwapIn     int64 `json:"swapin_total"`
	SwapOut    int64 `json:"swapout_total"`
	OOMKill    int64 `json:"oom_kill_total"`
}

// hostEntropy covers node_exporter entropy collector
// (/proc/sys/kernel/random/entropy_avail).
type hostEntropy struct {
	Available int64 `json:"available"`
}

// hostFileDesc covers node_exporter filefd collector (/proc/sys/fs/file-nr).
type hostFileDesc struct {
	Allocated int64 `json:"allocated"`
	Maximum   int64 `json:"maximum"`
}

// hostPressure covers node_exporter pressure collector (/proc/pressure/).
// Available on Linux 4.20+ with CONFIG_PSI.
type hostPressure struct {
	CPUSomeAvg10  float64 `json:"cpu_some_avg10"`
	CPUSomeAvg60  float64 `json:"cpu_some_avg60"`
	CPUSomeAvg300 float64 `json:"cpu_some_avg300"`
	MemSomeAvg10  float64 `json:"mem_some_avg10"`
	MemSomeAvg60  float64 `json:"mem_some_avg60"`
	MemSomeAvg300 float64 `json:"mem_some_avg300"`
	MemFullAvg10  float64 `json:"mem_full_avg10"`
	MemFullAvg60  float64 `json:"mem_full_avg60"`
	MemFullAvg300 float64 `json:"mem_full_avg300"`
	IOSomeAvg10   float64 `json:"io_some_avg10"`
	IOSomeAvg60   float64 `json:"io_some_avg60"`
	IOSomeAvg300  float64 `json:"io_some_avg300"`
	IOFullAvg10   float64 `json:"io_full_avg10"`
	IOFullAvg60   float64 `json:"io_full_avg60"`
	IOFullAvg300  float64 `json:"io_full_avg300"`
}

// hostConntrack covers node_exporter conntrack collector
// (/proc/sys/net/netfilter/).
type hostConntrack struct {
	Current int64 `json:"current"`
	Maximum int64 `json:"maximum"`
}

// hostThermal covers node_exporter thermal_zone collector.
type hostThermal struct {
	Sensors map[string]float64 `json:"sensors"`
}

// hostDMI covers node_exporter dmi collector (/sys/class/dmi/id/).
type hostDMI struct {
	BIOSVendor     string `json:"bios_vendor,omitempty"`
	BIOSVersion    string `json:"bios_version,omitempty"`
	ProductName    string `json:"product_name,omitempty"`
	ProductVendor  string `json:"product_vendor,omitempty"`
	ProductVersion string `json:"product_version,omitempty"`
	BoardName      string `json:"board_name,omitempty"`
	BoardVendor    string `json:"board_vendor,omitempty"`
}

// ── Collector ─────────────────────────────────────────────────────────────────

func collectHostMetrics(ctx context.Context) (*hostMetrics, error) {
	m := &hostMetrics{
		CollectedAt: time.Now().UTC(),
		DiskIO:      make(map[string]hostDiskIO),
		Network:     make(map[string]hostNetIface),
	}

	// uname / os (node_exporter: uname, os collectors)
	if info, err := gopshost.InfoWithContext(ctx); err == nil {
		m.System = hostSystem{
			Hostname:        info.Hostname,
			OS:              info.OS,
			Platform:        info.Platform,
			PlatformVersion: info.PlatformVersion,
			KernelVersion:   info.KernelVersion,
			KernelArch:      info.KernelArch,
			GoArch:          runtime.GOARCH,
			UptimeSeconds:   info.Uptime,
			BootTime:        info.BootTime,
			Procs:           info.Procs,
		}
	}

	// cpu + cpufreq (node_exporter: cpu, cpufreq collectors)
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		m.CPU.LogicalCount = n
	}
	if n, err := cpu.CountsWithContext(ctx, false); err == nil {
		m.CPU.PhysicalCount = n
	}
	if infos, err := cpu.InfoWithContext(ctx); err == nil && len(infos) > 0 {
		m.CPU.ModelName = infos[0].ModelName
		freqs := make([]float64, len(infos))
		for i, ci := range infos {
			freqs[i] = round2(ci.Mhz)
		}
		m.CPU.FreqMHz = freqs
	}
	if percents, err := cpu.PercentWithContext(ctx, 0, true); err == nil {
		m.CPU.UsagePercent = roundSlice(percents, 2)
		m.CPU.TotalPercent = round2(avg(percents))
	}

	// loadavg (node_exporter: loadavg collector)
	if la, err := load.AvgWithContext(ctx); err == nil {
		m.LoadAvg = hostLoadAvg{
			Load1:  round2(la.Load1),
			Load5:  round2(la.Load5),
			Load15: round2(la.Load15),
		}
	}

	// meminfo (node_exporter: meminfo collector)
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		m.Memory = hostMemory{
			TotalBytes:     vm.Total,
			UsedBytes:      vm.Used,
			FreeBytes:      vm.Free,
			AvailableBytes: vm.Available,
			UsedPercent:    round2(vm.UsedPercent),
			BuffersBytes:   vm.Buffers,
			CachedBytes:    vm.Cached,
		}
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		m.Memory.SwapTotalBytes = sw.Total
		m.Memory.SwapUsedBytes = sw.Used
		m.Memory.SwapFreeBytes = sw.Free
		m.Memory.SwapPercent = round2(sw.UsedPercent)
	}

	// filesystem (node_exporter: filesystem collector)
	// Exclude virtual/pseudo FSes consistent with node_exporter defaults.
	excludedFstypes := map[string]bool{
		"tmpfs": true, "devtmpfs": true, "proc": true, "sysfs": true,
		"devfs": true, "cgroup": true, "cgroup2": true, "pstore": true,
		"debugfs": true, "tracefs": true, "securityfs": true, "bpf": true,
		"fusectl": true, "hugetlbfs": true, "mqueue": true, "rpc_pipefs": true,
		"nsfs": true, "efivarfs": true, "autofs": true,
	}
	if parts, err := disk.PartitionsWithContext(ctx, false); err == nil {
		for _, p := range parts {
			if excludedFstypes[p.Fstype] {
				continue
			}
			if u, err := disk.UsageWithContext(ctx, p.Mountpoint); err == nil {
				m.Filesystems = append(m.Filesystems, hostFilesystem{
					Device:      p.Device,
					Mountpoint:  p.Mountpoint,
					Fstype:      p.Fstype,
					TotalBytes:  u.Total,
					UsedBytes:   u.Used,
					FreeBytes:   u.Free,
					UsedPercent: round2(u.UsedPercent),
				})
			}
		}
	}

	// diskstats (node_exporter: diskstats collector)
	if counters, err := disk.IOCountersWithContext(ctx); err == nil {
		for name, d := range counters {
			m.DiskIO[name] = hostDiskIO{
				ReadBytes:  d.ReadBytes,
				WriteBytes: d.WriteBytes,
				ReadCount:  d.ReadCount,
				WriteCount: d.WriteCount,
			}
		}
	}

	// netdev (node_exporter: netdev collector)
	if ifaces, err := gopsnet.IOCountersWithContext(ctx, true); err == nil {
		for _, iface := range ifaces {
			if iface.Name == "lo" {
				continue
			}
			m.Network[iface.Name] = hostNetIface{
				BytesSent:   iface.BytesSent,
				BytesRecv:   iface.BytesRecv,
				PacketsSent: iface.PacketsSent,
				PacketsRecv: iface.PacketsRecv,
				Errin:       iface.Errin,
				Errout:      iface.Errout,
				Dropin:      iface.Dropin,
				Dropout:     iface.Dropout,
			}
		}
	}

	// netstat (node_exporter: netstat collector) via gopsutil ProtoCounters
	if protos, err := gopsnet.ProtoCountersWithContext(ctx, []string{"tcp", "udp"}); err == nil {
		ns := &hostNetStat{}
		for _, p := range protos {
			switch strings.ToLower(p.Protocol) {
			case "tcp":
				ns.TCPActiveOpens = p.Stats["ActiveOpens"]
				ns.TCPPassiveOpens = p.Stats["PassiveOpens"]
				ns.TCPAttemptFails = p.Stats["AttemptFails"]
				ns.TCPEstabResets = p.Stats["EstabResets"]
				ns.TCPCurrEstab = p.Stats["CurrEstab"]
				ns.TCPInSegs = p.Stats["InSegs"]
				ns.TCPOutSegs = p.Stats["OutSegs"]
				ns.TCPRetransSegs = p.Stats["RetransSegs"]
			case "udp":
				ns.UDPInDatagrams = p.Stats["InDatagrams"]
				ns.UDPOutDatagrams = p.Stats["OutDatagrams"]
				ns.UDPInErrors = p.Stats["InErrors"]
			}
		}
		m.NetStat = ns
	}

	// thermal_zone (node_exporter: thermal_zone collector) via gopsutil sensors
	if temps, err := gopshost.SensorsTemperatures(); err == nil && len(temps) > 0 {
		sensors := make(map[string]float64, len(temps))
		for _, t := range temps {
			if t.Temperature > 0 {
				sensors[t.SensorKey] = round2(t.Temperature)
			}
		}
		if len(sensors) > 0 {
			m.Thermal = &hostThermal{Sensors: sensors}
		}
	}

	// Linux-only /proc collectors — gracefully no-op on other platforms.
	if runtime.GOOS == "linux" {
		m.SockStat = readSockStat()
		m.ProcStat = readProcStat()
		m.VMStat = readVMStat()
		m.Entropy = readEntropy()
		m.FileDesc = readFileDesc()
		m.Pressure = readPressure()
		m.Conntrack = readConntrack()
		m.DMI = readDMI()
	}

	return m, nil
}

// ── Linux /proc readers ───────────────────────────────────────────────────────
// Each function returns nil when the file is absent or unparseable, allowing
// graceful degradation in containers or non-standard kernels.

// readSockStat parses /proc/net/sockstat (node_exporter: sockstat collector).
func readSockStat() *hostSockStat {
	data, err := os.ReadFile("/proc/net/sockstat")
	if err != nil {
		return nil
	}
	s := &hostSockStat{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "sockets:":
			s.SocketsUsed = procFieldInt(fields, "used")
		case "TCP:":
			s.TCPInUse = procFieldInt(fields, "inuse")
			s.TCPOrphan = procFieldInt(fields, "orphan")
			s.TCPTimewait = procFieldInt(fields, "tw")
			s.TCPAlloc = procFieldInt(fields, "alloc")
		case "UDP:":
			s.UDPInUse = procFieldInt(fields, "inuse")
		}
	}
	if data6, err := os.ReadFile("/proc/net/sockstat6"); err == nil {
		sc6 := bufio.NewScanner(strings.NewReader(string(data6)))
		for sc6.Scan() {
			fields := strings.Fields(sc6.Text())
			if len(fields) > 0 && fields[0] == "UDP6:" {
				s.UDP6InUse = procFieldInt(fields, "inuse")
			}
		}
	}
	return s
}

// readProcStat parses /proc/stat (node_exporter: stat collector).
func readProcStat() *hostProcStat {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil
	}
	s := &hostProcStat{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "ctxt":
			s.ContextSwitches, _ = strconv.ParseInt(fields[1], 10, 64)
		case "processes":
			s.ForksTotal, _ = strconv.ParseInt(fields[1], 10, 64)
		case "procs_running":
			s.ProcsRunning, _ = strconv.ParseInt(fields[1], 10, 64)
		case "procs_blocked":
			s.ProcsBlocked, _ = strconv.ParseInt(fields[1], 10, 64)
		case "intr":
			if len(fields) >= 2 {
				s.InterruptsTotal, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
	}
	return s
}

// readVMStat parses /proc/vmstat (node_exporter: vmstat collector).
func readVMStat() *hostVMStat {
	data, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return nil
	}
	kv := parseKVFile(string(data))
	return &hostVMStat{
		PgFault:    kv["pgfault"],
		PgMajFault: kv["pgmajfault"],
		PgPgIn:     kv["pgpgin"],
		PgPgOut:    kv["pgpgout"],
		SwapIn:     kv["pswpin"],
		SwapOut:    kv["pswpout"],
		OOMKill:    kv["oom_kill"],
	}
}

// readEntropy reads /proc/sys/kernel/random/entropy_avail
// (node_exporter: entropy collector).
func readEntropy() *hostEntropy {
	v, err := readIntFile("/proc/sys/kernel/random/entropy_avail")
	if err != nil {
		return nil
	}
	return &hostEntropy{Available: v}
}

// readFileDesc reads /proc/sys/fs/file-nr (node_exporter: filefd collector).
func readFileDesc() *hostFileDesc {
	data, err := os.ReadFile("/proc/sys/fs/file-nr")
	if err != nil {
		return nil
	}
	// format: allocated\t0\tmaximum
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil
	}
	alloc, err1 := strconv.ParseInt(fields[0], 10, 64)
	max, err2 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	return &hostFileDesc{Allocated: alloc, Maximum: max}
}

// readPressure parses /proc/pressure/{cpu,memory,io}
// (node_exporter: pressure collector, Linux 4.20+ / CONFIG_PSI).
func readPressure() *hostPressure {
	parsePSI := func(path string) (some [3]float64, full [3]float64, ok bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			var target *[3]float64
			switch fields[0] {
			case "some":
				target = &some
			case "full":
				target = &full
			default:
				continue
			}
			for _, f := range fields[1:] {
				parts := strings.SplitN(f, "=", 2)
				if len(parts) != 2 {
					continue
				}
				v, err := strconv.ParseFloat(parts[1], 64)
				if err != nil {
					continue
				}
				switch parts[0] {
				case "avg10":
					target[0] = v
				case "avg60":
					target[1] = v
				case "avg300":
					target[2] = v
				}
			}
		}
		ok = true
		return
	}

	some, _, ok := parsePSI("/proc/pressure/cpu")
	if !ok {
		return nil // PSI not supported on this kernel
	}
	p := &hostPressure{
		CPUSomeAvg10:  some[0],
		CPUSomeAvg60:  some[1],
		CPUSomeAvg300: some[2],
	}
	if ms, mf, ok := parsePSI("/proc/pressure/memory"); ok {
		p.MemSomeAvg10, p.MemSomeAvg60, p.MemSomeAvg300 = ms[0], ms[1], ms[2]
		p.MemFullAvg10, p.MemFullAvg60, p.MemFullAvg300 = mf[0], mf[1], mf[2]
	}
	if is, iof, ok := parsePSI("/proc/pressure/io"); ok {
		p.IOSomeAvg10, p.IOSomeAvg60, p.IOSomeAvg300 = is[0], is[1], is[2]
		p.IOFullAvg10, p.IOFullAvg60, p.IOFullAvg300 = iof[0], iof[1], iof[2]
	}
	return p
}

// readConntrack reads netfilter conntrack counts
// (node_exporter: conntrack collector).
func readConntrack() *hostConntrack {
	cur, err1 := readIntFile("/proc/sys/net/netfilter/nf_conntrack_count")
	max, err2 := readIntFile("/proc/sys/net/netfilter/nf_conntrack_max")
	if err1 != nil && err2 != nil {
		return nil
	}
	return &hostConntrack{Current: cur, Maximum: max}
}

// readDMI reads hardware identity from /sys/class/dmi/id/
// (node_exporter: dmi collector).
func readDMI() *hostDMI {
	const base = "/sys/class/dmi/id/"
	read := func(name string) string {
		b, err := os.ReadFile(base + name)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	d := &hostDMI{
		BIOSVendor:     read("bios_vendor"),
		BIOSVersion:    read("bios_version"),
		ProductName:    read("product_name"),
		ProductVendor:  read("sys_vendor"),
		ProductVersion: read("product_version"),
		BoardName:      read("board_name"),
		BoardVendor:    read("board_vendor"),
	}
	if d.BIOSVendor == "" && d.ProductName == "" && d.BoardName == "" {
		return nil
	}
	return d
}

// ── Parsing helpers ───────────────────────────────────────────────────────────

func readIntFile(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

// parseKVFile parses "key value\n" files like /proc/vmstat.
func parseKVFile(data string) map[string]int64 {
	m := make(map[string]int64)
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			m[fields[0]] = v
		}
	}
	return m
}

// procFieldInt finds key in a fields slice and returns the int64 that follows it.
func procFieldInt(fields []string, key string) int64 {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == key {
			v, err := strconv.ParseInt(fields[i+1], 10, 64)
			if err == nil {
				return v
			}
		}
	}
	return 0
}

// ── Printer ───────────────────────────────────────────────────────────────────

func printHostStatus(cmd *cobra.Command, m *hostMetrics) {
	w := cmd.OutOrStdout()

	fmt.Fprintf(w, "Host Status  (%s)\n", m.CollectedAt.Format(time.RFC3339))
	fmt.Fprintln(w, strings.Repeat("─", 60))

	// System / uname + os + dmi
	fmt.Fprintf(w, "  Hostname        : %s\n", m.System.Hostname)
	fmt.Fprintf(w, "  OS              : %s %s (%s)\n", m.System.Platform, m.System.PlatformVersion, m.System.OS)
	fmt.Fprintf(w, "  Kernel          : %s  arch=%s\n", m.System.KernelVersion, m.System.KernelArch)
	fmt.Fprintf(w, "  Uptime          : %s\n", formatDuration(time.Duration(m.System.UptimeSeconds)*time.Second))
	fmt.Fprintf(w, "  Processes       : %d\n", m.System.Procs)
	if m.DMI != nil {
		if m.DMI.ProductName != "" {
			fmt.Fprintf(w, "  Hardware        : %s %s (%s)\n",
				m.DMI.ProductVendor, m.DMI.ProductName, m.DMI.ProductVersion)
		}
		if m.DMI.BIOSVendor != "" {
			fmt.Fprintf(w, "  BIOS            : %s %s\n", m.DMI.BIOSVendor, m.DMI.BIOSVersion)
		}
	}
	fmt.Fprintln(w)

	// CPU (cpu + cpufreq)
	fmt.Fprintln(w, "  CPU")
	if m.CPU.ModelName != "" {
		fmt.Fprintf(w, "    Model         : %s\n", m.CPU.ModelName)
	}
	fmt.Fprintf(w, "    Logical       : %d  Physical: %d\n", m.CPU.LogicalCount, m.CPU.PhysicalCount)
	fmt.Fprintf(w, "    Usage (total) : %.2f%%\n", m.CPU.TotalPercent)
	if len(m.CPU.UsagePercent) > 0 && len(m.CPU.UsagePercent) <= 32 {
		parts := make([]string, len(m.CPU.UsagePercent))
		for i, p := range m.CPU.UsagePercent {
			parts[i] = fmt.Sprintf("cpu%d=%.1f%%", i, p)
		}
		fmt.Fprintf(w, "    Per-CPU usage : %s\n", strings.Join(parts, "  "))
	}
	if len(m.CPU.FreqMHz) > 0 && len(m.CPU.FreqMHz) <= 32 {
		parts := make([]string, len(m.CPU.FreqMHz))
		for i, f := range m.CPU.FreqMHz {
			parts[i] = fmt.Sprintf("cpu%d=%.0fMHz", i, f)
		}
		fmt.Fprintf(w, "    Freq (cpufreq): %s\n", strings.Join(parts, "  "))
	}
	fmt.Fprintln(w)

	// Load average + proc_stat
	fmt.Fprintf(w, "  Load Avg        : %.2f  %.2f  %.2f  (1m / 5m / 15m)\n",
		m.LoadAvg.Load1, m.LoadAvg.Load5, m.LoadAvg.Load15)
	if m.ProcStat != nil {
		fmt.Fprintf(w, "  Procs Running   : %d  Blocked: %d\n",
			m.ProcStat.ProcsRunning, m.ProcStat.ProcsBlocked)
		fmt.Fprintf(w, "  Context Switches: %d  Forks: %d  Interrupts: %d\n",
			m.ProcStat.ContextSwitches, m.ProcStat.ForksTotal, m.ProcStat.InterruptsTotal)
	}
	fmt.Fprintln(w)

	// Pressure (PSI — pressure collector)
	if m.Pressure != nil {
		fmt.Fprintln(w, "  Pressure (PSI)          avg10   avg60   avg300")
		fmt.Fprintf(w, "    CPU  some            : %6.2f  %6.2f  %6.2f\n",
			m.Pressure.CPUSomeAvg10, m.Pressure.CPUSomeAvg60, m.Pressure.CPUSomeAvg300)
		fmt.Fprintf(w, "    Mem  some / full     : %6.2f  %6.2f  %6.2f / %6.2f  %6.2f  %6.2f\n",
			m.Pressure.MemSomeAvg10, m.Pressure.MemSomeAvg60, m.Pressure.MemSomeAvg300,
			m.Pressure.MemFullAvg10, m.Pressure.MemFullAvg60, m.Pressure.MemFullAvg300)
		fmt.Fprintf(w, "    I/O  some / full     : %6.2f  %6.2f  %6.2f / %6.2f  %6.2f  %6.2f\n",
			m.Pressure.IOSomeAvg10, m.Pressure.IOSomeAvg60, m.Pressure.IOSomeAvg300,
			m.Pressure.IOFullAvg10, m.Pressure.IOFullAvg60, m.Pressure.IOFullAvg300)
		fmt.Fprintln(w)
	}

	// Memory (meminfo + vmstat)
	fmt.Fprintln(w, "  Memory (RAM)")
	fmt.Fprintf(w, "    Total         : %s\n", fmtBytes(m.Memory.TotalBytes))
	fmt.Fprintf(w, "    Used          : %s  (%.2f%%)\n", fmtBytes(m.Memory.UsedBytes), m.Memory.UsedPercent)
	fmt.Fprintf(w, "    Available     : %s\n", fmtBytes(m.Memory.AvailableBytes))
	if m.Memory.BuffersBytes > 0 || m.Memory.CachedBytes > 0 {
		fmt.Fprintf(w, "    Buffers       : %s   Cached: %s\n",
			fmtBytes(m.Memory.BuffersBytes), fmtBytes(m.Memory.CachedBytes))
	}
	if m.Memory.SwapTotalBytes > 0 {
		fmt.Fprintf(w, "    Swap          : %s / %s  (%.2f%%)\n",
			fmtBytes(m.Memory.SwapUsedBytes), fmtBytes(m.Memory.SwapTotalBytes), m.Memory.SwapPercent)
	}
	if m.VMStat != nil {
		fmt.Fprintf(w, "    Page faults   : minor=%d  major=%d  OOM kills=%d\n",
			m.VMStat.PgFault, m.VMStat.PgMajFault, m.VMStat.OOMKill)
		fmt.Fprintf(w, "    Swap I/O      : in=%d  out=%d\n", m.VMStat.SwapIn, m.VMStat.SwapOut)
	}
	fmt.Fprintln(w)

	// Kernel resources (entropy + filefd)
	if m.Entropy != nil || m.FileDesc != nil {
		fmt.Fprintln(w, "  Kernel Resources")
		if m.Entropy != nil {
			fmt.Fprintf(w, "    Entropy avail : %d bits\n", m.Entropy.Available)
		}
		if m.FileDesc != nil {
			pct := float64(0)
			if m.FileDesc.Maximum > 0 {
				pct = float64(m.FileDesc.Allocated) / float64(m.FileDesc.Maximum) * 100
			}
			fmt.Fprintf(w, "    File desc     : %d / %d  (%.1f%%)\n",
				m.FileDesc.Allocated, m.FileDesc.Maximum, pct)
		}
		fmt.Fprintln(w)
	}

	// Conntrack
	if m.Conntrack != nil && m.Conntrack.Maximum > 0 {
		pct := float64(m.Conntrack.Current) / float64(m.Conntrack.Maximum) * 100
		fmt.Fprintf(w, "  Conntrack       : %d / %d  (%.1f%%)\n",
			m.Conntrack.Current, m.Conntrack.Maximum, pct)
		fmt.Fprintln(w)
	}

	// Filesystems (filesystem collector)
	if len(m.Filesystems) > 0 {
		fmt.Fprintln(w, "  Filesystems")
		fmt.Fprintf(w, "    %-30s  %-8s  %8s  %8s  %6s\n", "Mount", "Type", "Total", "Used", "Use%")
		for _, fs := range m.Filesystems {
			fmt.Fprintf(w, "    %-30s  %-8s  %8s  %8s  %5.1f%%\n",
				fs.Mountpoint, fs.Fstype,
				fmtBytes(fs.TotalBytes), fmtBytes(fs.UsedBytes),
				fs.UsedPercent)
		}
		fmt.Fprintln(w)
	}

	// Disk I/O (diskstats collector)
	if len(m.DiskIO) > 0 {
		fmt.Fprintln(w, "  Disk I/O")
		fmt.Fprintf(w, "    %-16s  %10s  %10s\n", "Device", "Read", "Written")
		for name, d := range m.DiskIO {
			fmt.Fprintf(w, "    %-16s  %10s  %10s\n",
				name, fmtBytes(d.ReadBytes), fmtBytes(d.WriteBytes))
		}
		fmt.Fprintln(w)
	}

	// Network interfaces (netdev collector)
	if len(m.Network) > 0 {
		fmt.Fprintln(w, "  Network Interfaces (netdev)")
		fmt.Fprintf(w, "    %-16s  %10s  %10s  %8s  %8s\n",
			"Interface", "Recv", "Sent", "Err-in", "Err-out")
		for name, iface := range m.Network {
			fmt.Fprintf(w, "    %-16s  %10s  %10s  %8d  %8d\n",
				name, fmtBytes(iface.BytesRecv), fmtBytes(iface.BytesSent),
				iface.Errin, iface.Errout)
		}
		fmt.Fprintln(w)
	}

	// Sockets (sockstat collector)
	if m.SockStat != nil {
		fmt.Fprintln(w, "  Sockets (sockstat)")
		fmt.Fprintf(w, "    Sockets used  : %d\n", m.SockStat.SocketsUsed)
		fmt.Fprintf(w, "    TCP           : inuse=%d  orphan=%d  timewait=%d  alloc=%d\n",
			m.SockStat.TCPInUse, m.SockStat.TCPOrphan,
			m.SockStat.TCPTimewait, m.SockStat.TCPAlloc)
		fmt.Fprintf(w, "    UDP           : inuse=%d  (v6: %d)\n",
			m.SockStat.UDPInUse, m.SockStat.UDP6InUse)
		fmt.Fprintln(w)
	}

	// TCP/UDP protocol counters (netstat collector)
	if m.NetStat != nil {
		fmt.Fprintln(w, "  TCP/UDP Stats (netstat)")
		fmt.Fprintf(w, "    TCP curr_estab : %d  retrans=%d\n",
			m.NetStat.TCPCurrEstab, m.NetStat.TCPRetransSegs)
		fmt.Fprintf(w, "    TCP opens      : active=%d  passive=%d  fails=%d  resets=%d\n",
			m.NetStat.TCPActiveOpens, m.NetStat.TCPPassiveOpens,
			m.NetStat.TCPAttemptFails, m.NetStat.TCPEstabResets)
		fmt.Fprintf(w, "    TCP segments   : in=%d  out=%d\n",
			m.NetStat.TCPInSegs, m.NetStat.TCPOutSegs)
		fmt.Fprintf(w, "    UDP datagrams  : in=%d  out=%d  errors=%d\n",
			m.NetStat.UDPInDatagrams, m.NetStat.UDPOutDatagrams, m.NetStat.UDPInErrors)
		fmt.Fprintln(w)
	}

	// Temperature sensors (thermal_zone collector)
	if m.Thermal != nil && len(m.Thermal.Sensors) > 0 {
		fmt.Fprintln(w, "  Temperatures (thermal_zone)")
		for key, temp := range m.Thermal.Sensors {
			fmt.Fprintf(w, "    %-30s : %.1f°C\n", key, temp)
		}
		fmt.Fprintln(w)
	}
}

// ── Formatters ────────────────────────────────────────────────────────────────

// fmtBytes formats a byte count as a human-readable IEC string.
func fmtBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}

// formatDuration renders a duration as "Xd Yh Zm".
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func roundSlice(fs []float64, decimals int) []float64 {
	factor := math.Pow(10, float64(decimals))
	out := make([]float64, len(fs))
	for i, f := range fs {
		out[i] = math.Round(f*factor) / factor
	}
	return out
}

func avg(fs []float64) float64 {
	if len(fs) == 0 {
		return 0
	}
	var sum float64
	for _, f := range fs {
		sum += f
	}
	return sum / float64(len(fs))
}
