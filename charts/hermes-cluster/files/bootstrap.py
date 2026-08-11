"""Boot-time self-provision for a headless Hermes session pod.

Runs before `hermes gateway run` on every boot (first boot and every
resume). Provisions this session's gateway identity against the connector
(rotating the secret inside the 2-deep verify window) and rewrites the
GATEWAY_RELAY_* lines of $HERMES_HOME/.env. All other .env content (LLM
keys, etc.) comes from the seeded home and is left untouched.

Never prints secret values.
"""

import json
import os
import pathlib
import re
import sys
import time
import urllib.error
import urllib.request

SESSION_ID = os.environ["HERMES_SESSION_ID"]
RELAY_URL = os.environ.get("HERMES_RELAY_URL", "http://hrc:8420")
BOT_ID = os.environ.get("HERMES_RELAY_BOT_ID", "dev-bot")
TOKEN = pathlib.Path("/run/hermes/provision-token").read_text().strip()
HOME = pathlib.Path(os.environ.get("HERMES_HOME", "/opt/data"))

body = json.dumps(
    {
        "gatewayId": SESSION_ID,
        "platform": "discord",
        "botId": BOT_ID,
        "instanceId": SESSION_ID,
        "displayName": f"hermes session {SESSION_ID}",
        # No wakeUrl: the lifecycle-manager registers it at session create;
        # provision COALESCE semantics keep it across our re-provisions.
    }
).encode()

creds = None
deadline = time.time() + 120
while creds is None:
    req = urllib.request.Request(
        RELAY_URL + "/relay/provision",
        data=body,
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            creds = json.load(resp)
    except urllib.error.HTTPError as err:
        if err.code in (401, 403, 409):
            # Bad/disabled token or revoked id: retrying would only trip the
            # connector's auth-failure throttle. Fail the boot loudly.
            print(f"[bootstrap] provision refused: HTTP {err.code}", flush=True)
            sys.exit(1)
        if time.time() > deadline:
            raise
        print(f"[bootstrap] provision HTTP {err.code}; retrying", flush=True)
        time.sleep(3)
    except Exception as err:  # connector not up yet, DNS, etc.
        if time.time() > deadline:
            raise
        print(f"[bootstrap] connector not reachable ({err}); retrying", flush=True)
        time.sleep(3)

env_path = HOME / ".env"
env = env_path.read_text() if env_path.exists() else ""
for key, value in {
    "GATEWAY_RELAY_URL": RELAY_URL,
    "GATEWAY_RELAY_ID": creds["gatewayId"],
    "GATEWAY_RELAY_SECRET": creds["secret"],
    "GATEWAY_RELAY_DELIVERY_KEY": creds["deliveryKey"],
    "GATEWAY_RELAY_PLATFORMS": "discord",
}.items():
    pattern = re.compile(rf"^{re.escape(key)}=.*$", re.M)
    if pattern.search(env):
        env = pattern.sub(lambda _m: f"{key}={value}", env)
    else:
        if env and not env.endswith("\n"):
            env += "\n"
        env += f"{key}={value}\n"
env_path.write_text(env)
env_path.chmod(0o600)
print(f"[bootstrap] provisioned {SESSION_ID}; relay creds written", flush=True)
