#!/usr/bin/env python3
"""Optional root-owned host alert hook; channel sending runs as the agent."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

alert = json.loads(Path(sys.argv[1]).read_text())
key = hashlib.sha256(json.dumps(alert, sort_keys=True).encode()).hexdigest()[:24]
text = 'The new bot needs attention: ' + str(alert.get('reason', 'host alert')) + '. Check sudo bot-next status on the VM.'
subprocess.run(['/usr/sbin/runuser', '-u', 'bot-next', '--', '/usr/bin/python3',
                '/var/lib/bot-next/bin/telegram', '--home', '/var/lib/bot-next',
                'send', '--id', 'host-alert-' + key, '--file', '-'],
               input=text, text=True, check=True, stdout=subprocess.DEVNULL, timeout=70)
