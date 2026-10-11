package dockerfile

// instructionDoc describes an instruction for hover and completion.
type instructionDoc struct {
	Syntax  string
	Summary string
	Snippet string
	Flags   []flagDoc
}

type flagDoc struct {
	Name    string
	Value   string // placeholder text; "" for boolean flags
	Summary string
}

const referenceURL = "https://docs.docker.com/reference/dockerfile/#"

var instructions = map[string]instructionDoc{
	"FROM": {"FROM [--platform=<platform>] <image>[:<tag>|@<digest>] [AS <name>]",
		"Starts a new build stage from a base image. A Dockerfile must start with FROM (only ARG may come before it). `AS <name>` names the stage so later stages can copy from it.",
		"FROM ${1:image}:${2:tag}${3: AS ${4:name}}",
		[]flagDoc{{"platform", "linux/amd64", "Platform of the base image, e.g. `linux/arm64` or `$BUILDPLATFORM`."}}},
	"RUN": {"RUN [--mount=...] [--network=...] [--security=...] <command> | [\"exec\", \"form\"]",
		"Runs a command in a new layer on top of the current image. Shell form runs through `/bin/sh -c` (or the SHELL instruction); heredocs (`<<EOF`) allow multi-line scripts.",
		"RUN ${1:command}",
		[]flagDoc{
			{"mount", "type=cache,target=/root/.cache", "Mounts a cache, secret, ssh agent, bind or tmpfs for this command only (`type=cache|bind|tmpfs|secret|ssh`)."},
			{"network", "default", "Network mode for the command: `default`, `none` or `host`."},
			{"security", "insecure", "`insecure` runs the command with elevated privileges (needs the `security.insecure` entitlement)."},
		}},
	"CMD": {"CMD [\"executable\", \"param\"] | command param",
		"Default command (or default arguments for ENTRYPOINT) when a container starts. Only the last CMD in a stage takes effect. Prefer the JSON (exec) form so signals reach the process.",
		"CMD [\"${1:executable}\"${2:, \"${3:param}\"}]", nil},
	"ENTRYPOINT": {"ENTRYPOINT [\"executable\", \"param\"] | command param",
		"Configures the container to run as an executable; CMD then supplies default arguments. Only the last ENTRYPOINT takes effect. Prefer the JSON (exec) form.",
		"ENTRYPOINT [\"${1:executable}\"]", nil},
	"LABEL": {"LABEL <key>=<value> ...",
		"Adds metadata to the image, e.g. `org.opencontainers.image.source`.",
		"LABEL ${1:key}=\"${2:value}\"", nil},
	"MAINTAINER": {"MAINTAINER <name>",
		"Deprecated. Use `LABEL org.opencontainers.image.authors=...` instead.", "", nil},
	"EXPOSE": {"EXPOSE <port>[/<protocol>] ...",
		"Documents the ports the container listens on. It does not publish them; use `-p` at run time.",
		"EXPOSE ${1:8080}", nil},
	"ENV": {"ENV <key>=<value> ...",
		"Sets environment variables for the following instructions and for containers started from the image.",
		"ENV ${1:KEY}=${2:value}", nil},
	"ADD": {"ADD [--chown=...] [--chmod=...] [--checksum=...] [--keep-git-dir] [--link] <src>... <dest>",
		"Copies files, URLs or git repositories into the image and extracts local tar archives. Prefer COPY for plain files.",
		"ADD ${1:src} ${2:dest}",
		[]flagDoc{
			{"chown", "user:group", "Owner of the copied files."},
			{"chmod", "0644", "Permissions of the copied files."},
			{"checksum", "sha256:", "Verifies the checksum of a remote file."},
			{"keep-git-dir", "", "Keeps the `.git` directory when adding a git repository."},
			{"link", "", "Copies into an independent layer, so the layer can be reused when earlier layers change."},
			{"exclude", "pattern", "Excludes files matching the pattern."},
		}},
	"COPY": {"COPY [--from=<stage|image>] [--chown=...] [--chmod=...] [--link] [--parents] [--exclude=...] <src>... <dest>",
		"Copies files from the build context, or from another stage or image with `--from`, into the image.",
		"COPY ${1:src} ${2:dest}",
		[]flagDoc{
			{"from", "stage", "Copies from a previous build stage (name or index) or from an image."},
			{"chown", "user:group", "Owner of the copied files."},
			{"chmod", "0644", "Permissions of the copied files."},
			{"link", "", "Copies into an independent layer, so the layer can be reused when earlier layers change."},
			{"parents", "", "Keeps the parent directories of the source paths."},
			{"exclude", "pattern", "Excludes files matching the pattern."},
		}},
	"VOLUME": {"VOLUME [\"/path\"] | /path ...",
		"Declares mount points for externally managed data. Changes to these paths after the VOLUME instruction are discarded.",
		"VOLUME [\"${1:/data}\"]", nil},
	"USER": {"USER <user>[:<group>]",
		"Sets the user (and group) for the following RUN, CMD and ENTRYPOINT instructions. Running as non-root is recommended.",
		"USER ${1:user}", nil},
	"WORKDIR": {"WORKDIR <path>",
		"Sets the working directory for the following instructions; it is created if missing. Use absolute paths.",
		"WORKDIR ${1:/app}", nil},
	"ARG": {"ARG <name>[=<default>] ...",
		"Declares a build argument, set with `--build-arg`. An ARG before FROM is only visible in FROM lines unless declared again inside the stage.",
		"ARG ${1:NAME}${2:=${3:default}}", nil},
	"ONBUILD": {"ONBUILD <INSTRUCTION>",
		"Registers an instruction that runs when the image is used as the base of another build.",
		"ONBUILD ${1:instruction}", nil},
	"STOPSIGNAL": {"STOPSIGNAL <signal>",
		"Signal sent to the container to stop it, e.g. `SIGTERM` or `SIGQUIT`.",
		"STOPSIGNAL ${1:SIGTERM}", nil},
	"HEALTHCHECK": {"HEALTHCHECK [--interval=...] [--timeout=...] [--start-period=...] [--start-interval=...] [--retries=...] CMD <command> | NONE",
		"Tells Docker how to test that the container still works. `HEALTHCHECK NONE` disables an inherited check.",
		"HEALTHCHECK --interval=${1:30s} --timeout=${2:5s} CMD ${3:command}",
		[]flagDoc{
			{"interval", "30s", "Time between checks."},
			{"timeout", "30s", "Time after which a check counts as failed."},
			{"start-period", "0s", "Grace period after start during which failures don't count."},
			{"start-interval", "5s", "Time between checks during the start period."},
			{"retries", "3", "Consecutive failures needed to report unhealthy."},
		}},
	"SHELL": {"SHELL [\"executable\", \"parameters\"]",
		"Overrides the shell used for the shell form of RUN, CMD and ENTRYPOINT, e.g. `[\"/bin/bash\", \"-o\", \"pipefail\", \"-c\"]`.",
		"SHELL [\"${1:/bin/bash}\", \"-o\", \"pipefail\", \"-c\"]", nil},
}

// instructionOrder is the order completion offers instructions in.
var instructionOrder = []string{"FROM", "RUN", "COPY", "WORKDIR", "ENV", "ARG", "CMD", "ENTRYPOINT",
	"EXPOSE", "USER", "LABEL", "ADD", "VOLUME", "HEALTHCHECK", "SHELL", "STOPSIGNAL", "ONBUILD", "MAINTAINER"}

// mountTypes and mountOptions help completing --mount values.
var mountOptions = map[string][]string{
	"cache":  {"target=", "id=", "sharing=locked", "sharing=shared", "sharing=private", "from=", "source=", "mode=", "uid=", "gid=", "ro"},
	"bind":   {"target=", "source=", "from=", "rw"},
	"tmpfs":  {"target=", "size="},
	"secret": {"id=", "target=", "env=", "required=true", "mode=", "uid=", "gid="},
	"ssh":    {"id=", "target=", "required=true", "mode=", "uid=", "gid="},
}
