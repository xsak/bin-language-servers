package ini

// Common systemd unit and podman Quadlet directives, per section, with short descriptions
// (written for bls; see systemd.unit(5), systemd.service(5), systemd.exec(5), podman-systemd.unit(5)
// for the full reference). The list is not exhaustive, so unknown directives are never reported.

type directive struct {
	name, doc string
	values    []string // suggested values
}

// closedValues lists directives whose values must be one of the suggestions (others accept
// free text, so their suggestions are only hints).
var closedValues = map[string]bool{"Service.Type": true, "Service.Restart": true, "Service.KillMode": true,
	"Service.ProtectSystem": true, "Service.ProtectHome": true, "Service.RemainAfterExit": true,
	"Service.NoNewPrivileges": true, "Service.PrivateTmp": true, "Service.DynamicUser": true,
	"Timer.Persistent": true, "Timer.WakeSystem": true, "Socket.Accept": true, "Unit.DefaultDependencies": true}

var yesNo = []string{"yes", "no"}

var systemdSections = map[string][]directive{
	"Unit": {
		{"Description", "Human-readable name of the unit, shown in logs and `systemctl status`.", nil},
		{"Documentation", "Space-separated URIs with documentation (`man:`, `https:`, `file:`).", nil},
		{"Requires", "Units that must be active; if they fail to start, this unit fails too.", nil},
		{"Wants", "Weaker Requires: units started along with this one, failures are ignored.", nil},
		{"BindsTo", "Like Requires, and this unit stops when the listed unit stops.", nil},
		{"PartOf", "Stop/restart of the listed unit is propagated to this unit.", nil},
		{"Requisite", "Units that must already be active; they are not started.", nil},
		{"Conflicts", "Units that are stopped when this unit starts (and vice versa).", nil},
		{"Before", "Ordering: this unit starts before the listed units.", nil},
		{"After", "Ordering: this unit starts after the listed units (e.g. `network-online.target`).", []string{"network-online.target", "network.target", "local-fs.target", "remote-fs.target", "time-sync.target"}},
		{"OnFailure", "Units activated when this unit enters the failed state.", nil},
		{"OnSuccess", "Units activated when this unit finishes successfully.", nil},
		{"StartLimitIntervalSec", "Time window for StartLimitBurst; `0` disables rate limiting.", nil},
		{"StartLimitBurst", "Number of starts allowed within StartLimitIntervalSec.", nil},
		{"ConditionPathExists", "Start only if the path exists (`!` negates).", nil},
		{"ConditionPathIsDirectory", "Start only if the path is a directory.", nil},
		{"AssertPathExists", "Like ConditionPathExists, but failing is an error.", nil},
		{"DefaultDependencies", "Whether implicit default dependencies are added.", yesNo},
		{"RequiresMountsFor", "Adds Requires= and After= for the mount units of these paths.", nil},
	},
	"Service": {
		{"Type", "How systemd decides the service has started.", []string{"simple", "exec", "forking", "oneshot", "dbus", "notify", "notify-reload", "idle"}},
		{"ExecStart", "Command run to start the service. Prefix `-` ignores failure; absolute path recommended.", nil},
		{"ExecStartPre", "Commands run before ExecStart.", nil},
		{"ExecStartPost", "Commands run after ExecStart.", nil},
		{"ExecReload", "Command run on `systemctl reload`.", nil},
		{"ExecStop", "Command run to stop the service (otherwise KillSignal is sent).", nil},
		{"ExecStopPost", "Commands run after the service stopped.", nil},
		{"Restart", "When the service is restarted after it exits.", []string{"no", "on-success", "on-failure", "on-abnormal", "on-watchdog", "on-abort", "always"}},
		{"RestartSec", "Time to wait before a restart (e.g. `5s`).", nil},
		{"TimeoutStartSec", "Time allowed for start-up; `infinity` disables.", nil},
		{"TimeoutStopSec", "Time allowed for stopping before SIGKILL.", nil},
		{"RemainAfterExit", "Consider the service active after all processes exited (useful with Type=oneshot).", yesNo},
		{"PIDFile", "PID file of a forking service.", nil},
		{"User", "User the processes run as.", nil},
		{"Group", "Group the processes run as.", nil},
		{"DynamicUser", "Run as a transient user allocated at start.", yesNo},
		{"WorkingDirectory", "Working directory; `~` means the user's home.", nil},
		{"Environment", "Environment variables: `KEY=value` (space-separated, quote values with spaces).", nil},
		{"EnvironmentFile", "File with KEY=value lines; `-` prefix ignores a missing file.", nil},
		{"StandardOutput", "Where stdout goes.", []string{"journal", "inherit", "null", "tty", "kmsg", "file:", "append:"}},
		{"StandardError", "Where stderr goes.", []string{"journal", "inherit", "null", "tty", "kmsg", "file:", "append:"}},
		{"KillMode", "Which processes are killed on stop.", []string{"control-group", "mixed", "process", "none"}},
		{"KillSignal", "Signal sent to stop the service.", []string{"SIGTERM", "SIGINT", "SIGQUIT", "SIGKILL"}},
		{"LimitNOFILE", "Maximum number of open files.", nil},
		{"Nice", "Scheduling priority (-20..19).", nil},
		{"NoNewPrivileges", "Processes can't gain privileges (setuid etc.).", yesNo},
		{"PrivateTmp", "Private /tmp and /var/tmp.", yesNo},
		{"ProtectSystem", "Mount /usr, /boot (and /etc with `full`/`strict`) read-only.", []string{"yes", "no", "full", "strict"}},
		{"ProtectHome", "Make /home, /root, /run/user inaccessible or read-only.", []string{"yes", "no", "read-only", "tmpfs"}},
		{"ReadWritePaths", "Paths writable despite ProtectSystem.", nil},
		{"CapabilityBoundingSet", "Capabilities the processes may have.", nil},
		{"AmbientCapabilities", "Capabilities passed to the processes.", nil},
		{"WatchdogSec", "Watchdog timeout; the service must call sd_notify(WATCHDOG=1).", nil},
		{"MemoryMax", "Hard memory limit (cgroup), e.g. `512M`.", nil},
		{"CPUQuota", "CPU time limit, e.g. `50%`.", nil},
	},
	"Install": {
		{"WantedBy", "Targets that pull this unit in when it is enabled.", []string{"multi-user.target", "default.target", "graphical.target", "timers.target", "sockets.target"}},
		{"RequiredBy", "Units that require this unit when it is enabled.", nil},
		{"Alias", "Additional names for the unit when enabled.", nil},
		{"Also", "Units enabled/disabled together with this one.", nil},
	},
	"Timer": {
		{"OnCalendar", "Calendar event, e.g. `daily`, `Mon *-*-* 02:00:00`, `*:0/15`. Check with `systemd-analyze calendar`.", []string{"minutely", "hourly", "daily", "weekly", "monthly", "yearly"}},
		{"OnBootSec", "Time after boot.", nil},
		{"OnStartupSec", "Time after the service manager started.", nil},
		{"OnUnitActiveSec", "Time after the unit was last activated.", nil},
		{"OnUnitInactiveSec", "Time after the unit was last deactivated.", nil},
		{"Persistent", "Run missed calendar events after downtime.", yesNo},
		{"RandomizedDelaySec", "Random delay added to each run.", nil},
		{"AccuracySec", "How exact the timer must be (default 1min).", nil},
		{"Unit", "Unit to activate (default: same name with .service).", nil},
		{"WakeSystem", "Wake the system from suspend.", yesNo},
	},
	"Socket": {
		{"ListenStream", "TCP port, address or UNIX socket path (stream).", nil},
		{"ListenDatagram", "UDP port, address or UNIX socket path (datagram).", nil},
		{"Accept", "Spawn one service instance per connection.", yesNo},
		{"SocketUser", "Owner of a UNIX socket.", nil},
		{"SocketMode", "Permissions of a UNIX socket.", nil},
		{"Service", "Service to activate (default: same name).", nil},
	},
	"Mount": {
		{"What", "Device, URL or path to mount.", nil},
		{"Where", "Mount point (must match the unit name).", nil},
		{"Type", "File system type.", nil},
		{"Options", "Mount options.", nil},
		{"TimeoutSec", "Time allowed for the mount.", nil},
	},
	"Path": {
		{"PathExists", "Activate when the path exists.", nil},
		{"PathChanged", "Activate when the file is closed after a change.", nil},
		{"PathModified", "Activate on every write.", nil},
		{"DirectoryNotEmpty", "Activate when the directory isn't empty.", nil},
		{"Unit", "Unit to activate (default: same name with .service).", nil},
	},
	// podman Quadlet
	"Container": {
		{"Image", "Image to run, e.g. `docker.io/library/nginx:1.27` (fully qualified recommended).", nil},
		{"ContainerName", "Name of the container (default: systemd-%N).", nil},
		{"Exec", "Command and arguments passed to the container.", nil},
		{"PublishPort", "Port mapping `[ip:]host:container[/proto]`.", nil},
		{"Volume", "Volume or bind mount `source:dest[:options]`.", nil},
		{"Environment", "Environment variable `KEY=value`.", nil},
		{"EnvironmentFile", "File with environment variables.", nil},
		{"Network", "Network to join (a `.network` unit, `host`, `none`, ...).", nil},
		{"Pod", "Quadlet `.pod` unit to join.", nil},
		{"User", "User inside the container.", nil},
		{"UserNS", "User namespace mode, e.g. `keep-id`.", nil},
		{"AutoUpdate", "Enable `podman auto-update`.", []string{"registry", "local"}},
		{"Label", "Container label `key=value`.", nil},
		{"HealthCmd", "Health check command.", nil},
		{"HealthInterval", "Time between health checks.", nil},
		{"Notify", "Let the container send sd_notify readiness.", []string{"true", "false", "healthy"}},
		{"ReadOnly", "Read-only root file system.", []string{"true", "false"}},
		{"NoNewPrivileges", "Disallow privilege escalation.", []string{"true", "false"}},
		{"DropCapability", "Capabilities to drop.", nil},
		{"AddCapability", "Capabilities to add.", nil},
		{"Secret", "Podman secret to expose.", nil},
		{"Tmpfs", "tmpfs mount.", nil},
		{"PodmanArgs", "Extra arguments for `podman run`.", nil},
	},
	"Volume":  {{"VolumeName", "Name of the volume.", nil}, {"Driver", "Volume driver.", nil}, {"Label", "Volume label.", nil}, {"Options", "Driver options.", nil}},
	"Network": {{"NetworkName", "Name of the network.", nil}, {"Subnet", "Subnet in CIDR notation.", nil}, {"Gateway", "Gateway address.", nil}, {"Driver", "Network driver.", []string{"bridge", "macvlan", "ipvlan"}}, {"Internal", "No external access.", []string{"true", "false"}}, {"DNS", "DNS server.", nil}, {"IPv6", "Enable IPv6.", []string{"true", "false"}}},
	"Pod":     {{"PodName", "Name of the pod.", nil}, {"PublishPort", "Port mapping for the pod.", nil}, {"Network", "Network of the pod.", nil}, {"Volume", "Volume for the pod.", nil}},
	"Kube":    {{"Yaml", "Kubernetes YAML file to run with `podman kube play`.", nil}, {"Network", "Network to join.", nil}, {"PublishPort", "Port mapping.", nil}, {"AutoUpdate", "Enable auto-update.", []string{"registry", "local"}}},
	"Image":   {{"Image", "Image to pull.", nil}, {"AllTags", "Pull all tags.", []string{"true", "false"}}, {"Arch", "Architecture.", nil}, {"AuthFile", "Registry auth file.", nil}},
	"Build":   {{"ImageTag", "Tag of the built image.", nil}, {"File", "Containerfile to build.", nil}, {"SetWorkingDirectory", "Build context directory.", nil}, {"Pull", "Pull policy.", []string{"always", "missing", "never", "newer"}}},
}

// sectionsFor returns the sections typical for a unit file extension.
func sectionsFor(ext string) []string {
	base := []string{"Unit", "Install"}
	switch ext {
	case ".service":
		return append(base, "Service")
	case ".timer":
		return append(base, "Timer")
	case ".socket":
		return append(base, "Socket")
	case ".mount", ".automount":
		return append(base, "Mount")
	case ".path":
		return append(base, "Path")
	case ".container":
		return append(base, "Container", "Service")
	case ".volume", ".network", ".pod", ".kube", ".image", ".build":
		name := map[string]string{".volume": "Volume", ".network": "Network", ".pod": "Pod", ".kube": "Kube", ".image": "Image", ".build": "Build"}[ext]
		return append(base, name, "Service")
	case ".target", ".slice", ".scope", ".swap":
		return base
	}
	return nil
}
