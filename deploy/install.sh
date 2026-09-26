#!/bin/sh
# Install the stompwatch user, directories, config, and systemd unit.
# Run it on the box with sudo. Running it again is safe: it never overwrites
# an existing config, and it starts nothing.
#
# Usage: install.sh [DIR]. DIR holds stompwatch.conf.example,
# camera.env.example and stompwatch.service; the default is the directory
# this script is in. "make install-service" copies them to a private
# directory that mktemp makes on the box, never to the shared /tmp, where
# another local user could plant or swap them first.
set -eu

src=${1:-$(dirname -- "$0")}
if [ ! -f "$src/stompwatch.service" ] || [ ! -f "$src/stompwatch.conf.example" ]; then
	echo "no stompwatch.service and stompwatch.conf.example in $src" >&2
	exit 1
fi

conf=/etc/stompwatch/stompwatch.conf
data=/data

# 1. Service user. It is in group audio, which owns the ALSA devices.
if id -u stompwatch >/dev/null 2>&1; then
	echo "user stompwatch exists"
else
	useradd --system --gid audio --home-dir "$data" --shell /usr/sbin/nologin stompwatch
	echo "created user stompwatch in group audio"
fi

# 2. Config. The directory is group audio, so the service user can enter it.
install -d -m 0750 -g audio /etc/stompwatch
if [ -f "$conf" ]; then
	echo "kept the existing $conf"
else
	install -m 0640 -g audio "$src/stompwatch.conf.example" "$conf"
	echo "wrote $conf from the example"
fi
chgrp audio "$conf"
chmod 0640 "$conf"

# 3. Data directories. The database is $data/noise.db, so the service user
# needs to write in $data itself. $data may be a whole disk that holds other
# things, so it is only created, or handed over when the service user cannot
# write it; its mode is left alone. The recordings and the database are for
# the service user only, so its own directories are mode 0700 and its
# database files 0600: the group audio often holds the login user too.
if [ ! -d "$data" ]; then
	install -d -o stompwatch -g audio -m 0700 "$data"
	echo "created $data, mode 0700"
elif su -s /bin/sh -c "test -w $data" stompwatch; then
	echo "$data is writable by stompwatch"
else
	chown stompwatch:audio "$data"
	echo "gave $data to stompwatch:audio"
fi
for f in "$data/noise.db" "$data/noise.db-wal" "$data/noise.db-shm" "$data/noise.db.lock"; do
	if [ -f "$f" ] && [ "$(stat -c %a "$f")" != 600 ]; then
		chmod 0600 "$f"
		echo "changed $f to mode 0600"
	fi
done
for dir in "$data/clips" "$data/clips/audio" "$data/clips/video" "$data/logs" "$data/snapshots"; do
	if [ ! -d "$dir" ]; then
		install -d -o stompwatch -g audio -m 0700 "$dir"
		echo "created $dir, mode 0700"
		continue
	fi
	owner=$(stat -c %U "$dir")
	if [ "$owner" != stompwatch ]; then
		chown stompwatch:audio "$dir"
		echo "gave $dir to stompwatch (it was $owner's)"
	fi
	mode=$(stat -c %a "$dir")
	if [ "$mode" != 700 ]; then
		chmod 0700 "$dir"
		echo "changed $dir from mode $mode to 0700"
	fi
done

# 4. systemd unit.
install -m 0644 "$src/stompwatch.service" /etc/systemd/system/stompwatch.service
systemctl daemon-reload
echo "installed the systemd unit"

# 5. Check what the service user can actually reach.
ok=yes
# ffmpeg is only needed with a camera. Its absence is a warning here and a
# clear line in the log at start, never a reason for the service not to run.
if command -v ffmpeg >/dev/null 2>&1; then
	echo "ffmpeg is installed: $(ffmpeg -version 2>/dev/null | head -n 1)"
else
	echo "WARNING: ffmpeg is not installed; video needs it: apt install ffmpeg"
fi
# The camera login file, when there is one, must be readable by the service
# user and nobody else.
creds=/etc/stompwatch/camera.env
# The example is installed beside the config, so the owner has something to
# copy. It holds no password, so it is readable; the real file is not.
if [ -f "$src/camera.env.example" ]; then
	install -m 0640 -g audio "$src/camera.env.example" /etc/stompwatch/camera.env.example
fi
if [ ! -f "$creds" ]; then
	echo "no camera login at $creds; copy camera.env.example there when you add a camera"
fi
if [ -f "$creds" ]; then
	# ffmpeg carries the camera URL, password included, on its command
	# line, and Linux shows every command line to every local user. /proc
	# mounted with hidepid hides other users' processes from all but root.
	# This only warns: it never changes the mount.
	opts=$(awk '$2 == "/proc" && $3 == "proc" { o = $4 } END { print o }' /proc/mounts)
	case ",$opts," in
	*,hidepid=invisible,* | *,hidepid=noaccess,* | *,hidepid=ptraceable,* | *,hidepid=1,* | *,hidepid=2,* | *,hidepid=4,*) ;;
	*)
		echo "WARNING: /proc is not mounted with hidepid, so every local user can read"
		echo "  the camera password on ffmpeg's command line (ps, /proc/PID/cmdline)."
		echo "  To hide other users' processes, add this line to /etc/fstab:"
		echo "    proc /proc proc defaults,hidepid=invisible 0 0"
		echo "  then run: mount -o remount /proc"
		;;
	esac
	mode=$(stat -c %a "$creds")
	if [ "$mode" != 600 ]; then
		echo "WARNING: $creds is mode $mode; run: chmod 0600 $creds"
		ok=no
	fi
	if ! su -s /bin/sh -c "test -r $creds" stompwatch; then
		echo "WARNING: the service user cannot read $creds; run: chown stompwatch:audio $creds"
		ok=no
	fi
fi
for path in "$conf" /usr/local/bin/stompwatch; do
	if ! su -s /bin/sh -c "test -r $path" stompwatch; then
		echo "WARNING: the service user cannot read $path"
		ok=no
	fi
done
for path in "$data" "$data/logs"; do
	if ! su -s /bin/sh -c "test -w $path" stompwatch; then
		echo "WARNING: the service user cannot write $path"
		ok=no
	fi
done
if [ "$ok" = no ]; then
	echo "fix the warnings above before starting the service"
	exit 1
fi

echo "the service user can read the config and the binary, and write $data and $data/logs"
echo "next: check $conf, then run: systemctl enable --now stompwatch"
