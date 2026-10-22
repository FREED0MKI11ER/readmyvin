# Change FREED0MKI11ER/readmyvin to your repo if different.
# Run once on the server:  sudo bash deploy/install.sh
#
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "please run as root (sudo bash deploy/install.sh)" >&2
  exit 1
fi

HERE="$(cd "$(dirname "$0")" && pwd)"

install -m 0755 "$HERE/readmyvin-update.sh" /usr/local/bin/readmyvin-update.sh
install -m 0644 "$HERE/readmyvin-update.service" /etc/systemd/system/readmyvin-update.service
install -m 0644 "$HERE/readmyvin-update.timer" /etc/systemd/system/readmyvin-update.timer

if [ ! -f /etc/default/readmyvin-update ]; then
  install -m 0644 "$HERE/readmyvin-update.env.example" /etc/default/readmyvin-update
  echo "Wrote /etc/default/readmyvin-update - edit it if your service/binary/paths differ."
fi

systemctl daemon-reload
systemctl enable --now readmyvin-update.timer

echo "Running an update check now..."
/usr/local/bin/readmyvin-update.sh || true

echo
echo "Installed. Timer status:"
systemctl status readmyvin-update.timer --no-pager || true
echo
echo "Logs:  journalctl -u readmyvin-update.service -n 50 --no-pager"
